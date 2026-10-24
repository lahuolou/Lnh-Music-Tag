package scrape

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/liuzl/gocc"
)

// simpForMatch 匹配用简繁归一：把繁体艺人/标题统一转简体再比较，
// 避免"候选艺人繁体（梁靜茹）vs 目标艺人简体（梁静茹）"匹配失败，
// 导致同名异艺人候选顶替正确结果。
var simpForMatch = func() func(string) string {
	cc, err := gocc.New("t2s")
	if err != nil {
		return func(s string) string { return s }
	}
	return func(s string) string {
		o, err := cc.Convert(s)
		if err != nil {
			return s
		}
		return o
	}
}()


// FetchLyrics returns the lyrics text for a result. Strategy:
//   - NetEase / QQ: fetch by the source song ID (best for Chinese tracks)
//   - otherwise: search QQ then NetEase by title+artist, picking only a
//     candidate whose artist matches the target (same-name different-artist
//     tracks must not steal lyrics, e.g. EO2《内疚》must not get 陈奕迅《内疚》),
//     then lrclib (international) with the same artist check.
func (m *MultiSource) FetchLyrics(sr SearchResult) (string, error) {
	// 选中候选自带 SourceID（前端手选 / 融合代表源）：直接按 ID 取，不二次搜索。
	if sr.Source == "netease" && sr.SourceID != "" && m.SourceHealthy("netease") {
		if l, err := neteaseLyric(m.client, sr.SourceID); err == nil && l != "" {
			m.recordSourceSuccess("netease")
			return l, nil
		} else if err != nil {
			m.recordSourceFailure("netease")
		}
	}
	if sr.Source == "qq" && sr.SourceID != "" && m.SourceHealthy("qq") {
		if l, err := qqLyric(m.client, sr.SourceID); err == nil && l != "" {
			m.recordSourceSuccess("qq")
			return l, nil
		} else if err != nil {
			m.recordSourceFailure("qq")
		}
	}
	if strings.TrimSpace(sr.Title) == "" {
		return "", fmt.Errorf("未找到歌词")
	}
	q := strings.TrimSpace(sr.Title + " " + strings.Join(sr.Artists, " "))

	// QQ 兜底：取多候选，按标题+艺人匹配择优，艺人不符一律不取（宁缺毋滥）。
	if m.SourceHealthy("qq") {
		if qres, err := (qqSource{}).Search(m.client, q, 6); err == nil {
			m.recordSourceSuccess("qq")
			if cand := pickLyricCandidate(qres, sr.Title, sr.Artists); cand.SourceID != "" {
				if l, err2 := qqLyric(m.client, cand.SourceID); err2 == nil && l != "" {
					m.recordSourceSuccess("qq")
					return l, nil
				}
			}
		} else {
			m.recordSourceFailure("qq")
		}
	}
	// 网易兜底：短超时；同样只取艺人匹配候选。
	if m.SourceHealthy("netease") {
		if res, err := (neteaseSource{}).Search(m.client, q, 6); err == nil {
			m.recordSourceSuccess("netease")
			if cand := pickLyricCandidate(res, sr.Title, sr.Artists); cand.SourceID != "" && cand.SourceID != sr.SourceID {
				if l, err2 := neteaseLyric(m.client, cand.SourceID); err2 == nil && l != "" {
					m.recordSourceSuccess("netease")
					return l, nil
				}
			}
		} else {
			m.recordSourceFailure("netease")
		}
	}
	// lrclib 国际源：结果自带 artistName，一并校验艺人。
	if m.SourceHealthy("lrclib") {
		if l, err := lrclibLyric(m.client, sr.Title, sr.Artists); err == nil && l != "" {
			m.recordSourceSuccess("lrclib")
			return l, nil
		} else if err != nil {
			m.recordSourceFailure("lrclib")
		}
	}
	return "", fmt.Errorf("未找到歌词")
}

