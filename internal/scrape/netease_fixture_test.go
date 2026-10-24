package scrape

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// roundTripFunc 允许测试用函数实现 http.RoundTripper。
type roundTripFunc func(r *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// neteaseFixtureClient 返回一个 Client，其所有 HTTP 请求都被重写到本地夹具服务器。
// 来源响应夹具：不依赖公网，CI 可稳定运行。
func neteaseFixtureClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return &Client{HTTP: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			u := *r.URL
			u.Scheme = "http"
			u.Host = host
			r2 := r.Clone(r.Context())
			r2.URL = &u
			return http.DefaultTransport.RoundTrip(r2)
		}),
		Timeout: 5 * time.Second,
	}}
}

// 网易云搜索响应正常夹具：验证字段提取（标题/艺人/专辑/时长/封面）。
func TestNeteaseSearchFixture(t *testing.T) {
	c := neteaseFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": 200,
			"result": {"songs": [
				{
					"id": 123456, "name": "勇气",
					"artists": [{"name": "梁静茹"}, {"name": ""}],
					"album": {"id": 789, "name": "恋爱的力量", "picUrl": "https://p.example/c.jpg"},
					"duration": 238000
				},
				{"id": 654321, "name": "勇气", "artists": [{"name": "棉子"}], "album": {"id": 0, "name": ""}, "duration": 0}
			]}
		}`))
	}))
	res, err := (neteaseSource{}).Search(c, "勇气 梁静茹", 8)
	if err != nil {
		t.Fatalf("search err: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("应解析 2 条，got %d", len(res))
	}
	first := res[0]
	if first.Source != "netease" || first.SourceID != "123456" || first.Title != "勇气" {
		t.Fatalf("首条字段错误: %+v", first)
	}
	if len(first.Artists) != 1 || first.Artists[0] != "梁静茹" {
		t.Fatalf("空艺人应过滤，got %v", first.Artists)
	}
	if first.Album != "恋爱的力量" || first.Duration != 238 || !strings.HasPrefix(first.CoverURL, "https://p.example/c.jpg?param=300y300") {
		t.Fatalf("专辑/时长/封面提取错误: %+v", first)
	}
	second := res[1]
	if second.SourceID != "654321" || second.Duration != 0 || second.AlbumID != "" {
		t.Fatalf("无封面字段应留空且不误填: %+v", second)
	}
}

// 网易云返回业务错误码：不应 panic，应返回空切片（nil error）。
func TestNeteaseSearchBusinessErrorFixture(t *testing.T) {
	c := neteaseFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code": 400, "result": null}`))
	}))
	res, err := (neteaseSource{}).Search(c, "不存在", 8)
	if err != nil {
		t.Fatalf("业务错误码不应作为硬错误: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("code!=200 应返回空结果，got %d", len(res))
	}
}

// HTTP 层失败（500/超时）：Search 必须返回 error，调用方据此走兜底源。
func TestNeteaseSearchHTTPErrorFixture(t *testing.T) {
	c := neteaseFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusInternalServerError)
	}))
	if _, err := (neteaseSource{}).Search(c, "勇气", 8); err == nil {
		t.Fatal("HTTP 500 应返回错误")
	}
}

// 歌词接口夹具：code=200 提取 lrc 原文；业务错误码返回降级错误。
func TestNeteaseLyricFixture(t *testing.T) {
	c := neteaseFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"code": 200, "lrc": {"lyric": "[00:00.00]梁静茹 - 勇气\n[00:10.00]终于做了这个决定"}}`
		if strings.Contains(r.URL.String(), "blocked") {
			body = `{"code": -1, "lrc": null}`
		}
		_, _ = w.Write([]byte(body))
	}))
	// 正常：提取 LRC
	lrc, err := neteaseLyric(c, "123456")
	if err != nil {
		t.Fatalf("lyric err: %v", err)
	}
	if !strings.Contains(lrc, "终于做了这个决定") {
		t.Fatalf("应提取歌词原文，got %q", lrc)
	}
	// 业务错误码：返回错误（触发调用方降级）
	if _, err := neteaseLyric(c, "blocked"); err == nil {
		t.Fatal("业务错误码应返回错误")
	}
}
