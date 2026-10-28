package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

// testStore 构造一个内存 store + 配置库。
func testStore(t *testing.T) (*store, *cstore.Config) {
	t.Helper()
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	t.Cleanup(func() { cfg.Close() })
	s := newStore(cfg.DB())
	s.addMany([]*model.Track{
		{ID: "id1", Path: filepath.Join("A", "song1.mp3"), FileName: "song1.mp3", Tags: map[string]string{}},
		{ID: "id2", Path: filepath.Join("B", "song2.flac"), FileName: "song2.flac", Tags: map[string]string{}},
		{ID: "id3", Path: filepath.Join("C", "song3.m4a"), FileName: "song3.m4a", Tags: map[string]string{}},
	})
	return s, cfg
}

func writeM3U(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write m3u: %v", err)
	}
}

func loadStoredPlaylists(t *testing.T, cfg *cstore.Config) []subsonicPlaylist {
	t.Helper()
	var ps []subsonicPlaylist
	if err := json.Unmarshal([]byte(cfg.GetDefault("streaming_playlists", "[]")), &ps); err != nil {
		t.Fatalf("unmarshal streaming_playlists: %v", err)
	}
	return ps
}

// TestImportM3UPlaylists：相对路径/去点相对路径/URL 跳过/缺失文件跳过。
func TestImportM3UPlaylists(t *testing.T) {
	root := t.TempDir()
	// 曲目真实存在于 root 下（路径与 store 登记一致）
	if err := os.MkdirAll(filepath.Join(root, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "B"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "A", "song1.mp3"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "B", "song2.flac"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "C", "song3.m4a"), []byte("x"), 0o644)

	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	s := newStore(cfg.DB())
	s.addMany([]*model.Track{
		{ID: "id1", Path: filepath.Join(root, "A", "song1.mp3"), FileName: "song1.mp3", Tags: map[string]string{}},
		{ID: "id2", Path: filepath.Join(root, "B", "song2.flac"), FileName: "song2.flac", Tags: map[string]string{}},
		{ID: "id3", Path: filepath.Join(root, "C", "song3.m4a"), FileName: "song3.m4a", Tags: map[string]string{}},
	})

	// 歌单放在子目录 mylists 下：相对条目相对歌单文件目录解析
	writeM3U(t, filepath.Join(root, "mylists", "favorites.m3u"),
		"#EXTM3U\n#EXTINF:240,Artist - Song1\nA/song1.mp3\n"+
			"#EXTINF:250,Artist2 - Song2\n./../B/song2.flac\n"+
			"../C/song3.m4a\n"+
			"https://example.com/stream.mp3\n"+
			"D/nothere.mp3\n")

	n := importM3UPlaylists(s, cfg, root)
	if n != 1 {
		t.Fatalf("应导入 1 个歌单，得到 %d", n)
	}
	ps := loadStoredPlaylists(t, cfg)
	if len(ps) != 1 {
		t.Fatalf("存储歌单数应为 1，得到 %d", len(ps))
	}
	p := ps[0]
	if p.Name != "favorites" {
		t.Errorf("歌单名应为 favorites，得到 %q", p.Name)
	}
	if !strings.HasPrefix(p.ID, m3uPlaylistPrefix) {
		t.Errorf("歌单 ID 应以 m3u- 开头，得到 %q", p.ID)
	}
	want := []string{"id1", "id2", "id3"}
	if len(p.TrackIDs) != 3 {
		t.Fatalf("曲目数应为 3，得到 %d: %v", len(p.TrackIDs), p.TrackIDs)
	}
	for i, w := range want {
		if p.TrackIDs[i] != w {
			t.Errorf("曲目顺序 %d 应为 %s，得到 %s", i, w, p.TrackIDs[i])
		}
	}
}

// TestImportM3UPlaylistsIdempotent：重复扫描不重复，手工歌单保留，
// 删除 .m3u 后 m3u 歌单消失。
func TestImportM3UPlaylistsIdempotent(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "song1.mp3"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "song2.flac"), []byte("x"), 0o644)
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	s := newStore(cfg.DB())
	s.addMany([]*model.Track{
		{ID: "id1", Path: filepath.Join(root, "song1.mp3"), FileName: "song1.mp3", Tags: map[string]string{}},
		{ID: "id2", Path: filepath.Join(root, "song2.flac"), FileName: "song2.flac", Tags: map[string]string{}},
	})
	// 预置一个手工创建的网关歌单（pl- 前缀）
	if err := cfg.Set("streaming_playlists", `[{"id":"pl-manual","name":"手工","created":"t","changed":"t","trackIds":["id1"]}]`); err != nil {
		t.Fatal(err)
	}
	writeM3U(t, filepath.Join(root, "mix.m3u"), "song1.mp3\nsong2.flac\n")

	if n := importM3UPlaylists(s, cfg, root); n != 1 {
		t.Fatalf("第一次应导入 1 个歌单，得到 %d", n)
	}
	if n := importM3UPlaylists(s, cfg, root); n != 1 {
		t.Fatalf("第二次仍应导入 1 个歌单（幂等），得到 %d", n)
	}
	ps := loadStoredPlaylists(t, cfg)
	if len(ps) != 2 {
		t.Fatalf("应保留手工歌单 + 1 个 m3u 歌单 = 2，得到 %d", len(ps))
	}
	foundManual, foundM3U := false, false
	for _, p := range ps {
		if p.ID == "pl-manual" {
			foundManual = true
			if len(p.TrackIDs) != 1 {
				t.Errorf("手工歌单曲目被改动: %v", p.TrackIDs)
			}
		}
		if p.ID == stableID(m3uPlaylistPrefix, "mix") {
			foundM3U = true
			if len(p.TrackIDs) != 2 {
				t.Errorf("m3u 歌单曲目应为 2，得到 %v", p.TrackIDs)
			}
		}
	}
	if !foundManual || !foundM3U {
		t.Fatalf("手工/m3u 歌单缺失: manual=%v m3u=%v", foundManual, foundM3U)
	}
	// 删除 .m3u 后重建：m3u 歌单消失，手工歌单保留
	os.Remove(filepath.Join(root, "mix.m3u"))
	if n := importM3UPlaylists(s, cfg, root); n != 0 {
		t.Fatalf("无 m3u 文件时应导入 0，得到 %d", n)
	}
	ps = loadStoredPlaylists(t, cfg)
	if len(ps) != 1 || ps[0].ID != "pl-manual" {
		t.Fatalf("删除 m3u 后应只剩手工歌单，得到 %d 个: %v", len(ps), ps)
	}
}

// TestM3UTrackIDsCaseInsensitive：m3u 内路径大小写与磁盘不一致仍能匹配。
func TestM3UTrackIDsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Song-One.mp3"), []byte("x"), 0o644)
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	s := newStore(cfg.DB())
	s.addMany([]*model.Track{
		{ID: "id1", Path: filepath.Join(root, "Song-One.mp3"), FileName: "Song-One.mp3", Tags: map[string]string{}},
	})
	writeM3U(t, filepath.Join(root, "case.m3u"), "song-one.mp3\nSONG-ONE.MP3\n")

	n := importM3UPlaylists(s, cfg, root)
	if n != 1 {
		t.Fatalf("应导入 1 个歌单，得到 %d", n)
	}
	p := loadStoredPlaylists(t, cfg)[0]
	if len(p.TrackIDs) != 1 || p.TrackIDs[0] != "id1" {
		t.Fatalf("大小写不敏感匹配应得到 1 个去重曲目 id1，得到 %v", p.TrackIDs)
	}
}
