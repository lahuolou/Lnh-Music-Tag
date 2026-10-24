// pending.go：低置信度刮削队列（P0 刮削可靠性）。
//
// 刮削融合结果的匹配置信度低于 lowConfidenceThreshold 时，把「原标签 vs 融合
// 候选」快照记入低置信度队列（pending_tracks），前端曲库提供「待确认」入口：
//   - 列表：展示原文件名/原标题/艺人、融合候选、置信度；
//   - 确认：用户核对候选正确（仅标记，文件标签保留刮削结果）；
//   - 忽略：用户判断候选错误（仅标记，不再提示）。
//
// 队列只读展示 + 确认/忽略标记，不提供回滚（文件标签已由刮削流程写入，
// 如需修正可在曲库编辑面板手动改）。表按 path 唯一，重复入队覆盖旧记录。
package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
)

// ensurePendingTable creates the low-confidence queue table if missing.
func ensurePendingTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS pending_tracks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		path       TEXT NOT NULL,
		title      TEXT NOT NULL,
		artist     TEXT DEFAULT '',
		fused_title   TEXT DEFAULT '',
		fused_artist  TEXT DEFAULT '',
		fused_album   TEXT DEFAULT '',
		confidence INTEGER NOT NULL,
		status     TEXT NOT NULL DEFAULT 'pending',
		created_at INTEGER NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_path ON pending_tracks(path)`)
	return err
}

// PendingTrack 低置信度队列条目（GET /api/scrape/pending 返回）。
type PendingTrack struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Title       string `json:"title"`  // 原标签/文件名标题
	Artist      string `json:"artist"` // 原标签/文件名艺人
	FusedTitle  string `json:"fusedTitle"`
	FusedArtist string `json:"fusedArtist"`
	FusedAlbum  string `json:"fusedAlbum"`
	Confidence  int    `json:"confidence"`
	Status      string `json:"status"` // pending / confirmed / ignored
	CreatedAt   int64  `json:"createdAt"`
}

// enqueuePending 刮削融合结果低置信度时入队（path 唯一，覆盖旧记录）。
// best-effort：数据库失败仅记日志，不中断刮削任务。
func enqueuePending(t *model.Track, wantTitle, wantArtist string, sr scrape.SearchResult) {
	if activityDB == nil || t == nil || !isLowConfidence(sr) {
		return
	}
	title, artist := wantTitle, wantArtist
	if title == "" {
		title = t.FileName
	}
	nowUnix := time.Now().Unix()
	_, err := activityDB.Exec(`
		INSERT INTO pending_tracks(path, title, artist, fused_title, fused_artist, fused_album, confidence, status, created_at)
		VALUES(?,?,?,?,?,?,?, 'pending', ?)
		ON CONFLICT(path) DO UPDATE SET
			title=excluded.title, artist=excluded.artist,
			fused_title=excluded.fused_title, fused_artist=excluded.fused_artist,
			fused_album=excluded.fused_album, confidence=excluded.confidence,
			status='pending', created_at=excluded.created_at`,
		t.Path, title, artist, sr.Title, stringsJoin(sr.Artists), sr.Album, sr.Confidence, nowUnix)
	if err != nil {
		log.Printf("enqueue pending track %s: %v", t.Path, err)
	}
}

func stringsJoin(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " / "
		}
		out += s
	}
	return out
}

// pendingListHandler GET /api/scrape/pending 列出队列（pending 优先，其余按时间倒序）。
func pendingListHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statusFilter := r.URL.Query().Get("status")
		rows, err := db.Query(`
			SELECT id, path, title, artist, fused_title, fused_artist, fused_album, confidence, status, created_at
			FROM pending_tracks
			WHERE (? = '' OR status = ?)
			ORDER BY CASE status WHEN 'pending' THEN 0 ELSE 1 END, created_at DESC`, statusFilter, statusFilter)
		if err != nil {
			http.Error(w, "查询失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []PendingTrack{}
		for rows.Next() {
			var p PendingTrack
			if err := rows.Scan(&p.ID, &p.Path, &p.Title, &p.Artist, &p.FusedTitle, &p.FusedArtist,
				&p.FusedAlbum, &p.Confidence, &p.Status, &p.CreatedAt); err != nil {
				http.Error(w, "读取失败: "+err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": out})
	}
}

// pendingResolveHandler POST /api/scrape/pending/resolve {id, action} 标记确认/忽略。
// action: confirm | ignore。
func pendingResolveHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
			http.Error(w, "无效请求", http.StatusBadRequest)
			return
		}
		status := "confirmed"
		if req.Action == "ignore" {
			status = "ignored"
		}
		if _, err := db.Exec(`UPDATE pending_tracks SET status=? WHERE id=?`, status, req.ID); err != nil {
			http.Error(w, "更新失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": req.ID, "status": status})
	}
}
