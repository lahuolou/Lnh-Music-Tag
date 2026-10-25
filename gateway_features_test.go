package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

func newTestGateway(t *testing.T, kind string, tracks ...*model.Track) *mediaGateway {
	t.Helper()
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	t.Cleanup(func() { cfg.Close() })
	s := newStore(nil)
	for _, tr := range tracks {
		s.add(tr)
	}
	return &mediaGateway{
		library: s, cfg: cfg, kind: kind,
		tokens: map[string]time.Time{}, plays: map[string]playState{},
	}
}

// 评分：rateItem 写 streaming_ratings，ratingFor 回读；0 清除。
func TestRateItemWritesSharedRatings(t *testing.T) {
	g := newTestGateway(t, "jellyfin",
		&model.Track{ID: "t1", Path: "/m/a.flac", FileName: "歌手 - 标题.flac"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Items/t1/Rating?Rating=5", nil)
	g.rateItem(rec, req, "t1")
	if rec.Code != http.StatusOK {
		t.Fatalf("rateItem status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ratingFor(g.cfg, "t1"); got != 5 {
		t.Fatalf("ratingFor t1=%d, want 5", got)
	}

	// Rating=0 清除
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/Items/t1/Rating?Rating=0", nil)
	g.rateItem(rec, req, "t1")
	if got := ratingFor(g.cfg, "t1"); got != 0 {
		t.Fatalf("after clear ratingFor t1=%d, want 0", got)
	}

	// 非法曲目 404，非法评分 400
	rec = httptest.NewRecorder()
	g.rateItem(rec, httptest.NewRequest(http.MethodPost, "/Items/nope/Rating?Rating=5", nil), "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing item status=%d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	g.rateItem(rec, httptest.NewRequest(http.MethodPost, "/Items/t1/Rating?Rating=9", nil), "t1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid rating status=%d, want 400", rec.Code)
	}
}

// 歌单：建歌单（body Name/Ids），追加去重，EntryIds 移除。
func TestPlaylistCreateAddRemove(t *testing.T) {
	g := newTestGateway(t, "jellyfin",
		&model.Track{ID: "t1", Path: "/m/a.flac", FileName: "s1.flac"},
		&model.Track{ID: "t2", Path: "/m/b.flac", FileName: "s2.flac"},
		&model.Track{ID: "t3", Path: "/m/c.flac", FileName: "s3.flac"})

	// 建歌单
	body := `{"Name":"我的歌单","Ids":["t1","t1"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Playlists", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	g.createPlaylistJf(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	pls := g.loadPlaylists()
	if len(pls) != 1 {
		t.Fatalf("want 1 playlist, got %d", len(pls))
	}
	if len(pls[0].TrackIDs) != 1 || pls[0].TrackIDs[0] != "t1" {
		t.Fatalf("dedup TrackIDs=%v, want [t1]", pls[0].TrackIDs)
	}
	plID := pls[0].ID

	// 追加 t2，重复 t1 去重
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/Playlists/"+plID+"/Items?Ids=t2,t1", nil)
	g.mutatePlaylistItems(rec, req, plID)
	if rec.Code != http.StatusOK {
		t.Fatalf("add items status=%d body=%s", rec.Code, rec.Body.String())
	}
	pls = g.loadPlaylists()
	got := pls[0].TrackIDs
	if len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Fatalf("after add TrackIDs=%v, want [t1 t2]", got)
	}

	// 移除 t1
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/Playlists/"+plID+"/Items?EntryIds=t1", nil)
	g.mutatePlaylistItems(rec, req, plID)
	pls = g.loadPlaylists()
	if len(pls[0].TrackIDs) != 1 || pls[0].TrackIDs[0] != "t2" {
		t.Fatalf("after remove TrackIDs=%v, want [t2]", pls[0].TrackIDs)
	}

	// 非法曲目加歌 404
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/Playlists/"+plID+"/Items?Ids=nope", nil)
	g.mutatePlaylistItems(rec, req, plID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("add invalid item status=%d, want 404", rec.Code)
	}

	// 建歌单缺 Name 400
	rec = httptest.NewRecorder()
	g.createPlaylistJf(rec, httptest.NewRequest(http.MethodPost, "/Playlists", strings.NewReader(`{"Ids":["t2"]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name status=%d, want 400", rec.Code)
	}
}

// JF/Emby authenticate 仅用流媒体密码 stream_pass：正确密码 200 发 token，错误 401。
func TestMediaAuthStreamPass(t *testing.T) {
	g := newTestGateway(t, "jellyfin")
	_ = g.cfg.Set("stream_pass", "test")

	// 正确流媒体密码 → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Password":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	g.authenticate(rec, req, "jellyfin")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct stream pass status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ok struct {
		AccessToken string `json:"AccessToken"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ok)
	if ok.AccessToken == "" {
		t.Fatalf("no AccessToken in %s", rec.Body.String())
	}

	// 错误密码 → 401
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"admin","Password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	g.authenticate(rec, req, "jellyfin")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pass status=%d, want 401", rec.Code)
	}
}

// JF/Emby 路由分发须识别标准登录路径：裸、/Public、/{userId} 三种都能 200 发 token，
// 不得落到 authorized 拦截返回 401（真实客户端使用 /Users/Public/AuthenticateByName）。
func TestMediaPublicLoginRoute(t *testing.T) {
	paths := []string{
		"users/authenticatebyname",
		"users/public/authenticatebyname",
		"users/lnh-admin/authenticatebyname",
	}
	for _, kind := range []string{"jellyfin", "emby"} {
		g := newTestGateway(t, kind)
		_ = g.cfg.Set("stream_pass", "test")
		for _, p := range paths {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/"+p, strings.NewReader(`{"Username":"admin","Pw":"test"}`))
			req.Header.Set("Content-Type", "application/json")
			if kind == "jellyfin" {
				g.serveJellyfin(rec, req, p)
			} else {
				g.serveEmby(rec, req, p)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s status=%d body=%s", kind, p, rec.Code, rec.Body.String())
			}
			var ok struct {
				AccessToken string `json:"AccessToken"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &ok)
			if ok.AccessToken == "" {
				t.Fatalf("%s %s no token: %s", kind, p, rec.Body.String())
			}
		}
	}
}

