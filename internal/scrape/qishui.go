package scrape

import (
	"net/url"
	"strconv"
)

// qishuiSource searches 汽水音乐 (Douyin's music service) via the Douyin web
// general-search API. Anti-bot headers/cookies may block this in some networks;
// failures degrade gracefully (auto tries the next source).
type qishuiSource struct{}

func (qishuiSource) Name() string  { return "qishui" }
func (qishuiSource) Label() string { return "汽水音乐" }

func (qishuiSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://www.douyin.com/aweme/v1/web/general/search/single/?keyword=" +
		url.QueryEscape(query) +
		"&search_channel=aweme_general&search_source=normal_search&offset=0&count=" +
		strconv.Itoa(limit)
	headers := map[string]string{"Referer": "https://www.douyin.com/"}
	var resp struct {
		StatusCode int `json:"status_code"`
		Data       []struct {
			AwemeInfo struct {
				AwemeID string `json:"aweme_id"`
				Music   struct {
					Title  string `json:"title"`
					Author string `json:"author"`
				} `json:"music"`
			} `json:"aweme_info"`
		} `json:"data"`
	}
	if err := c.getJSON(u, headers, &resp); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, d := range resp.Data {
		m := d.AwemeInfo.Music
		if m.Title == "" {
			continue
		}
		out = append(out, SearchResult{
			Source: "qishui", SourceID: d.AwemeInfo.AwemeID,
			Title: m.Title, Artists: splitNames(m.Author),
		})
	}
	return out, nil
}
