// 健康检查端点：/health 存活探针、/ready 就绪探针（供 Docker healthcheck 区分进程存活与服务可用）。
package main

import (
	"fmt"
	"net/http"

	cstore "lnh-music-tag/internal/store"
)

// healthHandler 存活探针：进程能响应即 200（不查依赖，避免健康检查误报导致容器误杀）。
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// readyHandler 就绪探针：SQLite 可读（SELECT 1）才算就绪；不泄露内部路径/密钥/错误细节。
func readyHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, code := "ready", http.StatusOK
		if cfg == nil || cfg.DB() == nil || cfg.DB().Ping() != nil {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
	}
}
