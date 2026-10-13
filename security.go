// 安全加固：全站安全响应头、请求体大小限制、CSRF 校验、登录限流。
// 依赖前端统一携带 X-Requested-With 头（web/src/api.js），双重防御跨站请求伪造。
package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// securityHeaders sets hardened HTTP response headers for every response.
// 注：frame-ancestors 放行同源（'self'）——第三方插件的 Web 面板以 iframe
// 嵌入宿主页面，必须允许同源框架嵌入；跨站嵌入仍被禁止。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' data: https:; "+
				"style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

// limitBody caps request bodies (e.g. batch IDs, tags, settings) to 8 MiB and
// rejects anything larger, protecting the JSON decoders from memory abuse.
// 插件导入允许大包（handler 内部已有 256MiB 上限），其余 API 保持 8MiB 防滥用。
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.URL.Path != "/api/plugins/import" {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		}
		next.ServeHTTP(w, r)
	})
}

const csrfHeader = "X-Requested-With"
const csrfValue = "LNH-MusicTag"

// csrfGuard rejects state-changing requests that do not carry the custom
// header, closing the remaining CSRF gap beyond SameSite=Lax cookies.
// /api/login is exempt (no session cookie involved, nothing to hijack).
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !strings.HasPrefix(r.URL.Path, "/api/login") {
				if r.Header.Get(csrfHeader) != csrfValue {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: missing CSRF header"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- 登录限流（防暴力破解）----

type loginGuard struct {
	mu     sync.Mutex
	fails  map[string]int       // key -> 连续失败次数
	locked map[string]time.Time // key -> 锁定时长
}

func newLoginGuard() *loginGuard {
	return &loginGuard{fails: map[string]int{}, locked: map[string]time.Time{}}
}

// clientKey derives a stable identifier from the remote address.
func clientKey(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	return addr
}

const (
	maxLoginFails    = 5
	loginLockMinutes = 5
)

// allow reports whether attempts from this client may proceed.
func (g *loginGuard) allow(r *http.Request) bool {
	k := clientKey(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.locked[k]; ok {
		if time.Now().Before(until) {
			return false
		}
		delete(g.locked, k)
		g.fails[k] = 0
	}
	return true
}

// fail records a failed login; returns the remaining lock duration (0 = none).
func (g *loginGuard) fail(r *http.Request) time.Duration {
	k := clientKey(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails[k]++
	if g.fails[k] >= maxLoginFails {
		until := time.Now().Add(loginLockMinutes * time.Minute)
		g.locked[k] = until
		g.fails[k] = 0
		return loginLockMinutes * time.Minute
	}
	return 0
}

// success clears the failure counters for this client.
func (g *loginGuard) success(r *http.Request) {
	k := clientKey(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, k)
	delete(g.locked, k)
}
