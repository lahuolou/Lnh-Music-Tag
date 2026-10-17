package scrape

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// fivesingSource searches 5sing (5sing.kugou.com) via its public JSONP search
// endpoint (no key required).
type fivesingSource struct{}

func (fivesingSource) Name() string  { return "fivesing" }
func (fivesingSource) Label() string { return "5sing" }

func (fivesingSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://search.5sing.kugou.com/home/json?keyword=" + url.QueryEscape(query) +
		"&type=1&page=1&rows=" + strconv.Itoa(limit)
	body, err := c.getBytes(u, map[string]string{"Referer": "https://5sing.kugou.com/"})
	if err != nil {
		return nil, err
	}
	// 接口返回 JSONP（jsonp123(...)），剥掉外壳再解析
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[i+1 : len(s)-1]
	}
	var resp struct {
		Data struct {
			List []struct {
				SongID   string `json:"songId"`
				SongName string `json:"songName"`
				Singer   string `json:"singerName"`
				Album    string `json:"albumName"`
				SongTime int    `json:"songTime"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Data.List))
	for _, r := range resp.Data.List {
		artists := []string{}
		for _, a := range splitNames(r.Singer) {
			artists = append(artists, a)
		}
		out = append(out, SearchResult{
			Source: "fivesing", SourceID: r.SongID,
			Title: r.SongName, Artists: artists, Album: r.Album,
			Duration: r.SongTime,
		})
	}
	return out, nil
}
