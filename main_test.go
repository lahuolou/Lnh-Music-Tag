package main

import (
	"testing"

	"lnh-musictag/internal/model"
)

// TestStoreScanOrder 验证 /api/tracks 按扫描（插入）顺序稳定返回：
// 先扫描到的排前面，后扫描的递增在后面，不因 map 遍历随机而乱序。
func TestStoreScanOrder(t *testing.T) {
	s := newStore(nil)
	paths := []string{"/m/c.flac", "/m/a.mp3", "/m/b.wav"}
	for i, p := range paths {
		s.add(&model.Track{ID: string(rune('a' + i)), Path: p, FileName: p})
	}
	got := s.all()
	if len(got) != 3 {
		t.Fatalf("want 3 tracks, got %d", len(got))
	}
	want := []string{"/m/c.flac", "/m/a.mp3", "/m/b.wav"}
	for i := range want {
		if got[i].Path != want[i] {
			t.Fatalf("track[%d] = %s, want %s（必须保持扫描顺序）", i, got[i].Path, want[i])
		}
	}
	// 多次读取顺序一致
	again := s.all()
	for i := range want {
		if again[i].Path != want[i] {
			t.Fatalf("second read track[%d] = %s, want %s（顺序必须稳定）", i, again[i].Path, want[i])
		}
	}
}
