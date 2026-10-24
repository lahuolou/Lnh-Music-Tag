package main

import (
	"os"
	"path/filepath"
	"testing"

	"lnh-music-tag/internal/model"
)

func TestAlbumDirName(t *testing.T) {
	cases := []struct{ album, want string }{
		{"勇气", "勇气"},
		{"", ""},
		{"  ", ""},
		{"Various", ""},
		{"Various Artists", ""},
		{"unknown album", ""},
		{"Unknown", ""},
		{"Untitled", ""},
		{"单曲", ""},
		{"未知专辑", ""},
		{"A/B: C*D", "A B  C D"}, // 路径分隔符与非法字符清洗为空格
		{"The Best Of", "The Best Of"},
	}
	for _, c := range cases {
		tr := &model.Track{Tags: map[string]string{"ALBUM": c.album}}
		if got := albumDirName(tr); got != c.want {
			t.Errorf("albumDirName(%q) = %q, want %q", c.album, got, c.want)
		}
	}
}

func TestOrganizeTrackMovesToAlbumDir(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "old", "梁静茹 - 勇气.flac")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 外挂 .lrc 应跟随移动
	lrc := filepath.Join(root, "old", "梁静茹 - 勇气.lrc")
	if err := os.WriteFile(lrc, []byte("[00:00]lyrics"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := newStore(nil)
	tr := &model.Track{ID: "t1", Path: src, FileName: "梁静茹 - 勇气.flac", Tags: map[string]string{"ALBUM": "勇气"}}
	st.add(tr)

	ok, err := organizeTrack(st, tr, root)
	if err != nil {
		t.Fatalf("organizeTrack err: %v", err)
	}
	if !ok {
		t.Fatal("expected move, got skip")
	}
	want := filepath.Join(root, "勇气", "梁静茹 - 勇气.flac")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("target not exists: %v", err)
	}
	if tr.Path != want {
		t.Errorf("track.Path = %q, want %q", tr.Path, want)
	}
	// .lrc 跟随
	if _, err := os.Stat(filepath.Join(root, "勇气", "梁静茹 - 勇气.lrc")); err != nil {
		t.Errorf(".lrc not moved: %v", err)
	}
	// 幂等：再次整理应跳过
	ok2, err2 := organizeTrack(st, tr, root)
	if err2 != nil || ok2 {
		t.Errorf("second organize: ok=%v err=%v, want skip", ok2, err2)
	}
}

func TestOrganizeTrackSkipsWhenRootMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "no-such-root")
	st := newStore(nil)
	tr := &model.Track{ID: "t2", Path: filepath.Join(root, "a.flac"), FileName: "a.flac", Tags: map[string]string{"ALBUM": "X"}}
	st.add(tr)
	ok, err := organizeTrack(st, tr, root)
	if err != nil || ok {
		t.Errorf("ok=%v err=%v, want skip", ok, err)
	}
}

func TestOrganizeTrackSkipsWhenTargetExists(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "old", "a.flac")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 目标已存在（同名不同源）：不覆盖
	target := filepath.Join(root, "专辑", "a.flac")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newStore(nil)
	tr := &model.Track{ID: "t3", Path: src, FileName: "a.flac", Tags: map[string]string{"ALBUM": "专辑"}}
	st.add(tr)
	ok, err := organizeTrack(st, tr, root)
	if err == nil || ok {
		t.Errorf("ok=%v err=%v, want conflict error", ok, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "existing" {
		t.Error("existing target was overwritten")
	}
	if _, err := os.Stat(src); err != nil {
		t.Error("source should remain on conflict")
	}
}
