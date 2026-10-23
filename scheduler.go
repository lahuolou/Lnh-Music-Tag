// 任务计划（后台定时自动刮削）：
//
//	设置页可配置启用开关、cron 表达式（分 时 日 月 周）、刮削模式与「先扫描」开关。
//	调度器每 30 秒检查一次 cron 是否到点；到点后（可选）先触发目录扫描，再对全部
//	曲目启动一键刮削流水线。运行中不重复触发（防重入），每次触发写操作日志。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

// ---------- cron 表达式（5 段：分 时 日 月 周）----------

type cronField struct {
	all bool
	set map[int]bool
}

func (f cronField) match(v int) bool {
	if f.all {
		return true
	}
	return f.set[v]
}

// parseCronList 解析单段：* | */n | a | a-b | a,b,c（含组合与步进）。
func parseCronList(s string, lo, hi int) (cronField, error) {
	f := cronField{set: map[int]bool{}}
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		f.all = true
		return f, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			sv, err := strconv.Atoi(part[i+1:])
			if err != nil || sv <= 0 {
				return f, fmt.Errorf("cron 步进无效: %q", part)
			}
			step = sv
			part = part[:i]
		}
		if part == "*" {
			for v := lo; v <= hi; v += step {
				f.set[v] = true
			}
			continue
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			a, err1 := strconv.Atoi(part[:i])
			b, err2 := strconv.Atoi(part[i+1:])
			if err1 != nil || err2 != nil || a > b {
				return f, fmt.Errorf("cron 范围无效: %q", part)
			}
			for v := a; v <= b; v += step {
				f.set[v] = true
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return f, fmt.Errorf("cron 值无效: %q", part)
		}
		f.set[v] = true
	}
	return f, nil
}

type cronExpr struct {
	min, hour, dom, mon, dow cronField
}

// parseCron 解析标准 5 段 cron：分(0-59) 时(0-23) 日(1-31) 月(1-12) 周(0-6, 7=周日)。
func parseCron(expr string) (*cronExpr, error) {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron 需 5 段（分 时 日 月 周），当前 %d 段", len(parts))
	}
	var e cronExpr
	var err error
	if e.min, err = parseCronList(parts[0], 0, 59); err != nil {
		return nil, err
	}
	if e.hour, err = parseCronList(parts[1], 0, 23); err != nil {
		return nil, err
	}
	if e.dom, err = parseCronList(parts[2], 1, 31); err != nil {
		return nil, err
	}
	if e.mon, err = parseCronList(parts[3], 1, 12); err != nil {
		return nil, err
	}
	if e.dow, err = parseCronList(parts[4], 0, 7); err != nil {
		return nil, err
	}
	// 周字段 7 等价于 0（周日）。
	e.dow.set[0] = e.dow.set[0] || e.dow.set[7]
	delete(e.dow.set, 7)
	return &e, nil
}

func (e *cronExpr) match(t time.Time) bool {
	return e.min.match(t.Minute()) &&
		e.hour.match(t.Hour()) &&
		e.dom.match(t.Day()) &&
		e.mon.match(int(t.Month())) &&
		e.dow.match(int(t.Weekday()))
}

// nextAfter 返回 after 之后（严格大于）第一个匹配时刻；31 天内找不到返回零值。
func (e *cronExpr) nextAfter(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 24*60*31; i++ {
		if e.match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// ---------- 调度器 ----------

const (
	schedEnableKey = "scheduler_enabled"
	schedCronKey   = "scheduler_cron"
	schedModeKey   = "scheduler_mode"
	schedScanKey   = "scheduler_scan_first"
	watchEnableKey = "watcher_enabled"
	watchIntKey    = "watcher_interval"
)

type scheduler struct {
	cfg   *cstore.Config
	js    *jobStore
	st    *store
	ms    *scrape.MultiSource
	scans *scanManager

	mu      sync.Mutex
	running bool
	lastRun time.Time
	lastMsg string
	nextRun time.Time
	stopCh  chan struct{}

	// 后台监控（静默刮削新增歌曲）
	watchMu       sync.Mutex
	watchBase     map[string]time.Time // path -> mtime 基线；首次建立后只处理其后新增
	watchRunning  bool
	watchLast     time.Time // 上次目录清单检查时间
	lastWatchRun  time.Time
	lastWatchMsg  string
}

func newScheduler(cfg *cstore.Config, js *jobStore, st *store, ms *scrape.MultiSource, scans *scanManager) *scheduler {
	return &scheduler{cfg: cfg, js: js, st: st, ms: ms, scans: scans}
}

func (s *scheduler) start() {
	s.stopCh = make(chan struct{})
	go s.loop()
}

func (s *scheduler) stop() {
	if s.stopCh != nil {
		close(s.stopCh)
	}
}

func (s *scheduler) enabled() bool { return s.cfg.GetDefault(schedEnableKey, "0") == "1" }

func (s *scheduler) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.check()
			s.watchCheck(time.Now())
		}
	}
}

