package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

// 直传流媒体的 HTTP Range 行为（http.ServeContent 标准语义）：
// 有效范围 → 206 + 正确 Content-Range；超范围 → 416；HEAD 返回元信息。
func TestStreamDirectRange(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")

	dir := t.TempDir()
	name := "range-test.flac"
	fp := filepath.Join(dir, name)
	payload := make([]byte, 1000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := os.WriteFile(fp, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: fp, FileName: name, Ext: ".flac", Size: int64(len(payload))})

	srv := httptest.NewServer(subsonicHandler(s, cfg))
	defer srv.Close()

	base := "/rest/stream.view?u=admin&p=test&id=t1"

	do := func(method, path string, rangeHeader string) (int, http.Header, []byte) {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}

	// 1) 无 Range：200 + 完整 1000 字节
	code, hdr, body := do("GET", base, "")
	if code != http.StatusOK || len(body) != 1000 {
		t.Fatalf("plain GET: code=%d len=%d", code, len(body))
	}
	if hdr.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges=%q", hdr.Get("Accept-Ranges"))
	}
	if hdr.Get("Content-Length") != "1000" {
		t.Fatalf("Content-Length=%q", hdr.Get("Content-Length"))
	}

	// 2) bytes=0-99 → 206 + Content-Range + 100 字节
	code, hdr, body = do("GET", base, "bytes=0-99")
	if code != http.StatusPartialContent {
		t.Fatalf("range 0-99: code=%d", code)
	}
	if cr := hdr.Get("Content-Range"); cr != "bytes 0-99/1000" {
		t.Fatalf("Content-Range=%q", cr)
	}
	if len(body) != 100 {
		t.Fatalf("range 0-99: len=%d", len(body))
	}
	for i, b := range body {
		if b != payload[i] {
			t.Fatalf("range 0-99: byte %d mismatch %d!=%d", i, b, payload[i])
		}
	}

	// 3) bytes=100- → 206 + 剩余 900 字节
	code, _, body = do("GET", base, "bytes=100-")
	if code != http.StatusPartialContent || len(body) != 900 {
		t.Fatalf("range 100-: code=%d len=%d", code, len(body))
	}

	// 4) bytes=5000-（超范围）→ 416 + Content-Range: bytes */1000
	code, hdr, body = do("GET", base, "bytes=5000-")
	if code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("range 5000-: code=%d", code)
	}
	if cr := hdr.Get("Content-Range"); cr != "bytes */1000" {
		t.Fatalf("416 Content-Range=%q", cr)
	}

	// 5) HEAD → 200 + Content-Length，无 body
	code, hdr, body = do("HEAD", base, "")
	if code != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD: code=%d len=%d", code, len(body))
	}
	if hdr.Get("Content-Length") != "1000" {
		t.Fatalf("HEAD Content-Length=%q", hdr.Get("Content-Length"))
	}
}

// 实体级缓存写时失效：star 后 getSong 必须立即反映 starred，不得返回陈旧缓存。
func TestEntityCacheInvalidatedOnStar(t *testing.T) {
	cfg, _ := cstore.OpenConfig(t.TempDir())
	defer cfg.Close()
	_ = cfg.Set("stream_pass", "test")

	dir := t.TempDir()
	name := "cache-star.flac"
	fp := filepath.Join(dir, name)
	if err := os.WriteFile(fp, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newStore(nil)
	s.add(&model.Track{ID: "t1", Path: fp, FileName: name, Ext: ".flac", Size: 64})

	srv := httptest.NewServer(subsonicHandler(s, cfg))
	defer srv.Close()

	getSong := func() string {
		resp, err := http.Get(srv.URL + "/rest/getSong?u=admin&p=test&id=t1&v=1.16.1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	star := func(method string) {
		resp, err := http.Post(srv.URL+"/rest/"+method+"?u=admin&p=test&id=t1&v=1.16.1", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: code=%d", method, resp.StatusCode)
		}
	}

	// 首次 getSong（缓存写入）：不应有 starred
	if r := getSong(); strings.Contains(r, "starred=") {
		t.Fatalf("initial getSong unexpectedly starred: %s", r)
	}
	// star 后（写时失效）：必须立即出现 starred
	star("star")
	if r := getSong(); !strings.Contains(r, "starred=") {
		t.Fatalf("getSong after star lacks starred (cache not invalidated): %s", r)
	}
	// unstar 清理（避免污染共享配置）
	star("unstar")
}
