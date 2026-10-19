package scrape

import (
	"net/url"
	"strconv"
)

// bilibiliSource searches Bilibili (哔哩哔哩) audio library.
type bilibiliSource struct{}

func (bilibiliSource) Name() string  { return "bilibili" }
func (bilibiliSource) Label() string { return "哔哩哔哩" }

func (bilibiliSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://api.bilibili.com/audio/music-service-c/s?mobi_app=web&search_type=music&keyword=" +
		url.QueryEscape(query) + "&page=1&pagesize=" + strconv.Itoa(limit)
	headers := map[string]string{"Referer": "https://www.bilibili.com/"}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Result []struct {
				ID     int64  `json:"id"`
				Title  string `json:"title"`
				Author string `json:"author"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(u, headers, &resp); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, s := range resp.Data.Result {
		out = append(out, SearchResult{
			Source: "bilibili", SourceID: strconv.FormatInt(s.ID, 10),
			Title: s.Title, Artists: splitNames(s.Author),
		})
	}
	return out, nil
}