func (s *scheduler) watchEnabled() bool { return s.cfg.GetDefault(watchEnableKey, "0") == "1" }

func (s *scheduler) watchInterval() time.Duration {
	return time.Duration(s.watchIntervalMinutes()) * time.Minute
}

// watchIntervalMinutes 返回监控检查间隔（分钟，1-1440）。
func (s *scheduler) watchIntervalMinutes() int {
	n, err := strconv.Atoi(s.cfg.GetDefault(watchIntKey, "5"))
	if err != nil || n < 1 {
		n = 5
	}
	if n > 1440 {
		n = 1440
	}
	return n
}

// watchCheck 按间隔检查目录清单：发现新增音频文件后静默入库并自动刮削。
func (s *scheduler) watchCheck(now time.Time) {
	if !s.watchEnabled() {
		return
	}
	interval := s.watchInterval()
	s.watchMu.Lock()
	if !s.watchLast.IsZero() && now.Sub(s.watchLast) < interval {
		s.watchMu.Unlock()
		return
	}
	s.watchLast = now
	s.watchMu.Unlock()
	s.watchScan(now)
}

// watchScan 收集目录音频清单并与基线对比，对新增曲目静默入库 + 自动刮削。
// 首次运行只建立基线（不刮削），避免进程重启后对全库重复刮削。
func (s *scheduler) watchScan(now time.Time) {
	s.watchMu.Lock()
	if s.watchRunning {
		s.watchMu.Unlock()
		return
	}
	s.watchRunning = true
	s.watchMu.Unlock()
	defer func() {
		s.watchMu.Lock()
		s.watchRunning = false
		s.watchMu.Unlock()
	}()

	dir := strings.TrimSpace(s.cfg.GetDefault("scan_dir", "/music"))
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		log.Printf("[watch] 目录不可访问，跳过本次检查: %q (err=%v)", dir, err)
		return
	}

	// 收集当前目录全部音频文件（path -> mtime）。
	current := map[string]time.Time{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || skipScanDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !model.AudioExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		current[filepath.Clean(path)] = info.ModTime()
		return nil
	})

	s.watchMu.Lock()
	first := s.watchBase == nil
	if first {
		s.watchBase = current
		s.watchMu.Unlock()
		return
	}
	var added []string
	for p := range current {
		if _, ok := s.watchBase[p]; !ok {
			added = append(added, p)
		}
	}
	s.watchMu.Unlock()
	if len(added) == 0 {
		return
	}

	// 静默处理：读标签入库，再对新增曲目批量刮削（封面+歌词+全部元数据）。
	var ids []string
	for _, p := range added {
		if s.st.exists(p) {
			continue
		}
		t, terr := taglibx.ReadTrackLight(p)
		if terr != nil {
			continue
		}
		s.st.add(t)
		ids = append(ids, t.ID)
	}
	if len(ids) == 0 {
		s.watchMu.Lock()
		s.watchBase = current
		s.watchMu.Unlock()
		return
	}
	job := s.js.create(len(ids), "scrape")
	s.js.runBounded(job, func() {
		runBatch(job, s.st, s.ms, s.cfg, "auto", ids, fieldsFromList(nil, true, true), false)
	})
	msg := fmt.Sprintf("监控新增 %d 首，已静默刮削（任务 %s）", len(ids), job.ID)
	s.mu.Lock()
	s.lastWatchRun = now
	s.lastWatchMsg = msg
	s.mu.Unlock()
	addLog("act_watch", true, 1, 0, 0, "任务计划："+msg)

	// 处理完成后把新增并入基线，避免下次重复。
	s.watchMu.Lock()
	s.watchBase = current
	s.watchMu.Unlock()
}

func (s *scheduler) check() {
	if !s.enabled() {
		return
	}
	e, err := parseCron(s.cfg.GetDefault(schedCronKey, "0 3 * * *"))
	if err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.nextRun.IsZero() {
		s.nextRun = e.nextAfter(now)
	}
	due := !now.Before(s.nextRun)
	s.mu.Unlock()
	if !due {
		return
	}
	s.mu.Lock()
	s.nextRun = e.nextAfter(now)
	s.mu.Unlock()
	s.fire(now)
}

