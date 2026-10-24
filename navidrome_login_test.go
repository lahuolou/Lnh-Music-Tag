package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	cstore "lnh-music-tag/internal/store"
)

func TestNavidromeLoginRejectsOversizedBody(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if err := cfg.Set("stream_pass", "secret"); err != nil {
		t.Fatal(err)
	}
	n := NewNavidromeAPI(nil, cfg)
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":"x","password":"`+strings.Repeat("a", (1<<20)+1)+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	n.login(w, r)
	if w.Code != 413 {
		t.Fatalf("status=%d, want 413; body=%s", w.Code, w.Body.String())
	}
}

func TestNavidromeLoginRejectsMalformedJSON(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if err := cfg.Set("stream_pass", "secret"); err != nil {
		t.Fatal(err)
	}
	n := NewNavidromeAPI(nil, cfg)
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	n.login(w, r)
	if w.Code != 400 {
		t.Fatalf("status=%d, want 400; body=%s", w.Code, w.Body.String())
	}
}
