package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cstore "lnh-music-tag/internal/store"
)

// ---- 登录/网关认证限速（防暴力破解，阶段 A）----

func TestLoginGuardLockout(t *testing.T) {
	g := newLoginGuard()
	req := func() *http.Request { return httptest.NewRequest(http.MethodPost, "/api/login", nil) }
	// 前 maxLoginFails-1 次失败不锁定，第 maxLoginFails 次锁定
	for i := 0; i < maxLoginFails; i++ {
		g.fail(req())
	}
	if g.allow(req()) {
		t.Fatal("超过失败阈值后应锁定")
	}
	// success 清零后可重新尝试
	g.success(req())
	if !g.allow(req()) {
		t.Fatal("success 后应解除锁定")
	}
}

func TestGatewayAuthenticateRateLimit(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	t.Cleanup(func() { cfg.Close() })
	_ = cfg.Set("stream_pass", "secret")
	g := &mediaGateway{
		library: newStore(nil), cfg: cfg, kind: "jellyfin",
		tokens: map[string]time.Time{}, plays: map[string]playState{},
		guard: newLoginGuard(),
	}
	for i := 0; i < maxLoginFails; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", nil)
		req.SetBasicAuth("admin", "wrong")
		g.authenticate(rec, req, "jellyfin")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401, got %d", i+1, rec.Code)
		}
	}
	// 超过阈值后即使密码正确也被限速
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", nil)
	req.SetBasicAuth("admin", "secret")
	g.authenticate(rec, req, "jellyfin")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("限速后应 429, got %d", rec.Code)
	}
}

func TestNavidromeLoginRateLimit(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	t.Cleanup(func() { cfg.Close() })
	_ = cfg.Set("stream_pass", "secret")
	n := &navidromeAPI{library: newStore(nil), cfg: cfg, guard: newLoginGuard()}
	for i := 0; i < maxLoginFails; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/login",
			strings.NewReader(`{"username":"admin","password":"wrong"}`))
		n.login(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401, got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"username":"admin","password":"secret"}`))
	n.login(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("限速后应 429, got %d", rec.Code)
	}
}

// ---- 健康端点（阶段 A）----

func TestHealthEndpoints(t *testing.T) {
	// /health：进程响应即 200
	rec := httptest.NewRecorder()
	healthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("/health got %d %q", rec.Code, rec.Body.String())
	}
	// /ready：nil 配置（DB 不可用）→ 503
	rec = httptest.NewRecorder()
	readyHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready(nil cfg) got %d, want 503", rec.Code)
	}
	// /ready：真实配置（DB 可读）→ 200
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	t.Cleanup(func() { cfg.Close() })
	rec = httptest.NewRecorder()
	readyHandler(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ready"}` {
		t.Fatalf("/ready got %d %q", rec.Code, rec.Body.String())
	}
}
