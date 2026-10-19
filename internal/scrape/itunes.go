package scrape

import (
	"net/url"
	"strconv"
	"strings"
)

// itunesSource searches the public iTunes Search API (no key required).
type itunesSource struct{}

func (itunesSource) Name() string  { return "itunes" }
func (itunesSource) Label() string { return "iTunes" }

func (itunesSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://itunes.apple.com/search?term=" + url.QueryEscape(query) +
		"&entity=song&limit=" + strconv.Itoa(limit)
	var resp struct {
		Results []struct {
			TrackID    int64  `json:"trackId"`
			TrackName  string `json:"trackName"`
			ArtistName string `json:"artistName"`
			Collection string `json:"collectionName"`
			Release    string `json:"releaseDate"`
			Artwork    string `json:"artworkUrl100"`
			Ms         int    `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		date := ""
		if len(r.Release) >= 10 {
			date = r.Release[:10]
		}
		cover := strings.Replace(r.Artwork, "100x100", "300x300", 1)
		out = append(out, SearchResult{
			Source: "itunes", SourceID: strconv.FormatInt(r.TrackID, 10),
			Title: r.TrackName, Artists: []string{r.ArtistName}, Album: r.Collection,
			Date: date, Duration: r.Ms / 1000, CoverURL: cover,
		})
	}
	return out, nil
}
