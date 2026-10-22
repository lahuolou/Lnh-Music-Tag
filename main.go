// Lnh-Music-Tag: a self-hosted web app for audio tag editing, library dedup,
// metadata scraping (domestic + international sources) and lyrics. Built with
// go.senan.xyz/taglib (WASM TagLib, no CGo) + pure-Go SHA256 dedup. Config
// (admin, API keys, options) is stored in SQLite, not environment variables.
package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lnh-music-tag/internal/dedup"
	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

// AppVersion 返回当前版本号（单一版本源 = 仓库根 VERSION 文件；与 GitHub
// Release tag 比较，用于“新版本提示”）。发布新版本只改 VERSION 一处，
// 避免代码常量与文件两处手工同步导致“检查更新”检测不到新版。
//
//go:embed VERSION
var versionFile string

func AppVersion() string { return strings.TrimSpace(versionFile) }

//go:embed web/dist
var webFS embed.FS

var installMu sync.Mutex // ffmpeg 安装串行锁

type store struct {
	mu     sync.RWMutex
	tracks map[string]*model.Track
	byPath map[string]string // path -> id
	order  map[string]int64  // id -> 扫描（插入）序号，保证 /api/tracks 按扫描顺序稳定返回
	seq    atomic.Int64      // 原子序号生成器：新增/更新/DB 缺失回退统一走这里，不用时间戳
	rev    int64             // 数据变更计数：add/remove/update 递增（插件/缓存失效锚点）
	db     *sql.DB           // 曲目持久化（可能为 nil：配置库不可用时退化为纯内存）

	coverMu    sync.RWMutex
	coverCache map[string]coverEntry // id -> 封面字节缓存（mtime 变化自动失效）
}

type coverEntry struct {
	mod time.Time
	img []byte
}

func newStore(db *sql.DB) *store {
	return &store{tracks: map[string]*model.Track{}, byPath: map[string]string{}, order: map[string]int64{}, db: db, coverCache: map[string]coverEntry{}}
}

// add 登记一首曲目（单条路径：编辑/手动添加等低频写操作）。
func (s *store) add(t *model.Track) {
	s.addMany([]*model.Track{t})
}

// addMany 批量登记曲目（扫描等大库场景）：一次加锁写入内存，再批量落库
// （upsertTracksDB 事务分批）。与单条 add 语义完全一致：byPath 冲突跳过、
// 标记计算、原子 seq 递增、rev 变更计数；批量只优化了锁与事务次数。
func (s *store) addMany(ts []*model.Track) {
	if len(ts) == 0 {
		return
	}
	s.mu.Lock()
	var added []*model.Track
	for _, t := range ts {
		if _, exists := s.byPath[t.Path]; exists {
			continue
		}
		t.HasTrad = detectTraditional(t.Tags)
		t.Garbled = looksGarbledTags(t)
		t.NeedsIdentify = needsIdentify(t)
		s.tracks[t.ID] = t
		s.byPath[t.Path] = t.ID
		seq := s.seq.Add(1)
		s.order[t.ID] = seq
		s.rev++
		added = append(added, t)
	}
	db := s.db
	s.mu.Unlock()
	if len(added) == 0 {
		return
	}
	upsertTracksDB(db, s, added)
}

func (s *store) all() []*model.Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Track, 0, len(s.tracks))
	for _, t := range s.tracks {
		out = append(out, t)
	}
	// 按扫描顺序（插入序）稳定返回：先扫描到的在前，后扫描的递增在后
	sort.Slice(out, func(i, j int) bool { return s.order[out[i].ID] < s.order[out[j].ID] })
	return out
}

func (s *store) get(id string) *model.Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tracks[id]
}

func (s *store) byPathID(path string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byPath[path]
}

// ensureHashes computes and caches SHA256 for tracks that lack one (scanned
// with the light read). Hashing is I/O heavy, so it runs on a small worker
// pool and each file is hashed at most once per process. prog (if non-nil)
// reports progress (current file name + completed count) after every file;
// cancel (if non-nil) is checked per file so a one-key dedup stage can be
// cancelled while hashing — without this, a large/slow library appears stuck
// at 0 progress and ignores cancel. Each file has a timeout so one stuck
// file (e.g. on a slow network mount) cannot block the whole pool.
func (s *store) ensureHashes(tracks []*model.Track, prog func(cur string, done int), cancel func() bool) int {
	var jobs []*model.Track
	s.mu.RLock()
	for _, t := range tracks {
		if t.SHA256 == "" {
			jobs = append(jobs, t)
		}
	}
	s.mu.RUnlock()
	if len(jobs) == 0 {
		return 0
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 2 {
		workers = 2
	}
	const fileTimeout = 120 * time.Second // 单文件兜底：慢盘/网络盘防止一个文件卡死全部 worker
	ch := make(chan *model.Track)
	var wg sync.WaitGroup
	var done atomic.Int64
	// 批量落库：worker 只更新内存，全部完成后统一批量写库（事务分批），
	// 避免大库去重时每文件一次独立事务的 fsync 开销。
	var updMu sync.Mutex
	var updated []*model.Track
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				if cancel != nil && cancel() {
					continue // drain channel so the feeder can exit
				}
				h := hashFileTimeout(t.Path, fileTimeout)
				if h != "" {
					s.mu.Lock()
					if cur := s.tracks[t.ID]; cur != nil && cur.SHA256 == "" {
						cur.SHA256 = h
						updMu.Lock()
						updated = append(updated, cur)
						updMu.Unlock()
					}
					s.mu.Unlock()
				}
				n := done.Add(1)
				if prog != nil {
					prog(t.FileName, int(n))
				}
			}
		}()
	}
	for _, t := range jobs {
		if cancel != nil && cancel() {
			break
		}
		ch <- t
	}
	close(ch)
	wg.Wait()
	if db := s.db; db != nil && len(updated) > 0 {
		upsertTracksDB(db, s, updated)
	}
	return int(done.Load())
}

