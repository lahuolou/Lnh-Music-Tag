package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

// newSubsonicTestServer 构造带 3 首曲目的 Subsonic 测试服务器。
func newSubsonicTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg, _ := cstore.OpenConfig(t.TempDir())
	t.Cleanup(func() { cfg.Close() })
	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: "/m/zz.flac", FileName: "zz.flac", Tags: map[string]string{"TITLE": "Bravo", "GENRE": "Rock", "YEAR": "2010"}})
	s.add(&model.Track{ID: "t2", Path: "/m/aa.flac", FileName: "aa.flac", Tags: map[string]string{"TITLE": "Alpha", "GENRE": "Jazz", "YEAR": "2005"}})
	s.add(&model.Track{ID: "t3", Path: "/m/mm.flac", FileName: "mm.flac", Tags: map[string]string{"TITLE": "Mike", "GENRE": "Rock", "YEAR": "2020"}})
	_ = cfg.Set("stream_pass", "test")
	srv := httptest.NewServer((&subsonicAPI{library: s, cfg: cfg}).handler())
	t.Cleanup(srv.Close)
	return srv
}

func getSongsTitles(t *testing.T, srv *httptest.Server, query string) ([]string, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/rest/getSongs.view?u=admin&p=test&f=json&" + query)
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
		if e, ok := r["error"].(map[string]any); ok {
			return nil, e["code"].(string) + ":" + e["message"].(string)
		}
		return nil, r["status"].(string)
	}
	songs := r["songs"].(map[string]any)["song"].([]any)
	out := make([]string, 0, len(songs))
	for _, s := range songs {
		out = append(out, s.(map[string]any)["title"].(string))
	}
	return out, "ok"
}

// TestSubsonicGetSongs：NASMusicTV 实际请求（type=alphabeticalByName + size/offset）。
func TestSubsonicGetSongs(t *testing.T) {
	srv := newSubsonicTestServer(t)

	titles, st := getSongsTitles(t, srv, "type=alphabeticalByName&size=2&offset=0")
	if st != "ok" {
		t.Fatalf("getSongs status=%s", st)
	}
	if len(titles) != 2 || titles[0] != "Alpha" || titles[1] != "Bravo" {
		t.Fatalf("分页 size=2 应按标题取 [Alpha Bravo]，得到 %v", titles)
	}

	titles, _ = getSongsTitles(t, srv, "type=alphabeticalByName&size=2&offset=1")
	if len(titles) != 2 || titles[0] != "Bravo" || titles[1] != "Mike" {
		t.Fatalf("offset=1 应取 [Bravo Mike]，得到 %v", titles)
	}

	titles, _ = getSongsTitles(t, srv, "type=alphabetical")
	if len(titles) != 3 {
		t.Fatalf("无分页应返回全部 3 首，得到 %d", len(titles))
	}
}

// TestSubsonicGetSongsTypes：规范 type 分支（byNewest 逆序 / byGenre / byYear）。
func TestSubsonicGetSongsTypes(t *testing.T) {
	srv := newSubsonicTestServer(t)

	// byNewest：插入序 t1,t2,t3 逆序 → Mike, Alpha, Bravo
	titles, st := getSongsTitles(t, srv, "type=byNewest")
	if st != "ok" {
		t.Fatalf("byNewest status=%s", st)
	}
	if len(titles) != 3 || titles[0] != "Mike" || titles[2] != "Bravo" {
		t.Fatalf("byNewest 应按添加逆序 [Mike Alpha Bravo]，得到 %v", titles)
	}

	// byGenre：Rock → Bravo, Mike（按标题序？实现未排序——只过滤，保持扫描序 t1,t3）
	titles, _ = getSongsTitles(t, srv, "type=byGenre&genre=Rock")
	if len(titles) != 2 {
		t.Fatalf("byGenre Rock 应有 2 首，得到 %v", titles)
	}

	// byYear：2010..2020 → Bravo(2010), Mike(2020)，按年份降序
	titles, _ = getSongsTitles(t, srv, "type=byYear&fromYear=2010&toYear=2020")
	if len(titles) != 2 || titles[0] != "Mike" {
		t.Fatalf("byYear 2010-2020 应为 [Mike Bravo]，得到 %v", titles)
	}

	// 缺 type → 参数错误
	if _, st := getSongsTitles(t, srv, "size=5"); st != "10:Missing parameter: type" {
		t.Fatalf("缺 type 应报 code 10，得到 %q", st)
	}
}
