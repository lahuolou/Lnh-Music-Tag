package scrape

import (
	"fmt"
	"net/url"
	"strconv"
)

// lastfmSource searches Last.fm (requires a free API key set on the settings
// page: https://www.last.fm/api/account/create).
type lastfmSource struct{}

func (lastfmSource) Name() string  { return "lastfm" }
func (lastfmSource) Label() string { return "Last.fm" }

func (lastfmSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if c.LastFMKey == "" {
		return nil, fmt.Errorf("Last.fm API Key 未设置（设置页填写）")
	}
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://ws.audioscrobbler.com/2.0/?method=track.search&track=" +
		url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit) +
		"&api_key=" + url.QueryEscape(c.LastFMKey) + "&format=json"
	var resp struct {
		Results struct {
			TrackMatches struct {
				Track []struct {
					Name      string `json:"name"`
					Artist    string `json:"artist"`
					Album     string `json:"album"`
					URL       string `json:"url"`
					Duration  string `json:"duration"`
					Listeners string `json:"listeners"`
				} `json:"track"`
			} `json:"trackmatches"`
		} `json:"results"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results.TrackMatches.Track))
	for _, r := range resp.Results.TrackMatches.Track {
		dur := 0
		if d, err := strconv.Atoi(r.Duration); err == nil {
			dur = d / 1000
		}
		out = append(out, SearchResult{
			Source: "lastfm", SourceID: r.URL,
			Title: r.Name, Artists: []string{r.Artist}, Album: r.Album,
			Duration: dur,
		})
	}
	return out, nil
}