// pickLyricCandidate 在搜索候选中挑出标题与目标最接近、且艺人匹配的一条。
// 当目标艺人非空时，艺人不匹配的候选直接淘汰（避免同名不同人串歌）；
// 目标艺人为空（未知艺人曲目）时退回标题匹配。返回空 SearchResult 表示无可取候选。
func pickLyricCandidate(list []SearchResult, wantTitle string, wantArtists []string) SearchResult {
	hasArtist := len(normalizeArtistTokens(strings.Join(wantArtists, " "))) > 0
	var best SearchResult
	bestScore := -1.0
	for _, c := range list {
		if c.SourceID == "" || strings.TrimSpace(c.Title) == "" {
			continue
		}
		am := artistSimilar(c.Artists, wantArtists)
		tm := lyricTitleSimilar(c.Title, wantTitle)
		// 目标有艺人时，艺人不匹配（am 过低）直接跳过。
		if hasArtist && am < 0.5 {
			continue
		}
		score := am*2 + tm // 艺人权重高于标题，同名人优先锁定同艺人
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return best
}

// lyricTitleSimilar 标题匹配度：归一化后相等=1，互相包含=0.8，否则 0。
func lyricTitleSimilar(got, want string) float64 {
	g := normForMatch(got)
	w := normForMatch(want)
	if g == "" || w == "" {
		return 0
	}
	if g == w {
		return 1
	}
	if strings.Contains(g, w) || strings.Contains(w, g) {
		return 0.8
	}
	return 0
}

// ArtistSimilar 报告候选艺人与目标艺人列表的最大匹配度（0~1），供融合/歌词复用。
func ArtistSimilar(got, want []string) float64 {
	return artistSimilar(got, want)
}

// artistSimilar 返回候选艺人与目标艺人的最大匹配度。
func artistSimilar(got, want []string) float64 {
	wantToks := normalizeArtistTokens(strings.Join(want, " "))
	if len(wantToks) == 0 {
		return 0
	}
	gotToks := normalizeArtistTokens(strings.Join(got, " "))
	best := 0.0
	for _, wt := range wantToks {
		for _, gt := range gotToks {
			if gt == "" || wt == "" {
				continue
			}
			s := 0.0
			switch {
			case gt == wt:
				s = 1
			case strings.Contains(gt, wt) || strings.Contains(wt, gt):
				if len([]rune(gt)) >= 2 && len([]rune(wt)) >= 2 {
					s = 0.7
				}
			}
			if s > best {
				best = s
			}
		}
	}
	return best
}

// normalizeArtistTokens 把艺人字符串拆成归一化 token：小写、去括号备注、
// 拆分 &/、/,/feat./ft./with/x/vs. 等并列，去标点压缩空白。
func normalizeArtistTokens(s string) []string {
	s = simpForMatch(strings.ToLower(strings.TrimSpace(s)))
	// 去掉括号及其内容（现场/feat 备注）
	for {
		lo := strings.IndexAny(s, "([（")
		if lo < 0 {
			break
		}
		cl := strings.IndexAny(s[lo:], ")]）")
		if cl < 0 {
			s = strings.TrimSpace(s[:lo])
			break
		}
		s = s[:lo] + s[lo+cl+1:]
	}
	// 并列/合作标识替换为分隔符
	for _, sep := range []string{"&", "，", ",", "/", "、", "feat.", "feat", "ft.", "ft", "with", " x ", "vs.", "vs"} {
		s = strings.ReplaceAll(s, sep, "|")
	}
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = normForMatch(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// normForMatch 归一化用于比较的文本：小写、去所有标点与空白。
func normForMatch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// neteaseLyric fetches LRC lyrics by NetEase song id. NetEase returns a block
// (code -462) when Accept: application/json is sent, so use a plain request.
// 网易接口偶发限流：请求挂起不响应或返回空。用独立 3s 短超时 + 2 次重试，
// 失败快速让位给后续兜底（共享 client 的 10s 会让失败路径拖慢整条流水线）。
func neteaseLyric(c *Client, songID string) (string, error) {
	// 独立短超时 client：网易接口偶发挂起时快速失败让位兜底；
	// 继承共享 Transport，使测试可通过注入 RoundTripper 提供响应夹具。
	hc := &http.Client{Timeout: 3 * time.Second, Transport: c.HTTP.Transport}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		u := "https://music.163.com/api/song/lyric?id=" + url.PathEscape(songID) + "&lv=1&kv=1&tv=-1"
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Referer", "https://music.163.com/")
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var o struct {
			Code int `json:"code"`
			Lrc  struct {
				Lyric string `json:"lyric"`
			} `json:"lrc"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&o)
		resp.Body.Close()
		if decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		l := strings.TrimSpace(o.Lrc.Lyric)
		if o.Code != 200 || l == "" {
			lastErr = fmt.Errorf("netease lyric not available (code %d)", o.Code)
			continue
		}
		return l, nil
	}
	return "", lastErr
}

// qqLyric fetches lyrics by QQ song mid. The lyric may be base64-encoded.
func qqLyric(c *Client, songmid string) (string, error) {
	u := "https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg?songmid=" +
		url.QueryEscape(songmid) + "&format=json&nobase64=1&songtype=0"
	headers := map[string]string{"Referer": "https://y.qq.com/"}
	var o struct {
		Retcode int    `json:"retcode"`
		Lyric   string `json:"lyric"`
	}
	if err := c.getJSON(u, headers, &o); err != nil {
		return "", err
	}
	if o.Lyric == "" {
		return "", fmt.Errorf("qq lyric not available")
	}
	if b, err := base64.StdEncoding.DecodeString(o.Lyric); err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(o.Lyric), nil
}

// lrclibLyric fetches lyrics from lrclib (no key required, international).
// 结果自带 trackName/artistName，按标题+艺人匹配择优，目标艺人非空时
// 跳过艺人不符的条目，避免同名不同人串歌。
func lrclibLyric(c *Client, title string, artists []string) (string, error) {
	q := strings.TrimSpace(title + " " + strings.Join(artists, " "))
	u := "https://lrclib.net/api/search?q=" + url.QueryEscape(q)
	var out []struct {
		Synced    string `json:"syncedLyrics"`
		Plain     string `json:"plainLyrics"`
		TrackName string `json:"trackName"`
		Artist    string `json:"artistName"`
	}
	if err := c.getJSON(u, nil, &out); err != nil {
		return "", err
	}
	hasArtist := len(normalizeArtistTokens(strings.Join(artists, " "))) > 0
	// score: 艺人匹配*2 + 标题匹配；先选同步歌词，再选纯文本。
	pick := func(synced bool) (string, bool) {
		best, bestScore := "", -1.0
		for _, it := range out {
			txt := it.Plain
			if synced {
				txt = it.Synced
			}
			if txt == "" {
				continue
			}
			am := artistSimilar([]string{it.Artist}, artists)
			tm := lyricTitleSimilar(it.TrackName, title)
			if hasArtist && am < 0.5 {
				continue
			}
			score := am*2 + tm
			if score > bestScore {
				bestScore = score
				best = txt
			}
		}
		return best, best != ""
	}
	if l, ok := pick(true); ok {
		return l, nil
	}
	if l, ok := pick(false); ok {
		return l, nil
	}
	return "", fmt.Errorf("lrclib no lyrics")
}
