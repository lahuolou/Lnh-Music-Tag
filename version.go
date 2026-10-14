// 新版本检查：启动后异步查询最新版本号，与内置版本号比较，
// 通过 /api/version（缓存快照）与 /api/version/check（强制实时重查）暴露给前端。
//
// 查询顺序（任一成功即返回）：
//  1. GitHub Releases API（最实时、含跳转链接；大陆网络可能不通）
//  2. jsDelivr CDN 读取仓库根 VERSION 文件（大陆可访问性好，CDN 缓存最长 12h）
//  3. raw.githubusercontent.com 读取 VERSION 文件（备用）
//
// 三态结果：ok（成功取得最新版本）/ fail（全部源都失败）/ unknown（尚未检查）。
// 查询失败不再静默降级为“已是最新”，前端据此区分提示。
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	verStatusOK      = "ok"
	verStatusFail    = "fail"
	verStatusUnknown = "unknown"
	verCacheTTL      = 30 * time.Minute
	githubAPI        = "https://api.github.com/repos/%s/releases/latest"
	rawVersionURL    = "https://raw.githubusercontent.com/%s/main/VERSION"
	releasePageURL   = "https://github.com/%s/releases/latest"
)

// jsDelivr 多域名（任一可用即走 CDN，大陆可访问性好）。
var cdnHosts = []string{"cdn.jsdelivr.net", "fastly.jsdelivr.net", "gcore.jsdelivr.net"}

type updateChecker struct {
	mu      sync.Mutex
	latest  string // 已去掉 v 前缀的版本号
	url     string // 查看更新的跳转链接
	status  string // ok | fail | unknown
	checked time.Time
	repo    string
}

func newUpdateChecker(repo string) *updateChecker {
	return &updateChecker{repo: repo, status: verStatusUnknown}
}

// Start 启动后异步执行一次后台检查。
func (u *updateChecker) Start() { go u.check() }

// check 实时查询一次最新版本（多源按顺序尝试），并写入缓存。
func (u *updateChecker) check() {
	repo := u.repo
	// 源1：GitHub Releases API
	if latest, html, ok := fetchGitHubLatest(repo); ok {
		u.mu.Lock()
		u.latest, u.url, u.status, u.checked = latest, html, verStatusOK, time.Now()
		u.mu.Unlock()
		return
	}
	// 源2/3：VERSION 文件（jsDelivr CDN → raw）
	if latest, ok := fetchVersionFile(repo); ok {
		u.mu.Lock()
		u.latest, u.url, u.status, u.checked = latest, releasePage(repo), verStatusOK, time.Now()
		u.mu.Unlock()
		return
	}
	u.mu.Lock()
	u.status, u.checked = verStatusFail, time.Now()
	u.mu.Unlock()
}

// snapshot 返回当前缓存快照（不触发网络请求）。
func (u *updateChecker) snapshot() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.toMapLocked()
}

// forceCheck 强制实时重查（手动“检查更新”用），返回最新结果。
func (u *updateChecker) forceCheck() map[string]any {
	u.check()
	return u.snapshot()
}

func (u *updateChecker) toMapLocked() map[string]any {
	return map[string]any{
		"current": AppVersion(),
		"latest":  u.latest,
		"url":     u.url,
		"status":  u.status,
	}
}

// versionHandler GET /api/version：缓存快照（自动检查用）。
func (u *updateChecker) versionHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, u.snapshot())
}

// versionCheckHandler GET /api/version/check：强制实时重查（手动检查用）。
func (u *updateChecker) versionCheckHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, u.forceCheck())
}

// fetchGitHubLatest 从 GitHub Releases API 取最新 release tag。
func fetchGitHubLatest(repo string) (latest, html string, ok bool) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.Replace(githubAPI, "%s", repo, 1), nil)
	if err != nil {
		return "", "", false
	}
	req.Header.Set("User-Agent", "LNH-MusicTag/"+AppVersion())
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false // 无 release 或限流
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", false
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || rel.TagName == "" {
		return "", "", false
	}
	return strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v"), rel.HTMLURL, true
}

// fetchVersionFile 依次尝试 jsDelivr 多域名与 raw.githubusercontent 读取 VERSION 文件。
func fetchVersionFile(repo string) (latest string, ok bool) {
	urls := make([]string, 0, len(cdnHosts)+1)
	for _, h := range cdnHosts {
		urls = append(urls, "https://"+h+"/gh/"+repo+"@main/VERSION")
	}
	urls = append(urls, strings.Replace(rawVersionURL, "%s", repo, 1))
	for _, u := range urls {
		client := &http.Client{Timeout: 8 * time.Second}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "LNH-MusicTag/"+AppVersion())
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(string(body)), "v")
		if v == "" || !looksLikeVersion(v) {
			continue
		}
		return v, true
	}
	return "", false
}

func releasePage(repo string) string { return strings.Replace(releasePageURL, "%s", repo, 1) }

func looksLikeVersion(v string) bool {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' })
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// hasUpdate 用数值化逐段比较版本：latest 是否比 current 新（1.4.50 → 1.5.0 判新）。
func hasUpdate(latest, current string) bool {
	l, c := parseVersion(latest), parseVersion(current)
	for i := 0; i < len(l) || i < len(c); i++ {
		lv, cv := 0, 0
		if i < len(l) {
			lv = l[i]
		}
		if i < len(c) {
			cv = c[i]
		}
		if lv != cv {
			return lv > cv
		}
	}
	return false
}

func parseVersion(v string) []int {
	var out []int
	for _, p := range strings.FieldsFunc(strings.TrimSpace(v), func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == ' '
	}) {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
