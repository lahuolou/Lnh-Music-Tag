package main

import (
	"os"
	"path/filepath"
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

// TestEnsureHashesProgressAndCancel 验证一键刮削去重阶段的哈希：
// ① 正常路径按文件计数并回写 SHA256（不再 0/947 假死）；
// ② cancel 回调置位后立即中断，不再同步阻塞整条流水线。
func TestEnsureHashesProgressAndCancel(t *testing.T) {
	dir := t.TempDir()
	s := newStore(nil)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, "t"+string(rune('a'+i))+".bin")
		if err := os.WriteFile(p, []byte("content-"+string(rune('a'+i))), 0644); err != nil {
			t.Fatal(err)
		}
		s.add(&model.Track{ID: "id" + string(rune('a'+i)), Path: p, FileName: filepath.Base(p)})
	}

	// 正常路径：3 个文件全部哈希，进度回调 3 次，SHA256 写回
	done := 0
	last := ""
	n := s.ensureHashes(s.all(), func(cur string, d int) {
		done++
		last = cur
	}, nil)
	if n != 3 || done != 3 {
		t.Fatalf("want hashed=3/progress=3, got hashed=%d progress=%d", n, done)
	}
	if last == "" {
		t.Fatal("progress callback must report a file name")
	}
	for _, tr := range s.all() {
		if tr.SHA256 == "" {
			t.Fatalf("track %s should have SHA256 after ensureHashes", tr.ID)
		}
	}

	// 取消路径：cancel 立即为 true → 提前中断，返回 0，不阻塞
	s2 := newStore(nil)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, "x"+string(rune('a'+i))+".bin")
		if err := os.WriteFile(p, []byte("other-"+string(rune('a'+i))), 0644); err != nil {
			t.Fatal(err)
		}
		s2.add(&model.Track{ID: "x" + string(rune('a'+i)), Path: p, FileName: filepath.Base(p)})
	}
	cancelled := 0
	n2 := s2.ensureHashes(s2.all(), nil, func() bool { return true })
	if n2 != 0 {
		t.Fatalf("cancel immediately: want hashed=0, got %d", n2)
	}
	_ = cancelled
}
