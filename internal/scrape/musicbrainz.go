// Package scrape implements metadata enrichment against MusicBrainz and the
// Cover Art Archive (no API key required, subject to MusicBrainz rate limits)
// plus an optional AcoustID fingerprint lookup hook for when an API key and a
// fingerprint are available.
package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	mbBase    = "https://musicbrainz.org/ws/2"
	coverBase = "https://coverartarchive.org"
	userAgent = "LNH-MusicTag/0.1 (local prototype; contact: dev@example.invalid)"
)

// API 速率限制（文档口径）：
//   - AcoustID 免费层：3 请求/秒（桶容量 1，保持稳定间隔）
//   - MusicBrainz：1 请求/秒/IP（硬性上限）
var (
	acoustidLimit  = rate.Limit(3)
	musicbrainzQPS = rate.Limit(1)
)

// Client is a MusicBrainz scraper.
type Client struct {
	HTTP *http.Client
	// AcoustIDAPIKey, when non-empty, enables AcoustID recording lookup.
	AcoustIDAPIKey string
	// LastFMKey enables Last.fm track search.
	LastFMKey string
	// DiscogsToken enables Discogs database search.
	DiscogsToken string
	// JamendoClientID enables Jamendo track search.
	JamendoClientID string
	// SpotifyID/SpotifySecret enable Spotify search (client credentials).
	SpotifyID     string
	SpotifySecret string

	acOnce, mbOnce sync.Once
	acLim, mbLim   *rate.Limiter
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// acoustidLimiter 返回 AcoustID 3 QPS 令牌桶（懒初始化）。
func (c *Client) acoustidLimiter() *rate.Limiter {
	c.acOnce.Do(func() { c.acLim = rate.NewLimiter(acoustidLimit, 1) })
	return c.acLim
}

// musicbrainzLimiter 返回 MusicBrainz 1 QPS 令牌桶（懒初始化）。
func (c *Client) musicbrainzLimiter() *rate.Limiter {
	c.mbOnce.Do(func() { c.mbLim = rate.NewLimiter(musicbrainzQPS, 1) })
	return c.mbLim
}

// RecordingResult is a single search hit.
type RecordingResult struct {
	MBID      string    `json:"mbid"`
	Title     string    `json:"title"`
	Artists   []string  `json:"artists"`
	Duration  int       `json:"duration"` // ms, 0 if unknown
	Releases  []Release `json:"releases"`
	Score     int       `json:"score"`
	FirstDate string    `json:"firstDate"`
}

// Release is a release associated with a recording.
type Release struct {
	MBID  string `json:"mbid"`
	Title string `json:"title"`
	Date  string `json:"date"`
}

// SearchRecordings queries MusicBrainz recordings by a lucene query string.
func (c *Client) SearchRecordings(query string, limit int) ([]RecordingResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := fmt.Sprintf("%s/recording?query=%s&fmt=json&limit=%d",
		mbBase, url.QueryEscape(query), limit)
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Recordings []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			Score        int    `json:"score"`
			Length       int    `json:"length"`
			ArtistCredit []struct {
				Name string `json:"name"`
			} `json:"artist-credit"`
			FirstReleaseDate string `json:"first-release-date"`
			Releases         []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Date  string `json:"date"`
			} `json:"releases"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse musicbrainz response: %w", err)
	}
	out := make([]RecordingResult, 0, len(resp.Recordings))
	for _, r := range resp.Recordings {
		rr := RecordingResult{
			MBID: r.ID, Title: r.Title, Duration: r.Length, Score: r.Score,
			FirstDate: r.FirstReleaseDate,
		}
		for _, ac := range r.ArtistCredit {
			if ac.Name != "" {
				rr.Artists = append(rr.Artists, ac.Name)
			}
		}
		for _, rel := range r.Releases {
			rr.Releases = append(rr.Releases, Release{MBID: rel.ID, Title: rel.Title, Date: rel.Date})
		}
		out = append(out, rr)
	}
	return out, nil
}

// BuildQuery builds a lucene search query from title and artist.
func BuildQuery(title, artist string) string {
	var parts []string
	if t := strings.TrimSpace(title); t != "" {
		parts = append(parts, fmt.Sprintf(`recording:"%s"`, escapeLucene(t)))
	}
	if a := strings.TrimSpace(artist); a != "" {
		parts = append(parts, fmt.Sprintf(`artist:"%s"`, escapeLucene(a)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

func escapeLucene(s string) string {
	r := strings.NewReplacer(`"`, `\"`, `\`, `\\`, `:`, `\:`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// ReleaseDetail holds enriched release metadata for album-artist, genres,
// release date and track numbers.
type ReleaseDetail struct {
	Artists []string
	Genres  []string
	Date    string
	Tracks  []ReleaseTrack
	// 扩展元素
	Language   string
	AlbumType  string
	TotalDiscs int
}

// ReleaseTrack is a track within a release.
type ReleaseTrack struct {
	Title  string
	Number string
}

// ReleaseDetail fetches a release's album-artist, genres, date and track list.
// Best-effort for metadata completion (genre/year/album artist/track number).
func (c *Client) ReleaseDetail(mbid string) (*ReleaseDetail, error) {
	u := fmt.Sprintf("%s/release/%s?inc=artist-credits+genres+recordings&fmt=json",
		mbBase, url.PathEscape(mbid))
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Date         string `json:"date"`
		Language     string `json:"language"`
		ArtistCredit []struct {
			Name string `json:"name"`
		} `json:"artist-credit"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		ReleaseGroup struct {
			PrimaryType string `json:"primary-type"`
		} `json:"release-group"`
		Media []struct {
			Position   int `json:"position"`
			TrackCount int `json:"track-count"`
			Tracks     []struct {
				Title  string `json:"title"`
				Number string `json:"number"`
			} `json:"tracks"`
		} `json:"media"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	rd := &ReleaseDetail{Date: resp.Date, Language: resp.Language, AlbumType: resp.ReleaseGroup.PrimaryType, TotalDiscs: len(resp.Media)}
	for _, ac := range resp.ArtistCredit {
		if ac.Name != "" {
			rd.Artists = append(rd.Artists, ac.Name)
		}
	}
	for _, g := range resp.Genres {
		if g.Name != "" {
			rd.Genres = append(rd.Genres, g.Name)
		}
	}
	for _, med := range resp.Media {
		for _, tr := range med.Tracks {
			rd.Tracks = append(rd.Tracks, ReleaseTrack{Title: tr.Title, Number: tr.Number})
		}
	}
	return rd, nil
}

// LookupByFingerprint identifies a recording from a Chromaprint fingerprint
// via the AcoustID API (requires AcoustIDAPIKey). Returns the best matching
// recording; error when the key is missing or nothing matches.
// 限速：3 QPS（AcoustID 免费层硬性上限）。
func (c *Client) LookupByFingerprint(fp string, durationSec int) (RecordingResult, error) {
	if c.AcoustIDAPIKey == "" {
		return RecordingResult{}, fmt.Errorf("AcoustID API Key 未设置（设置页填写）")
	}
	if err := c.acoustidLimiter().Wait(context.Background()); err != nil {
		return RecordingResult{}, err
	}
	// meta=recordings+releasegroups+compress：返回录音 + 发行组信息并 GZip 压缩。
	// 服务端 gzip 由 Go http.Transport 自动解压。
	u := fmt.Sprintf("https://api.acoustid.org/v2/lookup?client=%s&fingerprint=%s&meta=recordings+releasegroups+compress&format=json",
		url.QueryEscape(c.AcoustIDAPIKey), url.QueryEscape(fp))
	if durationSec > 0 {
		u += fmt.Sprintf("&duration=%d", durationSec)
	}
	body, err := c.get(u)
	if err != nil {
		return RecordingResult{}, err
	}
	var resp struct {
		Status string `json:"status"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
		Results []struct {
			Score      float64 `json:"score"`
			Recordings []struct {
				ID       string  `json:"id"`
				Title    string  `json:"title"`
				Duration float64 `json:"duration"`
				Artists  []struct {
					Name string `json:"name"`
				} `json:"artists"`
				ReleaseGroups []struct {
					FirstReleaseDate string `json:"first-release-date"`
					Title            string `json:"title"`
				} `json:"releasegroups"`
			} `json:"recordings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return RecordingResult{}, fmt.Errorf("parse acoustid response: %w", err)
	}
	if resp.Status != "ok" {
		msg := resp.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return RecordingResult{}, fmt.Errorf("acoustid: %s", msg)
	}
	best := RecordingResult{}
	for _, r := range resp.Results { // AcoustID 结果按分数降序
		for _, rec := range r.Recordings {
			if rec.Title == "" || len(rec.Artists) == 0 {
				continue
			}
			if best.MBID == "" { // 取第一个有效匹配（分数最高）
				best = RecordingResult{MBID: rec.ID, Title: rec.Title, Duration: int(rec.Duration)}
				for _, a := range rec.Artists {
					if a.Name != "" {
						best.Artists = append(best.Artists, a.Name)
					}
				}
				for _, rg := range rec.ReleaseGroups {
					best.Releases = append(best.Releases, Release{MBID: "", Title: rg.Title, Date: rg.FirstReleaseDate})
				}
				if len(best.Releases) > 0 {
					best.FirstDate = best.Releases[0].Date
				}
			}
		}
	}
	if best.MBID == "" {
		return RecordingResult{}, fmt.Errorf("音频指纹未匹配到录音（AcoustID 无结果）")
	}
	return best, nil
}

// RecordingDetail 是 AcoustID 命中后由 MusicBrainz Recording lookup 补全的
// 完整元数据：标题、艺术家、专辑、专辑艺术家、流派、年代、曲目号。
type RecordingDetail struct {
	MBID        string
	Title       string
	Artists     []string
	Album       string
	AlbumArtist []string
	Date        string
	Genres      []string
	TrackNumber string
	// 扩展元素（MusicBrainz Release/ReleaseGroup 提供）
	Language    string
	AlbumType   string
	DiscNumber  int
	TotalTracks int
	TotalDiscs  int
}

// LookupRecording queries MusicBrainz for the full metadata of a recording
// (inc=artists+releases+release-groups+tags) and picks the first release for
// album / album-artist / date / track-number enrichment.
// 限速：1 QPS（MusicBrainz 硬性上限）。
func (c *Client) LookupRecording(mbid string) (*RecordingDetail, error) {
	if mbid == "" {
		return nil, fmt.Errorf("recording MBID 为空")
	}
	if err := c.musicbrainzLimiter().Wait(context.Background()); err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/recording/%s?inc=artists+releases+release-groups+tags&fmt=json",
		mbBase, url.PathEscape(mbid))
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		ArtistCredit []struct {
			Name string `json:"name"`
		} `json:"artist-credit"`
		Tags []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tags"`
		ReleaseGroups []struct {
			FirstReleaseDate string `json:"first-release-date"`
			PrimaryType      string `json:"primary-type"`
		} `json:"release-groups"`
		Releases []struct {
			Title        string `json:"title"`
			Date         string `json:"date"`
			Language     string `json:"language"`
			ArtistCredit []struct {
				Name string `json:"name"`
			} `json:"artist-credit"`
			Media []struct {
				Position   int `json:"position"`
				TrackCount int `json:"track-count"`
				Tracks     []struct {
					Title  string `json:"title"`
					Number string `json:"number"`
				} `json:"tracks"`
			} `json:"media"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse musicbrainz recording: %w", err)
	}
	d := &RecordingDetail{MBID: resp.ID, Title: resp.Title}
	for _, ac := range resp.ArtistCredit {
		if ac.Name != "" {
			d.Artists = append(d.Artists, ac.Name)
		}
	}
	for _, g := range resp.Tags {
		if g.Name != "" {
			d.Genres = append(d.Genres, g.Name)
		}
	}
	if len(resp.ReleaseGroups) > 0 {
		if resp.ReleaseGroups[0].FirstReleaseDate != "" {
			d.Date = resp.ReleaseGroups[0].FirstReleaseDate
		}
		d.AlbumType = resp.ReleaseGroups[0].PrimaryType
	}
	if len(resp.Releases) > 0 {
		rel := resp.Releases[0]
		d.Album = rel.Title
		if d.Date == "" {
			d.Date = rel.Date
		}
		d.Language = rel.Language
		for _, ac := range rel.ArtistCredit {
			if ac.Name != "" {
				d.AlbumArtist = append(d.AlbumArtist, ac.Name)
			}
		}
		d.TotalDiscs = len(rel.Media)
		// 曲目号：在该发行版 media 中按标题匹配本录音的曲目；
		// 同时补全光盘编号与总音轨数。
		for _, med := range rel.Media {
			for _, tr := range med.Tracks {
				if strings.EqualFold(strings.TrimSpace(tr.Title), strings.TrimSpace(resp.Title)) && tr.Number != "" {
					d.TrackNumber = tr.Number
					d.DiscNumber = med.Position
					d.TotalTracks = med.TrackCount
					break
				}
			}
			if d.TrackNumber != "" {
				break
			}
		}
	}
	return d, nil
}

// FetchCover retrieves the front cover for a release as bytes. Returns
// (nil, nil) if no cover exists.
func (c *Client) FetchCover(releaseMBID string) ([]byte, error) {
	u := fmt.Sprintf("%s/release/%s/front-500", coverBase, url.PathEscape(releaseMBID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cover art archive status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) get(u string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
