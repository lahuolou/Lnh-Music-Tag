package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/model"
)

// 转码失败必须回退直传（不得吐出空流/截断流导致“无法播放”）；
// download 标准语义是返回原始文件，即使客户端带 format=mp3 也不转码。
func TestDownloadOriginalAndTranscodeFallback(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")

	// 注册 ffmpeg 插件；仅当 PATH 探测到 ffmpeg 时启用并指向它，
	// 否则 ffmpegActive=false 直接走直传，两条路径都应返回原文件。
	registerPlugin(&Plugin{Name: "tool.ffmpeg", Label: "FFmpeg", Kind: PluginTool, Enabled: true, Config: map[string]string{}}, nil)
	if bin, err := exec.LookPath("ffmpeg"); err == nil {
		reg.plugins["tool.ffmpeg"].Config["path"] = bin
	}

	dir := t.TempDir()
	// 纯文本伪装成 flac：ffmpeg 无法解码，用于触发“转码失败→回退直传”。
	name := "G.E.M.邓紫棋-是否 (Live Piano Session).flac"
	fp := filepath.Join(dir, name)
	payload := []byte("NOT-A-REAL-AUDIO-" + strings.Repeat("x", 4096))
	if err := os.WriteFile(fp, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: fp, FileName: name, Ext: ".flac", Size: int64(len(payload)), Bitrate: 192, Duration: 120})

	srv := httptest.NewServer(subsonicHandler(s, cfg))
	defer srv.Close()

	get := func(path string) (int, http.Header, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}

	// stream 要求 mp3 转码但源无法解码：必须回退直传，返回原文件而非空流。
	code, _, body := get("/rest/stream.view?u=admin&p=test&id=t1&format=mp3&maxBitRate=192")
	if code != http.StatusOK {
		t.Fatalf("stream transcode-fallback code=%d", code)
	}
	if len(body) != len(payload) {
		t.Fatalf("stream fallback body=%d, want original %d (no empty/truncated stream)", len(body), len(payload))
	}

	// download 即使带 format=mp3 也直传原始文件，附件形式。
	code, hdr, body := get("/rest/download.view?u=admin&p=test&id=t1&format=mp3&maxBitRate=192")
	if code != http.StatusOK {
		t.Fatalf("download code=%d", code)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("download CD=%q, want attachment", cd)
	}
	if len(body) != len(payload) {
		t.Fatalf("download body=%d, want original %d", len(body), len(payload))
	}
}

// 真实无损 flac 请求 mp3 转码：无论 ffmpeg 能否编码，都必须返回 200 且非空，
// 绝不允许空流/截断（本机无 libmp3lame 时应回退直传 flac）。
func TestRealFlacTranscodeNeverEmpty(t *testing.T) {
	src := `E:\mp32\G.E.M.邓紫棋 - 是否 (Live Piano Session).flac`
	if _, err := os.Stat(src); err != nil {
		t.Skip("real library file not present on this machine")
	}
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")
	registerPlugin(&Plugin{Name: "tool.ffmpeg", Label: "FFmpeg", Kind: PluginTool, Enabled: true, Config: map[string]string{}}, nil)
	if bin, err := exec.LookPath("ffmpeg"); err == nil {
		reg.plugins["tool.ffmpeg"].Config["path"] = bin
	}
	s := newStore(nil)
	st, _ := os.Stat(src)
	s.add(&model.Track{ID: "real", Path: src, FileName: filepath.Base(src), Ext: ".flac", Size: st.Size(), Bitrate: 192, Duration: 240})

	for _, br := range []string{"192", "320"} {
		req := httptest.NewRequest(http.MethodGet, "/rest/stream?id=real", nil)
		rec := httptest.NewRecorder()
		streamTrack(rec, req, cfg, s.get("real"), "mp3", br, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("maxBitRate=%s code=%d", br, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("maxBitRate=%s returned EMPTY stream", br)
		}
	}
}

// maxBitRate（标准驼峰）优先，maxBitrate（小写）回退，二者都能被正确读取。
func TestBitRateParamCase(t *testing.T) {
	camel := url.Values{}
	camel.Set("maxBitRate", "192")
	if v := bitRateParam(camel); v != "192" {
		t.Fatalf("camelCase maxBitRate=%q, want 192", v)
	}
	lower := url.Values{}
	lower.Set("maxBitrate", "320")
	if v := bitRateParam(lower); v != "320" {
		t.Fatalf("lowercase maxBitrate=%q, want 320", v)
	}
	// 驼峰存在时优先于小写，避免被覆盖
	both := url.Values{}
	both.Set("maxBitRate", "192")
	both.Set("maxBitrate", "320")
	if v := bitRateParam(both); v != "192" {
		t.Fatalf("both=%q, want camel 192", v)
	}
}
