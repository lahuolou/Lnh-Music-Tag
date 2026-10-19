package scrape

import (
	"net/url"
	"strconv"
	"strings"
)

// miguSource searches Migu Music (咪咕音乐) via the current PC web search API
// (app.u.nf.migu.cn). The old music.migu.cn/v3 endpoints redirect to the v5 SPA
// and are no longer usable.
type miguSource struct{}

func (miguSource) Name() string  { return "migu" }
func (miguSource) Label() string { return "咪咕音乐" }

func (miguSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://app.u.nf.migu.cn/pc/v1.0/content/search_all.do?text=" +
		url.QueryEscape(query) + "&pageNo=1&pageSize=" + strconv.Itoa(limit) +
		"&searchSwitch=" + url.QueryEscape(`{"song":1}`)
	headers := map[string]string{"Referer": "https://music.migu.cn/"}
	var resp struct {
		Code string `json:"code"`
		Song struct {
			Result []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Singers []struct {
					Name string `json:"name"`
				} `json:"singers"`
				Albums []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"albums"`
				ImgItems []struct {
					ImgSizeType string `json:"imgSizeType"`
					Img         string `json:"img"`
				} `json:"imgItems"`
			} `json:"result"`
		} `json:"songResultData"`
	}
	if err := c.getJSON(u, headers, &resp); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, s := range resp.Song.Result {
		artists := []string{}
		for _, sg := range s.Singers {
			if sg.Name != "" {
				artists = append(artists, sg.Name)
			}
		}
		album := ""
		albumID := ""
		if len(s.Albums) > 0 {
			album = s.Albums[0].Name
			albumID = s.Albums[0].ID
		}
		cover := ""
		for _, im := range s.ImgItems {
			// 优先取中图（02），无则回退任意尺寸
			if im.Img != "" && (cover == "" || im.ImgSizeType == "02") {
				cover = im.Img
			}
		}
		out = append(out, SearchResult{
			Source: "migu", SourceID: s.ID, Title: strings.TrimSpace(s.Name),
			Artists: artists, Album: album, AlbumID: albumID, CoverURL: cover,
		})
	}
	return out, nil
}
