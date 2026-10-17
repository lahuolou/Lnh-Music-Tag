package scrape

import (
	"net/url"
	"strconv"
)

// listenbrainzSource searches the public ListenBrainz API (no key required,
// data mirror of MusicBrainz).
type listenbrainzSource struct{}

func (listenbrainzSource) Name() string  { return "listenbrainz" }
func (listenbrainzSource) Label() string { return "ListenBrainz" }

func (listenbrainzSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.listenbrainz.org/1/search/recordings/?query=" +
		url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
	var resp struct {
		Recordings []struct {
			RecordingMBID string `json:"recording_mbid"`
			Title         string `json:"title"`
			Artist        string `json:"artist"`
			Release       string `json:"release"`
			Date          string `json:"date"`
			DurationMs    int    `json:"length"`
		} `json:"recordings"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Recordings))
	for _, r := range resp.Recordings {
		date := ""
		if len(r.Date) >= 10 {
			date = r.Date[:10]
		}
		out = append(out, SearchResult{
			Source: "listenbrainz", SourceID: r.RecordingMBID,
			Title: r.Title, Artists: []string{r.Artist}, Album: r.Release,
			Date: date, Duration: r.DurationMs / 1000,
		})
	}
	return out, nil
}
