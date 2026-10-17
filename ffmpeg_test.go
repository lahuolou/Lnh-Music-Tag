package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPersistFFmpegTo 验证一键安装后的持久化：二进制与依赖库复制到配置卷，
// wrapper 脚本生成并可读，插件 path 配置指向 wrapper。
func TestPersistFFmpegTo(t *testing.T) {
	// 注册 tool.ffmpeg 插件，验证持久化会写回插件 path 配置
	registerPlugin(&Plugin{Name: "tool.ffmpeg", Label: "FFmpeg", Kind: PluginTool, Enabled: true}, nil)
	dir := t.TempDir()
	// 伪造已安装的 ffmpeg 二进制与依赖库
	bin := filepath.Join(dir, "usr-bin", "ffmpeg")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fake-ffmpeg-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	lib1 := filepath.Join(dir, "usr-lib", "libavcodec.so.61")
	lib2 := filepath.Join(dir, "usr-lib", "libavutil.so.59")
	for _, l := range []string{lib1, lib2} {
		if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l, []byte("lib"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(dir, "config", "ffmpeg")
	if err := persistFFmpegTo(bin, []string{lib1, lib2}, dst, nil); err != nil {
		t.Fatalf("persistFFmpegTo 失败: %v", err)
	}
	// 二进制复制成功且保留执行位
	saved := filepath.Join(dst, "bin", "ffmpeg")
	data, err := os.ReadFile(saved)
	if err != nil || string(data) != "fake-ffmpeg-bin" {
		t.Fatalf("二进制未正确复制: %v %q", err, data)
	}
	if st, _ := os.Stat(saved); runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		t.Fatal("持久化二进制缺少执行位")
	}
	// 依赖库复制成功
	for _, l := range []string{"libavcodec.so.61", "libavutil.so.59"} {
		if _, err := os.Stat(filepath.Join(dst, "lib", l)); err != nil {
			t.Fatalf("依赖库 %s 未复制: %v", l, err)
		}
	}
	// wrapper 生成：shebang + LD_LIBRARY_PATH 指向 lib 目录 + exec 二进制
	w, err := os.ReadFile(filepath.Join(dst, "ffmpeg"))
	if err != nil {
		t.Fatalf("wrapper 未生成: %v", err)
	}
	wc := string(w)
	for _, want := range []string{"#!/bin/sh", "LD_LIBRARY_PATH=", filepath.Join(dst, "lib"), "exec", saved} {
		if !strings.Contains(wc, want) {
			t.Fatalf("wrapper 缺少 %q: %s", want, wc)
		}
	}
	if st, _ := os.Stat(filepath.Join(dst, "ffmpeg")); runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		t.Fatal("wrapper 缺少执行位")
	}
	// 插件 path 已指向 wrapper
	if got := getPluginConfig("tool.ffmpeg")["path"]; got != filepath.Join(dst, "ffmpeg") {
		t.Fatalf("插件 path 未配置为 wrapper: %q", got)
	}
}

// TestFFmpegBinPersistedFallback 模拟容器更新重建后的场景：
// 系统层 ffmpeg 已丢失（PATH 无 ffmpeg），仅配置卷保留持久化安装，
// ffmpegBin 应自动认领配置卷里的 wrapper。
func TestFFmpegBinPersistedFallback(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ffmpeg", "ffmpeg")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ffmpeg \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LNH_CONFIG_DIR", dir)
	// 精简 PATH，确保 exec.LookPath 找不到系统 ffmpeg
	t.Setenv("PATH", os.Getenv("SystemRoot")+`\System32`)
	if got := ffmpegBin(nil); got != wrapper {
		t.Fatalf("持久化认领失败: got %q, want %q", got, wrapper)
	}
}

// TestShDQ 验证 shell 双引号转义（wrapper 路径含特殊字符时安全）。
func TestShDQ(t *testing.T) {
	if got := shDQ(`a"b$c\d`); got != `a\"b\$c\d` {
		t.Fatalf("shDQ 转义错误: %q", got)
	}
}