// ND login 仅认流媒体密码：正确 200 发 token，错误/未配置 401。
func TestNDLoginStreamPass(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")
	n := &navidromeAPI{library: newStore(nil), cfg: cfg}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"admin","password":"test"}`))
	n.login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ND correct pass status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ok)
	if ok.Token == "" {
		t.Fatalf("ND no token: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"admin","password":"nope"}`))
	n.login(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ND wrong pass status=%d, want 401", rec.Code)
	}
}

// Subsonic authorized 仅认流媒体密码明文；错误密码拒绝，未配置拒绝。
func TestSubsonicAuthorizedStreamPass(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	a := &subsonicAPI{cfg: cfg}

	// 未配置 stream_pass → 拒绝
	if a.authorized(httptest.NewRequest(http.MethodGet, "/rest/ping.view?p=test", nil)) {
		t.Fatal("unconfigured stream_pass must be rejected")
	}
	_ = cfg.Set("stream_pass", "test")

	if !a.authorized(httptest.NewRequest(http.MethodGet, "/rest/ping.view?p=test", nil)) {
		t.Fatal("correct stream_pass plaintext must be accepted")
	}
	if a.authorized(httptest.NewRequest(http.MethodGet, "/rest/ping.view?p=wrong", nil)) {
		t.Fatal("wrong password must be rejected")
	}
	// 空密码无 token → 拒绝（不再宽松握手）
	if a.authorized(httptest.NewRequest(http.MethodGet, "/rest/ping.view?u=admin", nil)) {
		t.Fatal("empty credential must be rejected")
	}
}

// ND songJSON 共享评分回传（与 Subsonic 同一 streaming_ratings）。
func TestNDSongJSONSharesRating(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = json.Unmarshal([]byte("{}"), &map[string]int{})
	_ = cfg.Set("streaming_ratings", `{"t1":4}`)
	n := &navidromeAPI{library: newStore(nil), cfg: cfg}
	tr := &model.Track{ID: "t1", Path: "/m/a.flac", FileName: "歌手 - 标题.flac"}
	n.library.add(tr)
	dto := n.songJSON(tr)
	if dto["rating"].(int) != 4 {
		t.Fatalf("ND rating=%v, want 4", dto["rating"])
	}
}

