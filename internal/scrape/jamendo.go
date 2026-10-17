package scrape

import (
	"fmt"
	"net/url"
	"strconv"
)

// jamendoSource searches Jamendo (requires a free client_id from
// https://devportal.jamendo.com, set on the settings page).
type jamendoSource struct{}

func (jamendoSource) Name() string  { return "jamendo" }
func (jamendoSource) Label() string { return "Jamendo" }

func (jamendoSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if c.JamendoClientID == "" {
		return nil, fmt.Errorf("Jamendo Client ID 未设置（设置页填写）")
	}
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.jamendo.com/v3.0/tracks/?client_id=" + url.QueryEscape(c.JamendoClientID) +
		"&search=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit) + "&format=json"
	var resp struct {
		Results []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist_name"`
			Album   string `json:"album_name"`
			Release string `json:"releasedate"`
			Audio   string `json:"audio"`
			Image   string `json:"image"`
			Dur     int    `json:"duration"`
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
		out = append(out, SearchResult{
			Source: "jamendo", SourceID: r.ID,
			Title: r.Name, Artists: []string{r.Artist.Name}, Album: r.Album,
			Date: date, Duration: r.Dur, CoverURL: r.Image,
		})
	}
	return out, nil
}
