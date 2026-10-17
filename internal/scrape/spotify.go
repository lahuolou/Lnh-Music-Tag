package scrape

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// spotifySource searches Spotify (requires Client ID + Secret from the
// Spotify Developer Dashboard, set on the settings page; the token is
// obtained via the client-credentials flow).
type spotifySource struct{}

func (spotifySource) Name() string  { return "spotify" }
func (spotifySource) Label() string { return "Spotify" }

func (spotifySource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if c.SpotifyID == "" || c.SpotifySecret == "" {
		return nil, fmt.Errorf("Spotify Client ID / Secret 未设置（设置页填写）")
	}
	token, err := spotifyToken(c)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.spotify.com/v1/search?q=" + url.QueryEscape(query) +
		"&type=track&limit=" + strconv.Itoa(limit)
	var resp struct {
		Tracks struct {
			Items []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				DurationMs int    `json:"duration_ms"`
				Artists    []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					Name   string `json:"name"`
					Images []struct {
						URL string `json:"url"`
					} `json:"images"`
					Release     string `json:"release_date"`
					TotalTracks int    `json:"total_tracks"`
					TrackNumber int    `json:"track_number"`
				} `json:"album"`
			} `json:"items"`
		} `json:"tracks"`
	}
	body, err := c.getBytes(u, map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Tracks.Items))
	for _, r := range resp.Tracks.Items {
		artists := make([]string, 0, len(r.Artists))
		for _, a := range r.Artists {
			if a.Name != "" {
				artists = append(artists, a.Name)
			}
		}
		cover := ""
		if len(r.Album.Images) > 0 {
			cover = r.Album.Images[0].URL
		}
		date := ""
		if len(r.Album.Release) >= 10 {
			date = r.Album.Release[:10]
		}
		out = append(out, SearchResult{
			Source: "spotify", SourceID: r.ID,
			Title: r.Name, Artists: artists, Album: r.Album.Name,
			Date: date, Duration: r.DurationMs / 1000, CoverURL: cover,
		})
	}
	return out, nil
}

func spotifyToken(c *Client) (string, error) {
	body := strings.NewReader("grant_type=client_credentials")
	u := "https://accounts.spotify.com/api/token"
	req, err := http.NewRequest(http.MethodPost, u, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(c.SpotifyID+":"+c.SpotifySecret)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("spotify token status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("spotify token 获取失败")
	}
	return tr.AccessToken, nil
}