// Emby 路由顺序回归：POST 评分、POST 建歌单不得被通用曲目读取/歌单列表吞掉。
func TestEmbyRouteWritesBeforeRead(t *testing.T) {
	g := newTestGateway(t, "emby", &model.Track{ID: "t1", Path: "/m/a.flac", FileName: "歌手 - 标题.flac"})
	_ = g.cfg.Set("stream_pass", "test")
	auth := func(r *http.Request) { r.Header.Set("X-Emby-Token", g.tokenFor("admin")) }

	// POST 评分：经 serveEmby 路由分发须落库，不得落到通用 item 读取。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/lnh-admin/Items/t1/Rating?Rating=3", nil)
	auth(req)
	g.serveEmby(rec, req, "users/lnh-admin/items/t1/rating")
	if got := ratingFor(g.cfg, "t1"); got != 3 {
		t.Fatalf("emby route rating=%d, want 3", got)
	}

	// POST 建歌单：不得被 GET 歌单列表吞掉。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/Playlists", strings.NewReader(`{"Name":"rv","Ids":["t1"]}`))
	auth(req)
	g.serveEmby(rec, req, "playlists")
	var created struct {
		ID         string `json:"Id"`
		ChildCount int    `json:"ChildCount"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ChildCount != 1 {
		t.Fatalf("emby create playlist ChildCount=%d body=%s", created.ChildCount, rec.Body.String())
	}
}

// getStatistics 回传真实歌曲/专辑/歌手/歌单计数，默认标签曲目归一到单个未知艺人/专辑。
func TestSubsonicStatisticsCounts(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	s := newStore(nil)
	for _, id := range []string{"t1", "t2", "t3"} {
		s.add(&model.Track{ID: id, Path: "/m/" + id + ".flac", FileName: id + ".flac"})
	}
	_ = cfg.Set("stream_pass", "test")
	a := &subsonicAPI{library: s, cfg: cfg}

	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/rest/getStatistics.view?u=admin&p=test&f=json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	r := raw["subsonic-response"].(map[string]any)
	if r["status"] != "ok" {
		t.Fatalf("statistics status=%v", r["status"])
	}
	st := r["statistics"].(map[string]any)
	cnt := func(k string) string {
		switch v := st[k].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', 0, 64)
		}
		return ""
	}
	if cnt("songCount") != "3" || cnt("albumCount") != "1" || cnt("artistCount") != "1" || cnt("playlistCount") != "0" {
		t.Fatalf("statistics=%v, want 3/1/1/0", st)
	}
}

// 根路径 *.view（不带 /rest 前缀）同样走 Subsonic 网关（协议自动路由：客户端 Base URL 填根地址即可）。
func TestSubsonicRootViewPath(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: "/m/t1.flac", FileName: "t1.flac"})
	_ = cfg.Set("stream_pass", "test")
	a := &subsonicAPI{library: s, cfg: cfg}

	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	// 根路径 /getStatistics.view（Base URL 填 http://IP:10248、未带 /rest 前缀）
	resp, err := http.Get(srv.URL + "/getStatistics.view?u=admin&p=test&f=json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	r := raw["subsonic-response"].(map[string]any)
	if r["status"] != "ok" {
		t.Fatalf("root .view status=%v, want ok", r["status"])
	}
	// 未认证拒绝（Subsonic 风格：HTTP 200 + body status=failed；ping 属公开端点不校验）
	bad, err := http.Get(srv.URL + "/getStatistics.view?u=admin&p=wrong&f=json")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	var badRaw map[string]any
	if err := json.NewDecoder(bad.Body).Decode(&badRaw); err != nil {
		t.Fatal(err)
	}
	br := badRaw["subsonic-response"].(map[string]any)
	if br["status"] == "ok" {
		t.Fatal("root .view with wrong password must be rejected")
	}
}

// Subsonic 建歌单加歌后 getPlaylist 返回曲目（entry 节点契约）。
// Subsonic getPlaylist 标准把曲目序列化为 <entry> 子节点；音流按 entry 解析，
// 若输出成 song，详情页会读不到任何曲目（列表计数仍对）。这里断言详情返回
// entry 数组且 songCount 与条目数一致。
func TestSubsonicPlaylistEntryNodes(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")
	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: "/m/a.flac", FileName: "a.flac", Tags: map[string]string{"TITLE": "歌一", "ARTIST": "甲"}})
	s.add(&model.Track{ID: "t2", Path: "/m/b.flac", FileName: "b.flac", Tags: map[string]string{"TITLE": "歌二", "ARTIST": "乙"}})
	a := &subsonicAPI{library: s, cfg: cfg}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	get := func(path string) map[string]any {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var env map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return env["subsonic-response"].(map[string]any)
	}

	get("/rest/createPlaylist.view?u=admin&p=test&f=json&name=就他&songIdToAdd=t1&songIdToAdd=t2")
	list := get("/rest/getPlaylists.view?u=admin&p=test&f=json")
	pls := list["playlists"].(map[string]any)["playlist"].([]any)
	pid := pls[0].(map[string]any)["id"].(string)

	detail := get("/rest/getPlaylist.view?u=admin&p=test&f=json&id=" + pid)
	pl := detail["playlist"].(map[string]any)
	entries, ok := pl["entry"].([]any)
	if !ok {
		t.Fatalf("playlist 曲目应为 entry 数组，got keys=%v", pl)
	}
	if len(entries) != 2 {
		t.Fatalf("entry 数=%d, want 2", len(entries))
	}
	if sc, _ := pl["songCount"].(float64); int(sc) != 2 {
		t.Fatalf("songCount=%v, want 2", pl["songCount"])
	}
}

// 专辑节点必须同时带 name 与 title：音流等客户端只读 title，缺 title 会把
// 专辑名渲染成占位“?”。
func TestAlbumNodeCarriesTitle(t *testing.T) {
	ts := []*model.Track{{ID: "t1", Tags: map[string]string{"ALBUM": "EO2专辑"}}}
	n := albumNode(ts, "al-1", "EO2专辑", "EO2", "ar-1")
	if n.Attr["title"] != "EO2专辑" {
		t.Fatalf("album node title=%q, want EO2专辑 (attrs=%v)", n.Attr["title"], n.Attr)
	}
	if n.Attr["coverArt"] != "t1" {
		t.Fatalf("album coverArt should reference track id, got %q", n.Attr["coverArt"])
	}
}

// 歌单计数只统计库中仍存在的有效曲目：失效/已删除的残留 id 不计入 ChildCount，
// 避免列表“3首”而详情只有 2 首的漂移。
func TestPlaylistCountSkipsStaleIDs(t *testing.T) {
	g := newTestGateway(t, "jellyfin",
		&model.Track{ID: "t1", Path: "/m/a.flac", FileName: "a.flac"},
		&model.Track{ID: "t2", Path: "/m/b.flac", FileName: "b.flac"})
	pl := subsonicPlaylist{ID: "pl-x", Name: "盒饭", TrackIDs: []string{"t1", "t2", "t999"}}
	if c := g.validSongCount(pl); c != 2 {
		t.Fatalf("validSongCount=%d, want 2 (stale t999 skipped)", c)
	}
	dto := g.playlistDTO(pl)
	if cc, _ := dto["ChildCount"].(int); cc != 2 {
		t.Fatalf("ChildCount=%v, want 2", dto["ChildCount"])
	}
}

// 未实现端点必须明确报错（Subsonic 协议 status="failed"），
// 不得返回 HTTP 200 + 伪成功/空响应掩盖未实现能力。
func TestUnimplementedEndpointReportsFailure(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if err := cfg.Set("stream_pass", "test"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(subsonicHandler(newStore(nil), cfg))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/rest/getAvatar?u=admin&p=test&v=1.16.1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	out := string(body[:n])
	if !strings.Contains(out, `status="failed"`) || !strings.Contains(out, "not supported") {
		t.Fatalf("getAvatar should report explicit failure, got: %s", out)
	}
}