// hashFileTimeout 计算文件 SHA256，超过 timeout 则返回空串（跳过该文件，
// 不阻塞哈希池）。哈希 goroutine 超时后仍会完成并写入 buffered channel。
func hashFileTimeout(path string, timeout time.Duration) string {
	out := make(chan string, 1)
	go func() {
		h, err := taglibx.HashFile(path)
		if err != nil {
			out <- ""
			return
		}
		out <- h
	}()
	select {
	case h := <-out:
		return h
	case <-time.After(timeout):
		return ""
	}
}

func (s *store) exists(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byPath[path]
	return ok
}

func (s *store) remove(id string) {
	s.mu.Lock()
	if t, ok := s.tracks[id]; ok {
		delete(s.tracks, id)
		delete(s.byPath, t.Path)
		s.rev++
		s.mu.Unlock()
		deleteTrackDB(s.db, s, id, t.Path)
		return
	}
	s.mu.Unlock()
}

func (s *store) removeByPath(p string) {
	s.mu.Lock()
	if id, ok := s.byPath[p]; ok {
		delete(s.tracks, id)
		delete(s.byPath, p)
		s.rev++
		s.mu.Unlock()
		deleteTrackDB(s.db, s, id, p)
		return
	}
	s.mu.Unlock()
}

// scanState holds live progress of an asynchronous directory scan.
type scanState struct {
	mu      sync.Mutex
	Running bool   `json:"running"`
	Paused  bool   `json:"paused"`
	Dir     string `json:"dir"`
	Added   int    `json:"added"`
	Total   int    `json:"total"` // audio files discovered so far
	Done    int    `json:"done"`
	Skipped int    `json:"skipped"` // 已知文件，智能跳过
	Removed int    `json:"removed"` // 已从磁盘消失、从列表清理的文件数
	Error   string `json:"error"`
}

