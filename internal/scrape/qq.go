package scrape

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// qqSource searches QQ Music (QQ音乐). Primary endpoint is the modern
// musicu.fcg gateway; the classic search_for_qq_cp endpoint is used as a
// fallback (musicu may return an empty list without a login cookie, and the
// old client_search_cp endpoint returns HTTP 500).
type qqSource struct{}

func (qqSource) Name() string  { return "qq" }
func (qqSource) Label() string { return "QQ音乐" }

// qqTrack is the shared internal shape for a QQ search hit.
type qqTrack struct {
	ID       string
	Name     string
	Interval int
	Singers  []string
	Album    string
	AlbumMid string
}

func (qqSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	if res, err := qqMusicu(c, query, limit); err == nil && len(res) > 0 {
		return res, nil
	}
	return qqLegacy(c, query, limit)
}

func qqMusicu(c *Client, query string, limit int) ([]SearchResult, error) {
	payload := map[string]any{
		"comm": map[string]any{"ct": 24},
		"req": map[string]any{
			"method": "DoSearchForQQMusicDesktop",
			"module": "music.search.SearchCgiService",
			"param": map[string]any{
				"query":        query,
				"num_per_page": limit,
				"page_num":     1,
				"search_type":  0,
			},
		},
	}
	body, _ := json.Marshal(payload)
	u := "https://u.y.qq.com/cgi-bin/musicu.fcg?data=" + url.QueryEscape(string(body))
	headers := map[string]string{"Referer": "https://y.qq.com/"}
	var resp struct {
		Code int `json:"code"`
		Req  struct {
			Code int `json:"code"`
			Data struct {
				Body struct {
					Song struct {
						List []struct {
							Mid      string `json:"mid"`
							Name     string `json:"name"`
							Interval int    `json:"interval"`
							Singer   []struct {
								Name string `json:"name"`
							} `json:"singer"`
							Album struct {
								Mid  string `json:"mid"`
								Name string `json:"name"`
							} `json:"album"`
						} `json:"list"`
					} `json:"song"`
				} `json:"body"`
			} `json:"data"`
		} `json:"req"`
	}
	if err := c.getJSON(u, headers, &resp); err != nil {
		return nil, err
	}
	tracks := make([]qqTrack, 0, len(resp.Req.Data.Body.Song.List))
	for _, s := range resp.Req.Data.Body.Song.List {
		singers := []string{}
		for _, sg := range s.Singer {
			if sg.Name != "" {
				singers = append(singers, sg.Name)
			}
		}
		tracks = append(tracks, qqTrack{
			ID: s.Mid, Name: s.Name, Interval: s.Interval,
			Singers: singers, Album: s.Album.Name, AlbumMid: s.Album.Mid,
		})
	}
	return qqToResults(tracks)
}

func qqLegacy(c *Client, query string, limit int) ([]SearchResult, error) {
	u := "https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp?p=1&n=" +
		fmt.Sprint(limit) + "&w=" + url.QueryEscape(query) + "&format=json"
	// 注意：不能用 getJSON——腾讯 soso 接口对 Accept: application/json 头会
	// 返回默认空响应（musicu 格式），必须不带 Accept 请求。
	body, err := c.getBytes(u, map[string]string{"Referer": "https://y.qq.com/"})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Song struct {
				List []struct {
					SongMid  string `json:"songmid"`
					Name     string `json:"songname"`
					Interval int    `json:"interval"`
					Singer   []struct {
						Name string `json:"name"`
					} `json:"singer"`
					Albumname string `json:"albumname"`
					AlbumMid  string `json:"albummid"`
				} `json:"list"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	tracks := make([]qqTrack, 0, len(resp.Data.Song.List))
	for _, s := range resp.Data.Song.List {
		singers := []string{}
		for _, sg := range s.Singer {
			if sg.Name != "" {
				singers = append(singers, sg.Name)
			}
		}
		tracks = append(tracks, qqTrack{
			ID: s.SongMid, Name: s.Name, Interval: s.Interval,
			Singers: singers, Album: s.Albumname, AlbumMid: s.AlbumMid,
		})
	}
	return qqToResults(tracks)
}

func qqToResults(tracks []qqTrack) ([]SearchResult, error) {
	out := make([]SearchResult, 0, len(tracks))
	for _, t := range tracks {
		cover := ""
		if t.AlbumMid != "" {
			cover = fmt.Sprintf("https://y.gtimg.cn/music/photo_new/T002R300x300M000%s.jpg", t.AlbumMid)
		}
		out = append(out, SearchResult{
			Source: "qq", SourceID: t.ID,
			Title: t.Name, Artists: t.Singers, Album: t.Album,
			Duration: t.Interval, CoverURL: cover,
		})
	}
	return out, nil
}
