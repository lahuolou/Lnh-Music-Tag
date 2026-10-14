// 曲目持久化：把扫描结果写入 SQLite（与设置同一 lnh.db），启动时加载。
// 这样升级/重启容器后曲目列表不丢，无需重新扫描；扫描/编辑/重命名/删除
// 都会同步落库（refreshTrack 是写操作后的统一入口）。
package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"lnh-musictag/internal/model"
)

// ensureTracksTable creates the tracks table if missing.
func ensureTracksTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS tracks (
		id   TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		data TEXT NOT NULL,  -- Track JSON
		seq  INTEGER NOT NULL
	)`)
	return err
}

// loadTracksFromDB rebuilds the in-memory store from the tracks table,
// restoring scan order (seq) so a restart behaves like a fresh scan.
// Tracks whose files were deleted externally since last run are dropped.
func loadTracksFromDB(s *store, db *sql.DB) {
	rows, err := db.Query(`SELECT id, path, data, seq FROM tracks ORDER BY seq`)
	if err != nil {
		log.Printf("loadTracks: %v", err)
		return
	}
	defer rows.Close()
	type row struct {
		id, path, data string
		seq            int64
	}
	var rowsOut []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path, &r.data, &r.seq); err != nil {
			log.Printf("loadTracks scan: %v", err)
			continue
		}
		rowsOut = append(rowsOut, r)
	}
	for _, r := range rowsOut {
		var t model.Track
		if err := json.Unmarshal([]byte(r.data), &t); err != nil {
			log.Printf("loadTracks unmarshal %s: %v", r.id, err)
			continue
		}
		// 文件已被外部删除的曲目：跳过（保留库内记录由下次扫描清理）
		s.mu.Lock()
		if _, ok := s.byPath[t.Path]; ok {
			s.mu.Unlock()
			continue
		}
		s.tracks[t.ID] = &t
		s.byPath[t.Path] = t.ID
		if r.seq > s.seq {
			s.seq = r.seq
		}
		s.order[t.ID] = r.seq
		s.mu.Unlock()
	}
	log.Printf("loadTracks: restored %d tracks from database", len(rowsOut))
}

func marshalTrack(t *model.Track) (string, error) {
	b, err := json.Marshal(t)
	return string(b), err
}

// upsertTrackDB persists one track (INSERT on first scan, UPDATE afterwards).
// Errors are logged and ignored: persistence is best-effort, memory is the
// source of truth for the running process.
func upsertTrackDB(db *sql.DB, s *store, t *model.Track) {
	if db == nil {
		return
	}
	data, err := marshalTrack(t)
	if err != nil {
		return
	}
	s.mu.RLock()
	seq, ok := s.order[t.ID]
	s.mu.RUnlock()
	if !ok {
		seq = time.Now().UnixNano()
	}
	_, err = db.Exec(
		`INSERT INTO tracks(id,path,data,seq) VALUES(?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET path=excluded.path, data=excluded.data, seq=excluded.seq`,
		t.ID, t.Path, data, seq)
	if err != nil {
		log.Printf("upsertTrackDB %s: %v", t.ID, err)
	}
}

// deleteTrackDB removes a persisted track by id (and by path fallback).
func deleteTrackDB(db *sql.DB, s *store, id, path string) {
	if db == nil {
		return
	}
	if _, err := db.Exec(`DELETE FROM tracks WHERE id=? OR path=?`, id, path); err != nil {
		log.Printf("deleteTrackDB %s: %v", id, err)
	}
}