// scanStatus is a lock-free snapshot of a scan state, safe to marshal/copy.
type scanStatus struct {
	Running bool   `json:"running"`
	Paused  bool   `json:"paused"`
	Dir     string `json:"dir"`
	Added   int    `json:"added"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Skipped int    `json:"skipped"`
	Removed int    `json:"removed"`
	Error   string `json:"error"`
}

func (st *scanState) snapshot() scanStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	return scanStatus{
		Running: st.Running, Paused: st.Paused, Dir: st.Dir, Added: st.Added,
		Total: st.Total, Done: st.Done, Skipped: st.Skipped, Removed: st.Removed, Error: st.Error,
	}
}

type scanManager struct {
	mu    sync.Mutex
	state *scanState
}

func newScanManager() *scanManager { return &scanManager{} }

func (sm *scanManager) start(store *store, dir string, autoFix, autoRename bool) error {
	sm.mu.Lock()
	if sm.state != nil && sm.state.snapshot().Running {
		sm.mu.Unlock()
		return errors.New("扫描已在运行")
	}
	st := &scanState{Running: true, Dir: dir}
	sm.state = st
	sm.mu.Unlock()
	go func() {
		added, removed, err := scanDirLive(store, dir, st, autoFix, autoRename)
		st.mu.Lock()
		st.Running = false
		st.Added = added
		st.Removed = removed
		if err != nil {
			st.Error = err.Error()
		}
		st.mu.Unlock()
		// 写操作日志：扫描完成（新增/跳过/清理的失效文件数）
		addLog("act_scan", err == nil, added, 0, st.snapshot().Skipped, actDetail("removed", removed))
	}()
	return nil
}

func (sm *scanManager) status() scanStatus {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state == nil {
		return scanStatus{}
	}
	return sm.state.snapshot()
}

func (sm *scanManager) pause(p bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state != nil {
		sm.state.mu.Lock()
		sm.state.Paused = p
		sm.state.mu.Unlock()
	}
}

func main() {
	configDir := getenvDefault("LNH_CONFIG_DIR", "/config")

	// Database-backed config: admin + API keys live here, not in env vars.
	cfg, err := cstore.OpenConfig(configDir)
	if err != nil {
		log.Fatalf("open config db: %v", err)
	}
	defer cfg.Close()

	store := newStore(cfg.DB())
	// 曲目持久化：启动时从数据库恢复上次扫描结果，升级/重启不丢列表
	if err := ensureTracksTable(cfg.DB()); err != nil {
		log.Printf("ensure tracks table: %v", err)
	} else {
		loadTracksFromDB(store, cfg.DB())
	}
	// 操作日志表（扫描/刮削/转换等用户操作的记录，后台可查看与删除）
	activityDB = cfg.DB()
	if err := ensureActivityTable(cfg.DB()); err != nil {
		log.Printf("ensure activity table: %v", err)
	}
	ms := scrape.NewMultiSource()
	// 插件框架：注册内置插件（刮削源 + FFmpeg），再从设置库恢复启停/配置
	// （首次启动自动迁移旧的 sources_enabled / ffmpeg_path 键）
	registerBuiltinPlugins(ms, cfg)
	loadPluginsFromDB(cfg, configDir, ms)

	adminUser, adminPass, generated, err := bootstrapAdmin(cfg, os.Getenv("LNH_ADMIN_USER"), os.Getenv("LNH_ADMIN_PASS"))
	if err != nil {
		log.Fatalf("init admin: %v", err)
	}
	sessions := newSessionStore(cfg)
	jobs := newJobStore(cfg.DB())
	scans := newScanManager()
	updates := newUpdateChecker("lahuolou/Lnh-Music-Tag")
	updates.Start()

	mux := http.NewServeMux()
	// Subsonic/OpenSubsonic endpoints are intentionally outside the browser-session
	// middleware; each request authenticates using Subsonic credentials.
	mux.Handle("/rest/", subsonicHandler(store, cfg))
	mux.Handle("/rest", subsonicHandler(store, cfg))
	// Navidrome-style REST compatibility subset (auth/login + selected /api/*); more specific
	// patterns take priority over the /api/ prefix used by the browser UI below.
	mux.HandleFunc("POST /auth/login", navidromeHandler(store, cfg).ServeHTTP)
	mux.HandleFunc("POST /auth/token", navidromeHandler(store, cfg).ServeHTTP)
	mux.HandleFunc("GET /ping", navidromeHandler(store, cfg).ServeHTTP)
	for _, path := range []string{
		"/api/song", "/api/song/{id}", "/api/song/{id}/stream",
		"/api/album", "/api/album/{id}", "/api/album/{id}/songs", "/api/album/{id}/coverart",
		"/api/artist", "/api/artist/{id}", "/api/artist/{id}/albums", "/api/artist/{id}/coverart",
		"/api/artist/{id}/tracks", "/api/artist/{id}/image",
		"/api/playlist", "/api/playlist/{id}", "/api/playlist/{id}/songs", "/api/playlist/{id}/tracks",
		"/api/album/{id}/tracks",
		"/api/search", "/api/search/songs", "/api/search/albums", "/api/search/artists",
		"/api/starred", "/api/starred/{id}", "/api/genres", "/api/albumlist/{type}", "/api/songlist/{type}",
		"/api/library/scanstatus", "/api/library/statistics", "/api/statistics", "/api/nowplaying", "/api/lyrics/{songId}",
		"/api/stream/{id}", "/api/coverart/{id}",
		"/api/coverArt", "/api/coverArt/{id}",
		"/api/keepalive", "/api/keepalive/keepalive",
	} {
		mux.HandleFunc(path, navidromeHandler(store, cfg).ServeHTTP)
	}
	// Jellyfin and Emby use separate URL namespaces and JSON adapters.
	mux.Handle("/jellyfin/", mediaGatewayHandler(store, cfg, "jellyfin"))
	mux.Handle("/jellyfin", mediaGatewayHandler(store, cfg, "jellyfin"))
	mux.Handle("/emby/", mediaGatewayHandler(store, cfg, "emby"))
	mux.Handle("/emby", mediaGatewayHandler(store, cfg, "emby"))
	// Jellyfin clients normally use the server root as their base URL.
	jf := mediaGatewayHandler(store, cfg, "jellyfin")
	for _, path := range []string{"/System/", "/Users/", "/Items", "/Items/", "/Artists", "/Artists/", "/Albums", "/Albums/", "/Playlists", "/Playlists/", "/Audio/", "/Genres", "/Sessions/", "/Library/", "/Branding/"} {
		mux.Handle(path, jf)
	}

	mux.HandleFunc("POST /api/login", sessions.loginHandler)
	mux.HandleFunc("POST /api/logout", sessions.logoutHandler)
	// /api/me 同时服务 Web 后台会话与 Navidrome 客户端：ND Bearer 令牌优先，否则回退 Web 会话。
	ndAPI := NewNavidromeAPI(store, cfg)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if ndAPI.Valid(r) {
			ndAPI.WriteMe(w)
			return
		}
		sessions.meHandler(w, r)
	})
	// /api/user/{name}：Navidrome 客户端按用户名拉取资料，ND Bearer 令牌优先，否则回退 Web 会话。
	mux.HandleFunc("GET /api/user/{name}", func(w http.ResponseWriter, r *http.Request) {
		if ndAPI.Valid(r) {
			ndAPI.WriteMe(w)
			return
		}
		sessions.meHandler(w, r)
	})

	pm := http.NewServeMux()
	pm.HandleFunc("POST /api/change-password", sessions.changePasswordHandler)
	pm.HandleFunc("POST /api/scrape/batch", batchScrapeHandler(jobs, store, ms, cfg))
	pm.HandleFunc("POST /api/scrape/onekey", oneKeyScrapeHandler(jobs, store, ms, cfg))
	pm.HandleFunc("GET /api/scrape/jobs/{id}", jobProgressHandler(jobs))
	pm.HandleFunc("POST /api/jobs/{id}/cancel", cancelJobHandler(jobs)) // 取消批量/工具任务
	pm.HandleFunc("GET /api/jobs/{id}", jobProgressHandler(jobs))       // 通用后台任务进度（转换/识别/乱码/简繁）
	pm.HandleFunc("GET /api/jobs", activeJobsHandler(jobs))             // 进行中的任务列表（刷新/换设备后恢复进度）
	pm.HandleFunc("GET /api/scrape/sources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ms.Sources())
	})
	// 通用插件设置接口：列出/启停/配置全部插件（刮削源、FFmpeg、预留类型）
	pm.HandleFunc("GET /api/plugins", pluginsHandler(cfg))
	pm.HandleFunc("POST /api/plugins", pluginsUpdateHandler(cfg, ms))
	pm.HandleFunc("POST /api/plugins/import", pluginsImportHandler(cfg, configDir))
	pm.HandleFunc("DELETE /api/plugins/{name}", pluginsDeleteHandler(cfg))
	pm.HandleFunc("POST /api/plugins/{name}/run", pluginsRunHandler())
	// 回收站：列出 / 恢复 / 永久删除 / 清空（删除与去重先移入 trash/，此处提供人工管理）
	pm.HandleFunc("GET /api/trash", trashListHandler(cfg))
	pm.HandleFunc("POST /api/trash/restore", trashRestoreHandler(cfg))
	pm.HandleFunc("POST /api/trash/delete", trashDeleteHandler(cfg))
	pm.HandleFunc("POST /api/trash/clear", trashClearHandler(cfg))
	pm.HandleFunc("GET /api/plugins/{name}/panel", pluginPanelHandler())
	pm.HandleFunc("GET /api/plugins/{name}/panel/{path...}", panelAssetHandler(cfg, configDir))
	pm.HandleFunc("POST /api/fs/scan-dir", fsScanDirHandler()) // 第三方插件面板通用目录扫描
	pm.HandleFunc("POST /api/convert", convertHandler(jobs, store, cfg))
	pm.HandleFunc("GET /api/ffmpeg/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ffmpegStatus(cfg))
	})
	pm.HandleFunc("POST /api/ffmpeg/install", func(w http.ResponseWriter, r *http.Request) {
		// 串行安装锁：同一时间只允许一次安装
		installMu.Lock()
		defer installMu.Unlock()
		out, err := installFFmpeg()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "安装失败: " + err.Error(), "output": out})
			return
		}
		// 安装成功后把二进制与动态库复制到配置卷并配置 path，
		// 容器更新重建后无需重新安装（失败不影响本次安装成功）。
		if perr := persistFFmpeg(cfg); perr != nil {
			addLog("act_plugin", false, 1, 0, 0, "FFmpeg 持久化失败: "+perr.Error())
		}
		bin := ffmpegBin(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out, "path": bin, "version": ffmpegVersion(bin)})
	})
	pm.HandleFunc("POST /api/ffmpeg/uninstall", func(w http.ResponseWriter, r *http.Request) {
		installMu.Lock()
		defer installMu.Unlock()
		uninstallFFmpeg(cfg)
		addLog("act_plugin", true, 1, 0, 0, "停用 FFmpeg（卸载）")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	pm.HandleFunc("POST /api/fix-encoding", fixEncodingHandler(jobs, store))
	pm.HandleFunc("POST /api/convert-script", convertScriptHandler(jobs, store))
	pm.HandleFunc("POST /api/rename-clean", renameCleanHandler(jobs, store))
	pm.HandleFunc("POST /api/identify", identifyHandler(jobs, store, ms, cfg))
	pm.HandleFunc("GET /api/version/check", updates.versionCheckHandler)

	// 操作日志：列表 / 勾选删除 / 清空
	pm.HandleFunc("GET /api/logs", logsHandler(cfg.DB()))
	pm.HandleFunc("POST /api/logs/delete", logsDeleteHandler(cfg.DB()))
	pm.HandleFunc("POST /api/logs/clear", logsClearHandler(cfg.DB()))
	mux.Handle("/api/", sessions.requireAuth(csrfGuard(limitBody(pm))))

	// /api/version 免认证：供前台前端/外部工具探测服务器版本，返回 200
	// 与后台前端版本显示所需字段（current/latest/status）。
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		v := AppVersion()
		writeJSON(w, http.StatusOK, map[string]any{
			"version":    v,
			"appName":    "Lnh-Music-Tag",
			"appVersion": v,
			"current":    v,
			"latest":     "",
			"status":     "ok",
		})
	})

	// Static UI (Vue SPA built into web/dist; /api/... routes win over this)
	dist, _ := fs.Sub(webFS, "web/dist")
	mux.Handle("/", cacheControl(http.FileServer(http.FS(dist))))

	// --- Scan (asynchronous, progressive) ---
	pm.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Dir string `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dir := strings.TrimSpace(req.Dir)
		if dir == "" {
			dir = cfg.GetDefault("scan_dir", "/music")
		}
		dir = filepath.Clean(dir) // 折叠 ../，防止路径遍历混淆；仍须为真实存在的目录
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			writeErr(w, http.StatusBadRequest, "目录不可访问: "+dir)
			return
		}
		autoFix := cfg.GetDefault("auto_fix_title", "1") == "1"
		autoRename := cfg.GetDefault("auto_rename_file", "1") == "1"
		if err := scans.start(store, dir, autoFix, autoRename); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": "扫描已开始", "dir": dir})
	})
	pm.HandleFunc("GET /api/scan/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, scans.status())
	})
	// 暂停 / 继续扫描
	pm.HandleFunc("POST /api/scan/pause", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paused bool `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		scans.pause(req.Paused)
		writeJSON(w, http.StatusOK, scans.status())
	})

	// --- Config / settings (stored in DB) ---
	pm.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"adminUser":      cfg.GetDefault("admin_user", "admin"),
			"autoFixTitle":   cfg.GetDefault("auto_fix_title", "1") == "1",
			"autoRenameFile": cfg.GetDefault("auto_rename_file", "1") == "1",
			"scanDir":        cfg.GetDefault("scan_dir", "/music"),
			"ffmpegPath":     cfg.GetDefault("ffmpeg_path", ""),
			"editFields":     editFieldsList(cfg),
		})
	})
	pm.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AdminUser      *string   `json:"adminUser"`
			AutoFixTitle   *bool     `json:"autoFixTitle"`
			AutoRenameFile *bool     `json:"autoRenameFile"`
			ScanDir        *string   `json:"scanDir"`
			FFmpegPath     *string   `json:"ffmpegPath"`
			EditFields     *[]string `json:"editFields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.AdminUser != nil {
			if u := strings.TrimSpace(*req.AdminUser); u != "" {
				cfg.Set("admin_user", u)
			}
		}
		if req.EditFields != nil {
			cfg.Set("edit_fields", strings.Join(*req.EditFields, ","))
		}
		if req.AutoFixTitle != nil {
			v := "0"
			if *req.AutoFixTitle {
				v = "1"
			}
			cfg.Set("auto_fix_title", v)
		}
		if req.AutoRenameFile != nil {
			v := "0"
			if *req.AutoRenameFile {
				v = "1"
			}
			cfg.Set("auto_rename_file", v)
		}
		if req.ScanDir != nil {
			if d := strings.TrimSpace(*req.ScanDir); d != "" {
				cfg.Set("scan_dir", d)
			}
		}
		if req.FFmpegPath != nil {
			cfg.Set("ffmpeg_path", strings.TrimSpace(*req.FFmpegPath))
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已保存"})
	})

	// List tracks. 每次返回时动态重算 garbled/needsIdentify 标记（覆盖扫描
	// 时快照），升级后无需重新扫描，"乱码/待识别"筛选立即按最新算法生效。
	// 列表瘦身：歌词正文不下发（见 stripLyrics），编辑时经单曲接口取全量标签。
	pm.HandleFunc("GET /api/tracks", func(w http.ResponseWriter, r *http.Request) {
		recalcFlags(store)
		writeJSON(w, http.StatusOK, stripListLyrics(store.all()))
	})

	// Refresh the list: drop records whose files no longer exist on disk
	// (deleted by another program, external cleanup, etc.), then return the
	// fresh list with up-to-date garbled/needsIdentify markers.
	pm.HandleFunc("GET /api/tracks/refresh", func(w http.ResponseWriter, r *http.Request) {
		removed := 0
		for _, t := range store.all() {
			if _, err := os.Stat(t.Path); err != nil {
				store.removeByPath(t.Path)
				removed++
			}
		}
		recalcFlags(store)
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "tracks": stripListLyrics(store.all())})
	})

	// 单曲详情：完整标签（含 LYRICS 歌词正文），编辑面板选中行时按需取。
	pm.HandleFunc("GET /api/track/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		writeJSON(w, http.StatusOK, t)
	})

	// Duplicates (hash-based, plus fingerprint if available)
	pm.HandleFunc("GET /api/duplicates", func(w http.ResponseWriter, r *http.Request) {
		tracks := store.all()
		// SHA256 按需计算：扫描时不哈希（快），打开去重页时才对缺失的
		// 曲目并发计算一次并缓存回 store。
		store.ensureHashes(tracks, nil, nil)
		groups := dedup.GroupByHash(tracks)
		groups = append(groups, dedup.GroupBySameName(tracks)...)
		groups = append(groups, dedup.GroupByArtistTitle(tracks)...)
		total := 0
		for _, g := range groups {
			total += g.Count
		}
		// 重复检测日志：仅用户主动“重新检测”（?log=1）时记录，
		// 被动查询（列表刷新/任务完成后的自动同步）不写日志，避免刷屏。
		if r.URL.Query().Get("log") == "1" {
			addLog("act_dedupscan", true, len(groups), 0, 0, actDetail("tracks", total))
		}
		writeJSON(w, http.StatusOK, groups)
	})

	// Remove selected duplicate files (user-choose which to keep first)
	pm.HandleFunc("POST /api/duplicates/remove", deleteTracksHandler(store, cfg, "act_dedup"))

	// 列表页选中删除文件（前端二次确认后调用；文件进回收站，同时清理 store）
	pm.HandleFunc("POST /api/tracks/delete", deleteTracksHandler(store, cfg, "act_del"))

	// Serve embedded cover art of a track
	pm.HandleFunc("GET /api/tracks/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		// 封面缓存：大库列表并发加载封面时，避免每个请求都走 WASM 读文件+解码。
		// 以文件 mtime 为失效键（写/清封面会改 mtime，自动失效）；缓存后浏览器
		// 也按 max-age 复用，不再每次轮询全量拉取。
		st, err := os.Stat(t.Path)
		if err != nil {
			writeErr(w, http.StatusNotFound, "file not found")
			return
		}
		store.coverMu.RLock()
		if e, ok := store.coverCache[t.ID]; ok && e.mod.Equal(st.ModTime()) {
			img := e.img
			store.coverMu.RUnlock()
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.Header().Set("Content-Type", http.DetectContentType(img))
			w.Write(img)
			return
		}
		store.coverMu.RUnlock()
		img, err := taglibx.ReadCover(t.Path)
		if err != nil || len(img) == 0 {
			writeErr(w, http.StatusNotFound, "no cover")
			return
		}
		store.coverMu.Lock()
		store.coverCache[t.ID] = coverEntry{mod: st.ModTime(), img: img}
		store.coverMu.Unlock()
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("Content-Type", http.DetectContentType(img))
		w.Write(img)
	})

	// 读取同目录外挂 .lrc 歌词文件
	pm.HandleFunc("GET /api/tracks/{id}/lrcfile", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		lp := taglibx.LrcPath(t.Path)
		if lp == "" {
			writeErr(w, http.StatusNotFound, "no lrc file")
			return
		}
		b, err := os.ReadFile(lp)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": filepath.Base(lp), "content": string(b)})
	})

	// Update tags of a track
	pm.HandleFunc("POST /api/tracks/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		var req struct {
			Clear bool              `json:"clear"`
			Tags  map[string]string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		m := map[string][]string{}
		for k, v := range req.Tags {
			if strings.TrimSpace(v) != "" {
				m[strings.ToUpper(k)] = []string{v}
			}
		}
		if err := taglibx.WriteTags(t.Path, m, req.Clear); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 保存后：标题/艺术家与文件名不符时，把源文件重命名为“艺术家 - 标题.后缀”
		renamed := false
		newName := t.FileName
		if title := strings.TrimSpace(req.Tags["TITLE"]); title != "" {
			if newPath := renameFileToArtistTitle(store, t, req.Tags["ARTIST"], title); newPath != "" {
				renamed = true
				newName = filepath.Base(newPath)
			}
		}
		if !renamed {
			refreshTrack(store, t.Path)
		}
		addLog("act_save", true, 1, 0, 0, t.FileName+(func() string {
			if renamed {
				return " → " + newName
			}
			return ""
		})())
		writeJSON(w, http.StatusOK, map[string]any{"ok": "written", "renamed": renamed, "name": newName})
	})

	// Embed cover art of a track
	pm.HandleFunc("POST /api/tracks/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		var req struct {
			URL        string `json:"url"`
			DataBase64 string `json:"dataBase64"`
			Clear      bool   `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var img []byte
		switch {
		case req.Clear:
			img = nil
		case req.DataBase64 != "":
			b, err := decodeBase64(req.DataBase64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			img = b
		case req.URL != "":
			b, err := fetchBytes(req.URL)
			if err != nil {
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
			img = b
		default:
			writeErr(w, http.StatusBadRequest, "url, dataBase64 or clear required")
			return
		}
		if err := taglibx.WriteCover(t.Path, img); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		refreshTrack(store, t.Path)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "cover written"})
	})

	// Multi-source metadata search
	pm.HandleFunc("GET /api/scrape/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		source := r.URL.Query().Get("source")
		limit := 8
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		// auto/空：并发查询全部启用源并合并（多源同时采集，结果带 source 标记，
		// 前端可跨源对比选最佳）；指定源时仍单源查询。
		var results []scrape.SearchResult
		var err error
		if source == "" || source == "auto" {
			results, err = ms.SearchWithFallback("", q, limit)
		} else {
			results, err = ms.Search(source, q, limit)
		}
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	})

	// Fetch lyrics for a track
	pm.HandleFunc("GET /api/lyrics", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sr := scrape.SearchResult{
			Source:   q.Get("source"),
			SourceID: q.Get("sourceId"),
			Title:    q.Get("title"),
		}
		if a := q.Get("artists"); a != "" {
			sr.Artists = strings.Split(a, "||")
		}
		if sr.Title == "" {
			writeErr(w, http.StatusBadRequest, "title is required")
			return
		}
		lyric, err := ms.FetchLyrics(sr)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"lyric": lyric})
	})

	// Scrape a track: write metadata + cover from a chosen source result
	pm.HandleFunc("POST /api/tracks/{id}/scrape", func(w http.ResponseWriter, r *http.Request) {
		t := store.get(r.PathValue("id"))
		if t == nil {
			writeErr(w, http.StatusNotFound, "track not found")
			return
		}
		var req struct {
			Source      string   `json:"source"`
			SourceID    string   `json:"sourceId"`
			ReleaseMBID string   `json:"releaseMBID"`
			AlbumID     string   `json:"albumID"`
			Title       string   `json:"title"`
			Artists     []string `json:"artists"`
			Album       string   `json:"album"`
			AlbumArtist []string `json:"albumArtist"`
			Genre       []string `json:"genre"`
			TrackNumber int      `json:"trackNumber"`
			Date        string   `json:"date"`
			CoverURL    string   `json:"coverURL"`
			FetchCover  bool     `json:"fetchCover"`
			FetchLyrics bool     `json:"fetchLyrics"`
			Fields      []string `json:"fields"` // 手动选择要写入的字段
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		fields := fieldsFromList(req.Fields, req.FetchCover, req.FetchLyrics)
		tags := map[string][]string{}
		if fields.Title {
			if v := stripAudioExt(strings.TrimSpace(req.Title)); v != "" {
				tags[taglibx.Title] = []string{v}
			}
		}
		if fields.Artist && len(req.Artists) > 0 {
			tags[taglibx.Artist] = req.Artists
		}
		if fields.Album {
			if v := strings.TrimSpace(req.Album); v != "" {
				tags[taglibx.Album] = []string{v}
			}
		}
		if fields.AlbumArtist && len(req.AlbumArtist) > 0 {
			tags[taglibx.AlbumArtist] = req.AlbumArtist
		}
		if fields.Genre && len(req.Genre) > 0 {
			tags[taglibx.Genre] = req.Genre
		}
		if fields.TrackNumber && req.TrackNumber > 0 {
			tags[taglibx.TrackNumber] = []string{strconv.Itoa(req.TrackNumber)}
		}
		if fields.Year {
			if v := strings.TrimSpace(req.Date); v != "" {
				tags[taglibx.Date] = []string{v}
			}
		}
		if v := strings.TrimSpace(req.SourceID); v != "" {
			switch req.Source {
			case "musicbrainz":
				tags[taglibx.MBTrackID] = []string{v}
			case "itunes", "netease", "qq", "kugou", "kuwo", "migu", "bilibili", "qishui":
				tags[taglibx.Comment] = []string{req.Source + ":" + v}
			}
		}
		if v := strings.TrimSpace(req.ReleaseMBID); v != "" {
			tags[taglibx.MBAlbumID] = []string{v}
		}
		if fields.Lyrics && req.Title != "" {
			if lyric, err := ms.FetchLyrics(scrape.SearchResult{
				Source: req.Source, SourceID: req.SourceID,
				Title: req.Title, Artists: req.Artists,
			}); err == nil && strings.TrimSpace(lyric) != "" {
				tags[taglibx.Lyrics] = []string{lyric}
			}
		}
		if err := taglibx.WriteTags(t.Path, tags, false); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if fields.Cover {
			if img, err := ms.FetchCover(scrape.SearchResult{
				Source: req.Source, ReleaseMBID: req.ReleaseMBID,
				CoverURL: req.CoverURL, AlbumID: req.AlbumID,
			}); err == nil && len(img) > 0 {
				taglibx.WriteCover(t.Path, img)
			}
		}
		refreshTrack(store, t.Path)
		addLog("act_scrapeone", true, 1, 0, 0, t.FileName+" ["+req.Source+"]")
		writeJSON(w, http.StatusOK, map[string]string{"ok": "scraped"})
	})

	addr := getenvDefault("LNH_PORT", ":10248")
	log.Printf("Lnh-Music-Tag listening on http://localhost%s", addr)
	log.Printf("admin user: %q (config stored in %s/lnh.db)", adminUser, configDir)
	if generated {
		log.Printf("[首次启动] 已生成随机初始密码并保存到数据库：%s（登录后请在“设置”页修改密码）", adminPass)
	}
	if err := http.ListenAndServe(addr, accessLog(securityHeaders(mux))); err != nil {
		log.Fatal(err)
	}
}

// scanDirLive walks dir, adding audio files to the store progressively and
// updating the scan progress. When autoFix is enabled, trailing audio
// extensions in the title are stripped and written back to the file.
// When autoRename is enabled, files are first renamed to keep only their real
// encoding extension (detected from the header), e.g. "发如雪.mp3.flac" -> "发如雪.flac".
// Known files (already in the store) are smart-skipped without re-reading tags,
// and the scan can be paused/resumed via st.Paused.
//
// skipScanDirs lists NAS/system directory names (case-insensitive) that are
// always skipped during scanning, e.g. Synology @eaDir / #recycle, hidden dirs.
var skipScanDirs = map[string]bool{
	"@eadir": true, "#recycle": true, "@__thumb": true, "@tmp": true,
	"node_modules": true, "$recycle.bin": true, ".git": true, ".svn": true,
}

func scanDirLive(s *store, dir string, st *scanState, autoFix, autoRename bool) (int, int, error) {
	st0, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot access %q: %w", dir, err)
	}
	if !st0.IsDir() {
		return 0, 0, fmt.Errorf("%q is not a directory", dir)
	}
	added := 0
	// 扫描期间先收集新曲目，walk 结束后一次性批量登记并事务分批落库
	// （逐首 add 会让 1000+ 首扫描产生 1000+ 次独立 SQLite 事务）。
	var pending []*model.Track
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		// 暂停点：暂停期间阻塞等待恢复
		for st.snapshot().Paused {
			time.Sleep(300 * time.Millisecond)
		}
		if d.IsDir() {
			// 跳过隐藏目录与 NAS/系统常见目录（@eaDir、#recycle、@__thumb 等）
			if strings.HasPrefix(d.Name(), ".") || skipScanDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		// 跳过隐藏文件（.开头）
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !model.AudioExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		st.mu.Lock()
		st.Total++
		st.mu.Unlock()
		// 智能跳过：已知文件（已在库中）不再重复读标签/算哈希
		if s.exists(path) {
			st.mu.Lock()
			st.Done++
			st.Skipped++
			st.mu.Unlock()
			return nil
		}
		// 文件重命名：去重复/错误音频后缀（先校验真实编码格式）。
		// 走到这里的曲目均为新发现（已在库中的在上面跳过），无需清理旧条目。
		if autoRename {
			if np, rerr := renameCleanFile(path); rerr == nil && np != path {
				path = np
			}
		}
		t, terr := taglibx.ReadTrackLight(path)
		if terr != nil {
			st.mu.Lock()
			st.Done++
			st.mu.Unlock()
			return nil
		}
		// 默认去后缀：标题含音频扩展名时自动修正并写回文件
		if autoFix {
			title := strings.TrimSpace(t.Tags["TITLE"])
			if title == "" {
				title = t.FileName
			}
			if fixed := stripAudioExt(title); fixed != title {
				if taglibx.WriteTags(path, map[string][]string{taglibx.Title: {fixed}}, false) == nil {
					t, _ = taglibx.ReadTrackLight(path)
				}
			}
		}
		if !s.exists(path) {
			added++
		}
		pending = append(pending, t)
		st.mu.Lock()
		st.Added = s.allTracksCount() + len(pending) // 已入库 + 本批待登记，保持进度即时
		st.Done++
		st.mu.Unlock()
		return nil
	})
	// 无论 walk 是否被错误中断，先把已收集的曲目登记并落库（不丢已扫描部分）
	s.addMany(pending)
	if err != nil {
		return added, 0, err
	}
	// 清理已消失文件：被其他程序/外部删除的曲目从列表同步移除
	removed := reconcileRemoved(s, dir)
	return added, removed, nil
}

// reconcileRemoved removes from the store any track under dir whose file no
// longer exists on disk (deleted externally), keeping the list in sync.
func reconcileRemoved(s *store, dir string) int {
	removed := 0
	dir = filepath.Clean(dir)
	for _, t := range s.all() {
		rel, err := filepath.Rel(dir, t.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if _, err := os.Stat(t.Path); err != nil {
			s.remove(t.ID)
			removed++
		}
	}
	return removed
}

// allTracksCount returns the current number of tracks in the store.
func (s *store) allTracksCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tracks)
}

// renameTrack 在乱码修复把文件重命名后调用：更新 store 的路径索引、重读
// 新路径标签（保持 ID 不变，列表选中/去重索引不失效），并同步 SQLite。
func renameTrack(s *store, t *model.Track, newPath string) {
	s.mu.Lock()
	oldPath := t.Path
	if id, ok := s.byPath[oldPath]; ok && id == t.ID {
		delete(s.byPath, oldPath)
	}
	t.Path = newPath
	t.FileName = filepath.Base(newPath)
	s.byPath[newPath] = t.ID
	order := s.order[t.ID]
	db := s.db
	s.mu.Unlock()
	// 库内删除旧路径行（同一 id 保持 seq 重新 upsert）
	deleteTrackDB(db, s, "", oldPath)
	if nt, err := taglibx.ReadTrackLight(newPath); err == nil {
		nt.ID = t.ID // 保持原 ID：列表勾选/去重索引不因重命名失效
		nt.SHA256 = t.SHA256
		nt.HasTrad = detectTraditional(nt.Tags)
		nt.Garbled = looksGarbledTags(nt)
		nt.NeedsIdentify = needsIdentify(nt)
		s.mu.Lock()
		s.tracks[t.ID] = nt
		s.byPath[newPath] = nt.ID
		s.rev++
		if _, ok := s.order[nt.ID]; !ok {
			s.order[nt.ID] = order
		}
		db2 := s.db
		s.mu.Unlock()
		upsertTrackDB(db2, s, nt)
	} else {
		// 新路径读取失败：原对象仍可用（路径已更新），仅落库
		upsertTrackDB(db, s, t)
	}
}

// refreshTrack re-reads a track's tags/properties after a write.
// Uses the light read (no full-file hash): SHA256 is computed lazily on the
// dedup page and cached back into the store. The refreshed record is also
// persisted so the list survives restarts/upgrades.
func refreshTrack(s *store, path string) {
	t, err := taglibx.ReadTrackLight(path)
	if err != nil {
		return
	}
	// 保留已有的内容哈希（若有），避免刷新标签后哈希丢失
	if old := s.get(s.byPathID(path)); old != nil {
		t.SHA256 = old.SHA256
	}
	t.HasTrad = detectTraditional(t.Tags)
	t.Garbled = looksGarbledTags(t)
	t.NeedsIdentify = needsIdentify(t)
	s.mu.Lock()
	s.tracks[t.ID] = t
	s.byPath[path] = t.ID
	s.rev++
	if _, ok := s.order[t.ID]; !ok {
		s.order[t.ID] = s.seq.Add(1)
	}
	db := s.db
	s.mu.Unlock()
	upsertTrackDB(db, s, t)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// 编辑面板默认显示的常用字段（未配置时）；可在设置页自定义。
const defaultEditFields = "cover,title,artist,album,albumartist,genre,year,tracknumber,tracktotal,composer,lyricist,lyrics"

// editFieldsList 返回设置页配置的编辑面板显示字段列表（逗号分隔，去空）。
func editFieldsList(cfg *cstore.Config) []string {
	var out []string
	for _, f := range strings.Split(cfg.GetDefault("edit_fields", defaultEditFields), ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// recalcFlags 重算全部曲目的乱码/待识别/繁体标记（覆盖扫描时的快照）。
// /api/tracks 与刷新接口共用，保证筛选与最新修复算法一致，无需重扫描。
func recalcFlags(s *store) {
	for _, t := range s.all() {
		t.Garbled = looksGarbledTags(t)
		t.NeedsIdentify = needsIdentify(t)
		t.HasTrad = detectTraditional(t.Tags)
		t.HasLyrics = strings.TrimSpace(t.Tags["LYRICS"]) != ""
	}
}

// deleteTracksHandler 删除勾选曲目（磁盘 → 回收站 + 库）：去重页与列表页删除共用。
// actKey 是操作日志的 action 键（act_dedup / act_del）。
func deleteTracksHandler(s *store, cfg *cstore.Config, actKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		removed := 0
		failed := []string{}
		for _, id := range req.IDs {
			t := s.get(id)
			if t == nil {
				continue
			}
			if _, err := moveToTrash(cfg, t.Path); err != nil {
				failed = append(failed, t.FileName)
				continue
			}
			s.remove(id)
			removed++
		}
		addLog(actKey, len(failed) == 0, removed, len(failed), 0, "")
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "failed": failed})
	}
}

func decodeBase64(s string) ([]byte, error) {
	imported := s
	if i := strings.Index(s, ","); i >= 0 {
		imported = s[i+1:]
	}
	return decodeBase64Raw(imported)
}
