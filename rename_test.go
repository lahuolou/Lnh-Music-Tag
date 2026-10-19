package main

import (
	"os"
	"path/filepath"
	"testing"

	"lnh-music-tag/internal/model"
)

func TestRenameFileToArtistTitle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Unknown - 蓝色蝴蝶.flac")
	if err := os.WriteFile(src, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStore(nil)
	tr := &model.Track{ID: "x", Path: src, FileName: "Unknown - 蓝色蝴蝶.flac", Ext: ".flac"}
	s.add(tr)

	np := renameFileToArtistTitle(s, tr, "贾格JuggShots", "蓝色蝴蝶")
	if np == "" {
		t.Fatalf("期望重命名成功，得到空")
	}
	if filepath.Base(np) != "贾格JuggShots - 蓝色蝴蝶.flac" {
		t.Fatalf("文件名错误: %s", np)
	}
	if _, err := os.Stat(np); err != nil {
		t.Fatalf("新文件不存在: %v", err)
	}

	// 已匹配时不重命名
	tr2 := &model.Track{ID: "y", Path: np, FileName: "贾格JuggShots - 蓝色蝴蝶.flac", Ext: ".flac"}
	s.add(tr2)
	if np2 := renameFileToArtistTitle(s, tr2, "贾格JuggShots", "蓝色蝴蝶"); np2 != "" {
		t.Fatalf("已匹配不应重命名: %s", np2)
	}
	// Unknown 艺术家不重命名
	tr3 := &model.Track{ID: "z", Path: filepath.Join(dir, "Unknown - 蓝色蝴蝶2.flac"), FileName: "Unknown - 蓝色蝴蝶2.flac", Ext: ".flac"}
	os.WriteFile(tr3.Path, []byte("x"), 0o644)
	s.add(tr3)
	if np3 := renameFileToArtistTitle(s, tr3, "Unknown", "蓝色蝴蝶2"); np3 != "" {
		t.Fatalf("Unknown 不应重命名: %s", np3)
	}
}
