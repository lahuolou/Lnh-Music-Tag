package taglibx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeAudio generates a tiny real audio file with ffmpeg for tag round-trips.
// Tests requiring a real file skip when ffmpeg is unavailable.
func makeAudio(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg 不可用，跳过集成测试")
	}
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.3", "-c:a", "aac", "-b:a", "64k", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 生成测试文件失败: %v %s", err, out)
	}
}

func TestSaveTagsAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "sample.m4a")
	makeAudio(t, f)

	// 首次原子写：写标签
	if err := SaveTagsAtomic(f, map[string][]string{Title: {"勇气"}, Artist: {"梁静茹"}, Album: {"勇气"}}, false); err != nil {
		t.Fatalf("首次原子写失败: %v", err)
	}
	// 重读验证标签正确且文件可解析
	tr, err := ReadTrack(f)
	if err != nil {
		t.Fatalf("写入后读取失败: %v", err)
	}
	if v := tr.Tags[Title]; len(v) == 0 || string(v) != "勇气" {
		t.Errorf("标题未写入: %v", v)
	}
	if v := tr.Tags[Artist]; len(v) == 0 || string(v) != "梁静茹" {
		t.Errorf("艺术家未写入: %v", v)
	}
	// 文件仍在（未被破坏）
	after, err := os.Stat(f)
	if err != nil {
		t.Fatalf("原文件丢失: %v", err)
	}
	if after.Size() <= 0 {
		t.Errorf("原文件大小异常: %d", after.Size())
	}

	// 再次原子写（幂等）：追加字段
	if err := SaveTagsAtomic(f, map[string][]string{Title: {"勇气"}, Artist: {"梁静茹"}, Album: {"勇气"}, Comment: {"round-2"}}, false); err != nil {
		t.Fatalf("二次原子写失败: %v", err)
	}
	tr2, err := ReadTrack(f)
	if err != nil {
		t.Fatalf("二次写入后读取失败: %v", err)
	}
	if v := tr2.Tags[Comment]; len(v) == 0 || string(v) != "round-2" {
		t.Errorf("二次写入字段未生效: %v", v)
	}
	// 标题保持（未被破坏）
	if v := tr2.Tags[Title]; len(v) == 0 || string(v) != "勇气" {
		t.Errorf("二次写入破坏标题: %v", v)
	}

	// 无残留临时/备份文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lnh-tmp-") || strings.Contains(e.Name(), ".lnh-bak-") {
			t.Errorf("残留临时/备份文件: %s", e.Name())
		}
	}
}

func TestSaveTagsAtomicFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "sample.m4a")
	makeAudio(t, f)
	original, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// 目标路径为不存在的文件：复制阶段失败，原文件（不存在）不应产生任何新文件残留
	missing := filepath.Join(dir, "no-such.m4a")
	if err := SaveTagsAtomic(missing, map[string][]string{Title: {"x"}}, false); err == nil {
		t.Fatal("对不存在文件原子写应失败")
	}
	// 目录可写但文件被占用的场景（Windows）：目标为目录本身，复制/替换必然失败
	if err := SaveTagsAtomic(dir, map[string][]string{Title: {"x"}}, false); err == nil {
		t.Fatal("对目录原子写应失败")
	}
	// 原文件应保持不变
	after, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if len(after) != len(original) {
		t.Errorf("原文件被意外改变: %d -> %d", len(original), len(after))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lnh-tmp-") || strings.Contains(e.Name(), ".lnh-bak-") {
			t.Errorf("失败后残留临时/备份文件: %s", e.Name())
		}
	}
}
