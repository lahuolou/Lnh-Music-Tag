package scrape

import (
	"fmt"
	"net/url"
	"strconv"
)

// kugouSource searches Kugou Music (酷狗音乐) public complexsearch gateway.
type kugouSource struct{}

func (kugouSource) Name() string  { return "kugou" }
func (kugouSource) Label() string { return "酷狗音乐" }

func (kugouSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://complexsearch.kugou.com/v2/search/song?keyword=" +
		url.QueryEscape(query) + "&page=1&pagesize=" + strconv.Itoa(limit) + "&platform=WebFilter"
	var resp struct {
		Status int `json:"status"`
		Data   struct {
			Lists []struct {
				FileHash   string `json:"FileHash"`
				SongName   string `json:"SongName"`
				SingerName string `json:"SingerName"`
				AlbumName  string `json:"AlbumName"`
				Duration   int    `json:"Duration"`
				AlbumID    string `json:"AlbumID"`
			} `json:"lists"`
		} `json:"data"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Data.Lists))
	for _, s := range resp.Data.Lists {
		artists := splitNames(s.SingerName)
		cover := ""
		if s.AlbumID != "" {
			cover = fmt.Sprintf("https://imgessl.kugou.com/stdmusic/300/%s.jpg", s.AlbumID)
		}
		out = append(out, SearchResult{
			Source: "kugou", SourceID: s.FileHash,
			Title: s.SongName, Artists: artists, Album: s.AlbumName,
			Duration: s.Duration, CoverURL: cover,
		})
	}
	return out, nil
}
