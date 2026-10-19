package scrape

import (
	"net/url"
	"strconv"
)

// deezerSource searches the public Deezer API (no key required).
type deezerSource struct{}

func (deezerSource) Name() string  { return "deezer" }
func (deezerSource) Label() string { return "Deezer" }

func (deezerSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.deezer.com/search?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
	var resp struct {
		Data []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				Title string `json:"title"`
				Cover string `json:"cover_medium"`
				Date  string `json:"release_date"`
			} `json:"album"`
			Duration int `json:"duration"`
		} `json:"data"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Data))
	for _, r := range resp.Data {
		date := ""
		if len(r.Album.Date) >= 10 {
			date = r.Album.Date[:10]
		}
		out = append(out, SearchResult{
			Source: "deezer", SourceID: strconv.FormatInt(r.ID, 10),
			Title: r.Title, Artists: []string{r.Artist.Name}, Album: r.Album.Title,
			Date: date, Duration: r.Duration, CoverURL: r.Album.Cover,
		})
	}
	return out, nil
}
