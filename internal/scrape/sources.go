package scrape

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// SearchResult is the unified metadata hit returned by any scrape source.
type SearchResult struct {
	Source      string   `json:"source"`
	SourceID    string   `json:"sourceId"`
	ReleaseMBID string   `json:"releaseMBID,omitempty"`
	AlbumID     string   `json:"albumID,omitempty"`
	Title       string   `json:"title"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist []string `json:"albumArtist,omitempty"`
	Genre       []string `json:"genre,omitempty"`
	Date        string   `json:"date"` // year (YYYY) or full date
	TrackNumber int      `json:"trackNumber,omitempty"`
	Duration    int      `json:"duration"` // seconds
	CoverURL    string   `json:"coverURL,omitempty"`
	// 扩展元素（MusicBrainz 提供；其他源暂留空）
	Composer    []string `json:"composer,omitempty"`
	Lyricist    []string `json:"lyricist,omitempty"`
	Language    string   `json:"language,omitempty"`
	AlbumType   string   `json:"albumType,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	DiscNumber  int      `json:"discNumber,omitempty"`
	TotalTracks int      `json:"totalTracks,omitempty"`
	TotalDiscs  int      `json:"totalDiscs,omitempty"`
}

// Source is a metadata provider. Search receives a plain text query.
type Source interface {
	Name() string
	Label() string
	Search(c *Client, query string, limit int) ([]SearchResult, error)
}

// SourceInfo is the public descriptor of a source for the UI (dropdown /
// settings toggles). Enabled mirrors the persisted plugin switch: disabled
// sources are skipped by auto and rejected when selected directly.
type SourceInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

// MultiSource aggregates domestic + international scrape sources.
type MultiSource struct {
	client  *Client
	byName  map[string]Source
	order   []string
	enabled map[string]bool // 插件式开关：false 的源在 auto 中跳过、直接选择时报错
	emu     sync.RWMutex
}

// Source preference groups used by auto: Chinese queries hit domestic sources
// first, Latin-script queries hit international sources first.
var domesticOrder = []string{"netease", "qq", "kugou", "kuwo", "migu", "bilibili", "qishui", "bodian", "fivesing"}
var internationalOrder = []string{"itunes", "musicbrainz", "listenbrainz", "deezer", "lastfm", "spotify", "discogs", "jamendo"}

// NewMultiSource registers all built-in sources (domestic first so the UI
// dropdown and auto mode both prefer Chinese services). Every source starts
// enabled; the settings page can switch any of them off.
func NewMultiSource() *MultiSource {
	m := &MultiSource{client: NewClient(), byName: map[string]Source{}, enabled: map[string]bool{}}
	m.register(neteaseSource{})
	m.register(qqSource{})
	m.register(kugouSource{})
	m.register(kuwoSource{})
	m.register(miguSource{})
	m.register(bilibiliSource{})
	m.register(qishuiSource{})
	m.register(bodianSource{})
	m.register(fivesingSource{})
	m.register(musicbrainzSource{})
	m.register(itunesSource{})
	m.register(listenbrainzSource{})
	m.register(deezerSource{})
	m.register(lastfmSource{})
	m.register(spotifySource{})
	m.register(discogsSource{})
	m.register(jamendoSource{})
	for _, n := range m.order {
		m.enabled[n] = true
	}
	return m
}

// SetEnabled turns a source plugin on/off (persisted by the caller in config).
func (m *MultiSource) SetEnabled(name string, on bool) {
	m.emu.Lock()
	defer m.emu.Unlock()
	m.enabled[name] = on
}

// IsEnabled reports whether a source plugin is currently switched on.
func (m *MultiSource) IsEnabled(name string) bool {
	m.emu.RLock()
	defer m.emu.RUnlock()
	return m.enabled[name]
}

func (m *MultiSource) register(s Source) {
	if _, ok := m.byName[s.Name()]; !ok {
		m.byName[s.Name()] = s
		m.order = append(m.order, s.Name())
	}
}

// Client exposes the shared HTTP client for callers that need it.
func (m *MultiSource) Client() *Client { return m.client }

// Sources returns the ordered source list (for the UI dropdown / settings),
// including the always-on "auto" pseudo-source.
func (m *MultiSource) Sources() []SourceInfo {
	m.emu.RLock()
	defer m.emu.RUnlock()
	out := []SourceInfo{{Name: "auto", Label: "自动", Enabled: true}}
	for _, n := range m.order {
		out = append(out, SourceInfo{Name: n, Label: m.byName[n].Label(), Enabled: m.enabled[n]})
	}
	return out
}

