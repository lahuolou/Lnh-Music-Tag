package scrape

import (
	"net/url"
	"strconv"
)

// neteaseSource searches NetEase Cloud Music (网易云音乐) public search API.
type neteaseSource struct{}

func (neteaseSource) Name() string  { return "netease" }
func (neteaseSource) Label() string { return "网易云音乐" }

func (neteaseSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://music.163.com/api/search/get/web?s=" + url.QueryEscape(query) +
		"&type=1&limit=" + strconv.Itoa(limit)
	headers := map[string]string{
		"Referer": "https://music.163.com/",
		"Origin":  "https://music.163.com",
	}
	var resp struct {
		Code   int `json:"code"`
		Result struct {
			Songs []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					ID     int64  `json:"id"`
					Name   string `json:"name"`
					PicURL string `json:"picUrl"`
				} `json:"album"`
				Duration int64 `json:"duration"`
			} `json:"songs"`
		} `json:"result"`
	}
	if err := c.getJSON(u, headers, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Result.Songs))
	for _, s := range resp.Result.Songs {
		artists := []string{}
		for _, a := range s.Artists {
			if a.Name != "" {
				artists = append(artists, a.Name)
			}
		}
		cover := ""
		albumID := ""
		if s.Album.PicURL != "" {
			cover = s.Album.PicURL + "?param=300y300"
		} else if s.Album.ID != 0 {
			albumID = strconv.FormatInt(s.Album.ID, 10)
		}
		out = append(out, SearchResult{
			Source: "netease", SourceID: strconv.FormatInt(s.ID, 10),
			Title: s.Name, Artists: artists, Album: s.Album.Name,
			Duration: int(s.Duration / 1000), CoverURL: cover, AlbumID: albumID,
		})
	}
	return out, nil
}
