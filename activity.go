// 操作日志：把用户的关键操作（扫描/刮削/乱码修复/简繁转换/格式转换/
// 音频识别/修正后缀/去重删除/保存标签等）记录到 SQLite，后台提供日志
// 列表与删除功能。主文案走前端 i18n（action 键 + count/fail/skip），
// detail 仅存自由文本补充（如扫描时清理的失效文件数、被操作的文件名）。
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type activityLog struct {
	ID     int64  `json:"id"`
	Time   string `json:"time"`
	Action string `json:"action"` // i18n 键：act_scan / act_scrape / act_onekey / act_fixenc / act_script / act_convert / act_identify / act_renameclean / act_dedup / act_del / act_save / act_scrapeone / act_cover
	OK     bool   `json:"ok"`
	Count  int    `json:"count"`  // 成功/处理数量
	Fail   int    `json:"fail"`   // 失败数量
	Skip   int    `json:"skip"`   // 跳过数量
	Detail string `json:"detail"` // 自由文本补充，原样显示
}

// ensureActivityTable creates the operation log table.
func ensureActivityTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS activity_logs (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		time   TEXT NOT NULL,
		action TEXT NOT NULL,
		ok     INTEGER NOT NULL,
		count  INTEGER NOT NULL DEFAULT 0,
		fail   INTEGER NOT NULL DEFAULT 0,
		skip   INTEGER NOT NULL DEFAULT 0,
		detail TEXT NOT NULL DEFAULT ''
	)`)
	return err
}

// activityDB 指向主数据库（main 初始化时赋值），nil 时日志静默跳过。
var activityDB *sql.DB

func addLog(action string, ok bool, count, fail, skip int, detail string) {
	if activityDB == nil {
		return
	}
	okI := 0
	if ok {
		okI = 1
	}
	if _, err := activityDB.Exec(`INSERT INTO activity_logs (time, action, ok, count, fail, skip, detail) VALUES (?,?,?,?,?,?,?)`,
		time.Now().Format("2006-01-02 15:04:05"), action, okI, count, fail, skip, detail); err != nil {
		log.Printf("addLog: %v", err)
	}
}

// logJobDone 在 runProgressJob / 批量刮削 / 一键刮削完成后统一写日志：
// 按任务类型统计成功/失败/跳过。一键刮削为一首曲目多阶段记录，
// 按曲目聚合（每首取最后一个阶段的结果）后再统计。
func logJobDone(j *scrapeJob) {
	if j == nil || j.Kind == "" {
		return
	}
	ok, fail, skip := 0, 0, 0
	count := func(r *scrapeResult) {
		// 跳过优先判定：OK=false 但 Skip=true 是"中性跳过"（如无乱码/已完整），不算失败
		if r.Skip {
			skip++
		} else if r.OK {
			ok++
		} else {
			fail++
		}
	}
	if j.Kind == "onekey" {
		last := map[string]*scrapeResult{}
		for i := range j.Results {
			last[j.Results[i].ID] = &j.Results[i]
		}
		for _, r := range last {
			count(r)
		}
	} else {
		for i := range j.Results {
			count(&j.Results[i])
		}
	}
	act := "act_job"
	switch j.Kind {
	case "scrape":
		act = "act_scrape"
	case "onekey":
		act = "act_onekey"
	case "fixEnc":
		act = "act_fixenc"
	case "script":
		act = "act_script"
	case "convert":
		act = "act_convert"
	case "identify":
		act = "act_identify"
	case "renameClean":
		act = "act_renameclean"
	}
	addLog(act, fail == 0, ok, fail, skip, "")
}

// listLogs returns the newest logs (descending).
func listLogs(db *sql.DB, limit int) []activityLog {
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.Query(`SELECT id, time, action, ok, count, fail, skip, detail FROM activity_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		log.Printf("listLogs: %v", err)
		return []activityLog{}
	}
	defer rows.Close()
	var out []activityLog
	for rows.Next() {
		var l activityLog
		var okI int
		if err := rows.Scan(&l.ID, &l.Time, &l.Action, &okI, &l.Count, &l.Fail, &l.Skip, &l.Detail); err != nil {
			continue
		}
		l.OK = okI == 1
		out = append(out, l)
	}
	return out
}

func logsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"logs": listLogs(db, 200)})
	}
}

// logsDeleteHandler 删除指定 id 的日志（勾选后批量删除）。
func logsDeleteHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(req.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择要删除的日志"})
			return
		}
		removed := 0
		for _, id := range req.IDs {
			res, err := db.Exec(`DELETE FROM activity_logs WHERE id = ?`, id)
			if err != nil {
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				removed++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
	}
}

// logsClearHandler 清空全部日志。
func logsClearHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := db.Exec(`DELETE FROM activity_logs`); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已清空日志"})
	}
}

// actDetail 拼装 detail 字段：把成对的名值拼成可读文本（如 "removed:2 skipped:30"）。
func actDetail(pairs ...any) string {
	var s string
	for i := 0; i+1 < len(pairs); i += 2 {
		if s != "" {
			s += " "
		}
		s += fmt.Sprintf("%v:%v", pairs[i], pairs[i+1])
	}
	return s
}
