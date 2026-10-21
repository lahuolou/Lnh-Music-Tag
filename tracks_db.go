// 曲目持久化：把扫描结果写入 SQLite（与设置同一 lnh.db），启动时加载。
// 这样升级/重启容器后曲目列表不丢，无需重新扫描；扫描/编辑/重命名/删除
// 都会同步落库（refreshTrack 是写操作后的统一入口）。
//
// 写入策略：单条路径（upsertTrackDB）保留给编辑/重命名等低频写操作；
// 批量路径（upsertTracksDB）用于扫描/哈希等大库场景，按批开事务写入，
// 显著降低 fsync 次数。持久化始终是 best-effort：内存状态是权威，
// 失败批次只记日志不中断任务。
package main

import (
	"database/sql"
	"encoding/json"
	"log"

	"lnh-music-tag/internal/model"
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
//
// 性能与健壮性：
//   - 先锁外完成全部查询/反序列化，再一次性加锁写入（避免逐条 Lock/Unlock）；
//   - 用库内 MAX(seq) 初始化原子序号计数器，后续新增/更新不再依赖时间戳，
//     保证重启前后 ORDER BY seq 结果一致（见 trackSeq）。
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
	var maxSeq int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path, &r.data, &r.seq); err != nil {
			log.Printf("loadTracks scan: %v", err)
			continue
		}
		rowsOut = append(rowsOut, r)
		if r.seq > maxSeq {
			maxSeq = r.seq
		}
	}
	// 锁外预解析：坏 JSON 直接跳过，不在临界区内做字符串解析
	type parsed struct {
		track *model.Track
		seq   int64
	}
	parsedOut := make([]parsed, 0, len(rowsOut))
	for _, r := range rowsOut {
		var t model.Track
		if err := json.Unmarshal([]byte(r.data), &t); err != nil {
			log.Printf("loadTracks unmarshal %s: %v", r.id, err)
			continue
		}
		parsedOut = append(parsedOut, parsed{track: &t, seq: r.seq})
	}
	// 一次性加锁批量写入
	restored := 0
	s.mu.Lock()
	for _, p := range parsedOut {
		// 文件已被外部删除的曲目：跳过（保留库内记录由下次扫描清理）
		if _, ok := s.byPath[p.track.Path]; ok {
			continue
		}
		s.tracks[p.track.ID] = p.track
		s.byPath[p.track.Path] = p.track.ID
		s.order[p.track.ID] = p.seq
		restored++
	}
	if maxSeq > s.seq.Load() {
		s.seq.Store(maxSeq)
	}
	s.mu.Unlock()
	log.Printf("loadTracks: restored %d tracks from database", restored)
}

func marshalTrack(t *model.Track) (string, error) {
	b, err := json.Marshal(t)
	return string(b), err
}

// trackSeq 返回曲目的持久化序号。曲目已在 order 中时直接返回（保持稳定）；
// 否则原子分配新序号并回写 order（不再使用时间戳，避免并发写入时
// UnixNano 重复导致重启后排序漂移）。
func (s *store) trackSeq(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq, ok := s.order[id]; ok {
		return seq
	}
	seq := s.seq.Add(1)
	s.order[id] = seq
	return seq
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
	seq := s.trackSeq(t.ID)
	_, err = db.Exec(
		`INSERT INTO tracks(id,path,data,seq) VALUES(?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET path=excluded.path, data=excluded.data, seq=excluded.seq`,
		t.ID, t.Path, data, seq)
	if err != nil {
		log.Printf("upsertTrackDB %s: %v", t.ID, err)
	}
}

// batchSize 单事务写入的曲目数上限：事务过大失败面扩大、回滚成本升高；
// 200–500 是吞吐与风险的平衡点。
const batchSize = 300

// upsertTracksDB 批量落库（扫描/哈希等大库场景使用）：按 batchSize 分批
// 事务写入，某批失败只记日志并统计失败数，不中断整体（内存状态始终权威，
// 任务照常完成）。返回 (成功数, 失败数)。
func upsertTracksDB(db *sql.DB, s *store, tracks []*model.Track) (int, int) {
	if db == nil || len(tracks) == 0 {
		return len(tracks), 0
	}
	okN, failN := 0, 0
	for i := 0; i < len(tracks); i += batchSize {
		end := i + batchSize
		if end > len(tracks) {
			end = len(tracks)
		}
		n, err := upsertTrackBatch(db, s, tracks[i:end])
		if err != nil {
			log.Printf("upsertTracksDB batch %d..%d (%d tracks): %v", i, end, end-i, err)
			failN += end - i
			continue
		}
		okN += n
	}
	if failN > 0 {
		log.Printf("upsertTracksDB: %d/%d persisted, %d failed", okN, len(tracks), failN)
	}
	return okN, failN
}

// upsertTrackBatch 用单事务写入一批曲目：prepare 一次、循环 Exec，
// 最后统一 Commit，把 N 次独立事务（每次 fsync）压成一次。
func upsertTrackBatch(db *sql.DB, s *store, tracks []*model.Track) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO tracks(id,path,data,seq) VALUES(?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET path=excluded.path, data=excluded.data, seq=excluded.seq`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, t := range tracks {
		data, err := marshalTrack(t)
		if err != nil {
			continue // 单条序列化失败：跳过该条，不中断整批
		}
		seq := s.trackSeq(t.ID)
		if _, err := stmt.Exec(t.ID, t.Path, data, seq); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(tracks), nil
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
