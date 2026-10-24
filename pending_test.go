package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // 注册 sqlite3 驱动（纯 Go，无 CGO）

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
)

// newPendingDB 建内存库并初始化低置信度表。
func newPendingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// :memory: 库每连接独立，限单连接保证建表与读写同库。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := ensurePendingTable(db); err != nil {
		t.Fatalf("ensure pending table: %v", err)
	}
	return db
}

func TestEnqueuePending(t *testing.T) {
	db := newPendingDB(t)
	old := activityDB
	activityDB = db
	defer func() { activityDB = old }()

	// 低置信度（40）入队
	tr := &model.Track{Path: "E:\\music\\x.flac", FileName: "x.flac"}
	sr := scrape.SearchResult{Title: "勇气 (2018春晚表演歌曲)", Artists: []string{"派伟俊"}, Album: "勇气", Confidence: 40}
	enqueuePending(tr, "勇气", "梁静茹", sr)

	// 同 path 再入队（覆盖）：置信度/状态刷新为 pending
	sr2 := scrape.SearchResult{Title: "勇气 (Live)", Artists: []string{"其他"}, Album: "", Confidence: 33}
	enqueuePending(tr, "勇气", "梁静茹", sr2)

	var n int
	func() {
		rows, err := db.Query(`SELECT COUNT(*) FROM pending_tracks`)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&n)
		}
	}()
	if n != 1 {
		t.Fatalf("同 path 应覆盖为 1 条，got %d", n)
	}

	// 高置信度不入队
	tr2 := &model.Track{Path: "E:\\music\\y.flac", FileName: "y.flac"}
	enqueuePending(tr2, "勇气", "梁静茹", scrape.SearchResult{Title: "勇气", Confidence: 100})
	var n2 int
	func() {
		rows2, err := db.Query(`SELECT COUNT(*) FROM pending_tracks WHERE path=?`, tr2.Path)
		if err != nil {
			t.Fatalf("count2: %v", err)
		}
		defer rows2.Close()
		if rows2.Next() {
			rows2.Scan(&n2)
		}
	}()
	if n2 != 0 {
		t.Fatalf("高置信度不应入队，got %d", n2)
	}
}

func TestPendingListAndResolve(t *testing.T) {
	db := newPendingDB(t)
	old := activityDB
	activityDB = db
	defer func() { activityDB = old }()

	enqueuePending(&model.Track{Path: "E:\\music\\a.flac", FileName: "a.flac"}, "A", "歌手",
		scrape.SearchResult{Title: "A2", Artists: []string{"歌手2"}, Confidence: 50})

	h := pendingListHandler(db)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/scrape/pending", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"confidence":50`) {
		t.Fatalf("list 应包含置信度 50，body=%s", rr.Body.String())
	}

	// resolve confirm
	res := pendingResolveHandler(db)
	rr2 := httptest.NewRecorder()
	res.ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/scrape/pending/resolve",
		strings.NewReader(`{"id":1,"action":"confirm"}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("resolve status = %d", rr2.Code)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pending_tracks WHERE id=1`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "confirmed" {
		t.Fatalf("resolve 后应 confirmed，got %q", status)
	}

	// 无效请求
	rr3 := httptest.NewRecorder()
	res.ServeHTTP(rr3, httptest.NewRequest(http.MethodPost, "/api/scrape/pending/resolve",
		strings.NewReader(`{}`)))
	if rr3.Code != http.StatusBadRequest {
		t.Fatalf("无效请求应 400，got %d", rr3.Code)
	}
}