// hasCJK reports whether the query contains any CJK (Chinese) character.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// Search runs a source by name. "auto" (or empty) prefers sources by language:
// Chinese queries try domestic sources first, foreign/Latin queries try
// international sources first, falling back to the other group. Disabled
// plugin sources are skipped in auto mode and rejected when named directly.
func (m *MultiSource) Search(name, query string, limit int) ([]SearchResult, error) {
	if name == "" {
		name = "auto"
	}
	if name != "auto" {
		s, ok := m.byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown source %q", name)
		}
		if !m.IsEnabled(name) {
			return nil, fmt.Errorf("source %q is disabled, enable it in Settings first", name)
		}
		return s.Search(m.client, query, limit)
	}
	var order []string
	if hasCJK(query) {
		order = append(domesticOrder, internationalOrder...)
	} else {
		order = append(internationalOrder, domesticOrder...)
	}
	var lastErr error
	for _, n := range order {
		s, ok := m.byName[n]
		if !ok || !m.IsEnabled(n) {
			continue
		}
		res, err := s.Search(m.client, query, limit)
		if err != nil {
			lastErr = err
			continue
		}
		if len(res) > 0 {
			return res, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return []SearchResult{}, nil
}

// SearchWithFallback 并发查询全部启用源：指定源（非 auto 时优先）+ 语言优先
// 链其余源同时发请求，合并所有命中返回，供调用方（scrapejob.go 的
// fuseMetadata）跨源按字段择优融合。原实现串行，第一个有命中的源就停，其余
// 源的结果全部浪费；且单个源挂起/限流时整条链等待。批量刮削、歌词、海报均受益。
func (m *MultiSource) SearchWithFallback(name, query string, limit int) ([]SearchResult, error) {
	order := []string{}
	if name != "" && name != "auto" && m.IsEnabled(name) {
		order = append(order, name)
	}
	var rest []string
	if hasCJK(query) {
		rest = append(domesticOrder, internationalOrder...)
	} else {
		rest = append(internationalOrder, domesticOrder...)
	}
	for _, n := range rest {
		if !contains(order, n) {
			order = append(order, n)
		}
	}
	type srcRes struct {
		results []SearchResult
		err     error
	}
	ch := make(chan srcRes, len(order))
	var wg sync.WaitGroup
	for _, n := range order {
		s, ok := m.byName[n]
		if !ok || !m.IsEnabled(n) {
			continue
		}
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			r, err := s.Search(m.client, query, limit)
			ch <- srcRes{r, err}
		}(s)
	}
	wg.Wait()
	close(ch)
	var out []SearchResult
	var lastErr error
	for r := range ch {
		if r.err != nil {
			lastErr = r.err
			continue
		}
		out = append(out, r.results...)
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, nil
	}
	return out, nil
}

