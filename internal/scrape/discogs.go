package scrape

import (
	"fmt"
	"net/url"
	"strconv"
)

// discogsSource searches the Discogs Database API (requires a personal token
// set on the settings page; a token is issued when you register at
// https://www.discogs.com/settings/developers).
type discogsSource struct{}

func (discogsSource) Name() string  { return "discogs" }
func (discogsSource) Label() string { return "Discogs" }

func (discogsSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if c.DiscogsToken == "" {
		return nil, fmt.Errorf("Discogs Token 未设置（设置页填写）")
	}
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.discogs.com/database/search?q=" + url.QueryEscape(query) +
		"&type=release&per_page=" + strconv.Itoa(limit) +
		"&token=" + url.QueryEscape(c.DiscogsToken)
	var resp struct {
		Results []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Year  int    `json:"year"`
			Cover string `json:"cover_image"`
			Type  string `json:"type"`
		} `json:"results"`
	}
	if err := c.getJSON(u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Type != "release" {
			continue
		}
		// Discogs release titles are usually "Artist - Title"
		title, artist := r.Title, ""
		for _, sep := range []string{" - ", " – ", " — "} {
			if i := indexOf(r.Title, sep); i > 0 {
				title, artist = trimSpace(r.Title[i+len(sep):]), trimSpace(r.Title[:i])
				break
			}
		}
		date := ""
		if r.Year > 0 {
			date = strconv.Itoa(r.Year)
		}
		out = append(out, SearchResult{
			Source: "discogs", SourceID: strconv.FormatInt(r.ID, 10),
			Title: title, Artists: []string{artist}, Date: date, CoverURL: r.Cover,
		})
	}
	return out, nil
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
