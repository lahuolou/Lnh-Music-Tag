package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cstore "lnh-music-tag/internal/store"
)

func TestAdminAPIRouteRequiresSession(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	sessions := newSessionStore(cfg)
	called := false
	h := sessions.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/plugins/import", nil))
	if w.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthenticated admin request: status=%d called=%v", w.Code, called)
	}
}

func TestCSRFGuardRequiresHeaderForMutation(t *testing.T) {
	called := false
	h := csrfGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/trash/clear", nil))
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("missing CSRF header: status=%d called=%v", w.Code, called)
	}
	called = false
	r := httptest.NewRequest(http.MethodPost, "/api/trash/clear", nil)
	r.Header.Set(csrfHeader, csrfValue)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || !called {
		t.Fatalf("valid CSRF header: status=%d called=%v", w.Code, called)
	}
}