// coverOrder builds the source order used to look for a cover: the source that
// produced the result first, then the language-preference chain.
func (m *MultiSource) coverOrder(prefer string) []string {
	order := []string{}
	if prefer != "" && prefer != "auto" {
		order = append(order, prefer)
	}
	all := append([]string{}, domesticOrder...)
	all = append(all, internationalOrder...)
	for _, n := range all {
		if n != prefer && !contains(order, n) {
			order = append(order, n)
		}
	}
	return order
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// FetchCoverFallback 在代表源无海报时按 query 补图：先首选源再遍历其余启用源，
// 全 query（标题+艺人）失败后第二轮用纯标题重试。wantArtist 非空时只采用艺人
// 匹配候选的封面，避免短/纯标题命中同名异人的错误专辑封面。
func (m *MultiSource) FetchCoverFallback(query, prefer, titleOnly, wantArtist string) ([]byte, error) {
	wantA := []string{}
	if w := strings.TrimSpace(wantArtist); w != "" {
		wantA = []string{w}
	}
	queries := []string{query}
	if titleOnly != "" && titleOnly != query {
		queries = append(queries, titleOnly)
	}
	for _, q := range queries {
		var lastErr error
		for _, n := range m.coverOrder(prefer) {
			s, ok := m.byName[n]
			if !ok || !m.IsEnabled(n) {
				continue
			}
			res, err := s.Search(m.client, q, 4)
			if err != nil || len(res) == 0 {
				if err != nil {
					lastErr = err
				}
				continue
			}
			// 优先取艺人匹配候选；无艺人约束（wantA 空）退回第一条。
			pick := -1
			for i, c := range res {
				if len(wantA) == 0 || ArtistSimilar(c.Artists, wantA) >= 0.5 {
					pick = i
					break
				}
			}
			if pick < 0 {
				continue
			}
			img, err := m.FetchCover(res[pick])
			if err != nil {
				lastErr = err
				continue
			}
			if len(img) > 0 {
				return img, nil
			}
		}
		if lastErr != nil {
			_ = lastErr
		}
	}
	return nil, nil
}

// Enrich fills album-artist, genre, release date, track number and the
// extended elements (language / album type / totals / disc number) from the
// source's release detail when available (currently MusicBrainz). Best-effort;
// never returns an error.
func (m *MultiSource) Enrich(sr *SearchResult) {
	if sr == nil || sr.Source != "musicbrainz" || sr.ReleaseMBID == "" {
		return
	}
	rd, err := m.client.ReleaseDetail(sr.ReleaseMBID)
	if err != nil {
		return
	}
	if len(rd.Artists) > 0 {
		sr.AlbumArtist = rd.Artists
	}
	if len(rd.Genres) > 0 {
		sr.Genre = rd.Genres
	}
	if rd.Date != "" {
		sr.Date = rd.Date
	}
	if rd.Language != "" {
		sr.Language = rd.Language
	}
	if rd.AlbumType != "" {
		sr.AlbumType = rd.AlbumType
	}
	if rd.TotalDiscs > 0 {
		sr.TotalDiscs = rd.TotalDiscs
	}
	// 曲目级信息（曲目号）按标题匹配补全；光盘编号/总音轨数由
	// recording 级 lookup（识别链路）提供，ReleaseDetail 平铺后无法还原。
	for _, tr := range rd.Tracks {
		if strings.EqualFold(strings.TrimSpace(tr.Title), strings.TrimSpace(sr.Title)) {
			if n, e := strconv.Atoi(tr.Number); e == nil && n > 0 {
				sr.TrackNumber = n
			}
			break
		}
	}
}

// FetchCover fetches cover art bytes for a result according to its source.
// 下载带源相关防盗链头（国内源 CDN 常见 Referer 校验），并对响应做图片校验
// （拦截 200 HTML 错误页/坏链接），失败自动重试一次。
func (m *MultiSource) FetchCover(sr SearchResult) ([]byte, error) {
	if sr.Source == "musicbrainz" && sr.ReleaseMBID != "" {
		return m.client.FetchCover(sr.ReleaseMBID)
	}
	if sr.Source == "netease" && sr.CoverURL == "" && sr.AlbumID != "" {
		return m.client.neteaseAlbumCover(sr.AlbumID)
	}
	if sr.CoverURL != "" {
		h := map[string]string{}
		switch sr.Source {
		case "qq", "kugou", "kuwo", "netease", "migu":
			h["Referer"] = "https://y.qq.com/"
		}
		return m.client.fetchImage(sr.CoverURL, h)
	}
	return nil, nil
}

// fetchImage downloads bytes with a retry and validates that the response is
// actually an image (Content-Type image/* 或长度足够且非 HTML 错误页)。
func (c *Client) fetchImage(u string, headers map[string]string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			last = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			last = rerr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if len(body) < 200 || strings.Contains(ct, "text/html") {
			last = errors.New("not an image response")
			continue
		}
		return body, nil
	}
	return nil, last
}

// neteaseAlbumCover resolves cover art via the NetEase album detail endpoint
// (the search API usually omits picUrl). Note: NetEase returns code -462 (a
// block) when Accept: application/json is sent, so this uses a plain request.
func (c *Client) neteaseAlbumCover(albumID string) ([]byte, error) {
	u := "https://music.163.com/api/album/" + url.PathEscape(albumID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://music.163.com/")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var o struct {
		Album struct {
			PicURL string `json:"picUrl"`
		} `json:"album"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, err
	}
	if o.Album.PicURL == "" {
		return nil, nil
	}
	return c.fetchImage(o.Album.PicURL+"?param=300y300", map[string]string{"Referer": "https://music.163.com/"})
}

// splitNames splits a combined artist/author string on ";", "、" and "/",
// trimming empty parts, e.g. "周杰伦;费玉清" -> ["周杰伦","费玉清"].
func splitNames(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		for _, q := range strings.Split(p, "、") {
			q = strings.TrimSpace(q)
			if q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

// getJSON performs a GET and decodes a JSON body, setting extra headers.
func (c *Client) getJSON(u string, headers map[string]string, out any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// getBytes performs a GET and returns the raw body bytes.
func (c *Client) getBytes(u string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
