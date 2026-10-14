package scrape

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchLyrics returns the lyrics text for a result. Strategy:
//   - NetEase / QQ: fetch by the source song ID (best for Chinese tracks)
//   - otherwise: NetEase search by title+artist, then lrclib (international)
func (m *MultiSource) FetchLyrics(sr SearchResult) (string, error) {
	if sr.Source == "netease" && sr.SourceID != "" {
		if l, err := neteaseLyric(m.client, sr.SourceID); err == nil && l != "" {
			return l, nil
		}
	}
	if sr.Source == "qq" && sr.SourceID != "" {
		if l, err := qqLyric(m.client, sr.SourceID); err == nil && l != "" {
			return l, nil
		}
	}
	if sr.Title != "" {
		q := strings.TrimSpace(sr.Title + " " + strings.Join(sr.Artists, " "))
		// QQ 搜索兜底（快；网易挂起/无版权时改走 QQ 歌词接口）
		if qres, err := (qqSource{}).Search(m.client, q, 1); err == nil && len(qres) > 0 && qres[0].SourceID != "" {
			if l, err2 := qqLyric(m.client, qres[0].SourceID); err2 == nil && l != "" {
				return l, nil
			}
		}
		// 网易搜索兜底（中文歌命中率高；接口偶发挂起，短超时快速让位）
		res, err := neteaseSource{}.Search(m.client, q, 1)
		if err == nil && len(res) > 0 && res[0].SourceID != "" && res[0].SourceID != sr.SourceID {
			if l, err2 := neteaseLyric(m.client, res[0].SourceID); err2 == nil && l != "" {
				return l, nil
			}
		}
		if l, err := lrclibLyric(m.client, sr.Title, sr.Artists); err == nil && l != "" {
			return l, nil
		}
	}
	return "", fmt.Errorf("未找到歌词")
}

// neteaseLyric fetches LRC lyrics by NetEase song id. NetEase returns a block
// (code -462) when Accept: application/json is sent, so use a plain request.
// 网易接口偶发限流：请求挂起不响应或返回空。用独立 3s 短超时 + 2 次重试，
// 失败快速让位给后续兜底（共享 client 的 10s 会让失败路径拖慢整条流水线）。
func neteaseLyric(c *Client, songID string) (string, error) {
	hc := &http.Client{Timeout: 3 * time.Second}
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
func lrclibLyric(c *Client, title string, artists []string) (string, error) {
	q := strings.TrimSpace(title + " " + strings.Join(artists, " "))
	u := "https://lrclib.net/api/search?q=" + url.QueryEscape(q)
	var out []struct {
		Synced string `json:"syncedLyrics"`
		Plain  string `json:"plainLyrics"`
	}
	if err := c.getJSON(u, nil, &out); err != nil {
		return "", err
	}
	for _, it := range out {
		if it.Synced != "" {
			return it.Synced, nil
		}
	}
	for _, it := range out {
		if it.Plain != "" {
			return it.Plain, nil
		}
	}
	return "", fmt.Errorf("lrclib no lyrics")
}