// fire 执行一次计划任务：可选先扫描，再对全部曲目跑一键刮削。防重入。
func (s *scheduler) fire(now time.Time) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	if s.cfg.GetDefault(schedScanKey, "1") == "1" {
		dir := strings.TrimSpace(s.cfg.GetDefault("scan_dir", "/music"))
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			autoFix := s.cfg.GetDefault("auto_fix_title", "1") == "1"
			autoRename := s.cfg.GetDefault("auto_rename_file", "1") == "1"
			if err := s.scans.start(s.st, dir, autoFix, autoRename); err == nil {
				addLog("act_sched", true, 1, 0, 0, "任务计划：开始扫描 "+dir)
			}
		}
	}

	list := resolveTracks(s.st, nil)
	if len(list) == 0 {
		s.mu.Lock()
		s.lastRun = now
		s.lastMsg = "曲库为空，已跳过刮削"
		s.mu.Unlock()
		return
	}
	// 刮削模式：onekey = 完整一键流水线；dedup = 先去重再一键刮削。
	job := s.js.create(len(list), "onekey")
	if s.cfg.GetDefault(schedModeKey, "onekey") == "dedup" {
		s.js.runBounded(job, func() {
			runOneKeyPipeline(job, s.st, s.ms, list, true, s.cfg)
		})
	} else {
		s.js.runBounded(job, func() {
			runOneKeyPipeline(job, s.st, s.ms, list, false, s.cfg)
		})
	}
	msg := fmt.Sprintf("自动刮削 %d 首（任务 %s）", len(list), job.ID)
	s.mu.Lock()
	s.lastRun = now
	s.lastMsg = msg
	s.mu.Unlock()
	addLog("act_sched", true, 1, 0, 0, "任务计划："+msg)
}

// ---------- API ----------

// getHandler GET /api/scheduler：当前配置 + 运行状态。
func (s *scheduler) getHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":      s.enabled(),
		"cron":         s.cfg.GetDefault(schedCronKey, "0 3 * * *"),
		"mode":         s.cfg.GetDefault(schedModeKey, "onekey"),
		"scanFirst":    s.cfg.GetDefault(schedScanKey, "1") == "1",
		"running":      s.running,
		"lastRun":      fmtSchedTime(s.lastRun),
		"lastMsg":      s.lastMsg,
		"nextRun":      fmtSchedTime(s.nextRun),
		"watchEnabled": s.watchEnabled(),
		"watchInterval": s.watchIntervalMinutes(),
		"watchLastRun":  fmtSchedTime(s.lastWatchRun),
		"watchLastMsg":  s.lastWatchMsg,
	})
}

// saveHandler POST /api/scheduler：保存配置并立即重算下次运行时间。
func (s *scheduler) saveHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled        *bool   `json:"enabled"`
		Cron           *string `json:"cron"`
		Mode           *string `json:"mode"`
		ScanFirst      *bool   `json:"scanFirst"`
		WatchEnabled   *bool   `json:"watchEnabled"`
		WatchInterval  *int    `json:"watchInterval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled != nil {
		v := "0"
		if *req.Enabled {
			v = "1"
		}
		s.cfg.Set(schedEnableKey, v)
	}
	if req.Cron != nil {
		expr := strings.TrimSpace(*req.Cron)
		if _, err := parseCron(expr); err != nil {
			writeErr(w, http.StatusBadRequest, "cron 表达式无效: "+err.Error())
			return
		}
		s.cfg.Set(schedCronKey, expr)
	}
	if req.Mode != nil && (*req.Mode == "onekey" || *req.Mode == "dedup") {
		s.cfg.Set(schedModeKey, *req.Mode)
	}
	if req.ScanFirst != nil {
		v := "0"
		if *req.ScanFirst {
			v = "1"
		}
		s.cfg.Set(schedScanKey, v)
	}
	if req.WatchEnabled != nil {
		v := "0"
		if *req.WatchEnabled {
			v = "1"
		}
		s.cfg.Set(watchEnableKey, v)
	}
	if req.WatchInterval != nil && *req.WatchInterval >= 1 && *req.WatchInterval <= 1440 {
		s.cfg.Set(watchIntKey, strconv.Itoa(*req.WatchInterval))
	}
	// 保存后立即重算下次运行时间。
	if e, err := parseCron(s.cfg.GetDefault(schedCronKey, "0 3 * * *")); err == nil {
		s.mu.Lock()
		s.nextRun = e.nextAfter(time.Now())
		s.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已保存"})
}

// runHandler POST /api/scheduler/run：立即触发一次（手动测试）。
func (s *scheduler) runHandler(w http.ResponseWriter, r *http.Request) {
	s.fire(time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已触发"})
}

func fmtSchedTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}
