// 静态资源缓存策略：
//   - index.html 及无哈希文件：no-cache（每次重新验证），确保浏览器/代理
//     不缓存旧版页面，从而加载最新构建的带哈希 JS/CSS，避免“旧界面”问题。
//   - 带内容哈希的 /assets/* 文件：一年强缓存 + immutable（文件名变即内容变）。
package main

import (
	"net/http"
	"strings"
)

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/assets/") && hashedAsset(p) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// hashedAsset 判断文件名是否带内容哈希（形如 index-XXXX.js / index-XXXX.css）。
func hashedAsset(p string) bool {
	base := p[strings.LastIndex(p, "/")+1:]
	if !strings.HasPrefix(base, "index-") {
		return false
	}
	dot := strings.LastIndex(base, ".")
	if dot < 0 {
		return false
	}
	mid := base[len("index-"):dot]
	return mid != "" && !strings.ContainsAny(mid, "/.")
}
