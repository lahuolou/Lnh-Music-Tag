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
// CSP 策略：宿主页面保持严格 script-src 'self'；仅第三方插件面板路由
// （/api/plugins/{name}/panel[/...]）放宽为 'unsafe-inline'，兼容面板内联
// 脚本/事件（第三方插件兼容例外，详见开发文档「安全说明」）。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-XSS-Protection", "1; mode=block")
		scriptSrc := "script-src 'self'"
		if isPluginPanelPath(r.URL.Path) {
			scriptSrc = "script-src 'self' 'unsafe-inline'"
		}
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' data: https:; "+
				"style-src 'self' 'unsafe-inline'; "+
				scriptSrc+"; "+
				"connect-src 'self'; "+
				"frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

// isPluginPanelPath 报告请求路径是否为第三方插件面板（入口或面板子资源）。
func isPluginPanelPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/plugins/")
	if !ok {
		return false
	}
	seg := strings.SplitN(rest, "/", 3)
	return len(seg) >= 2 && seg[0] != "" && seg[1] == "panel"
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
					writeErr(w, http.StatusForbidden, "forbidden: missing CSRF header")
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
