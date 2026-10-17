package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/taglibx"
)

func TestJobSnapshotRestoreAndDuration(t *testing.T) {
	// 使用临时文件库而非 :memory:：jobStore 的多连接（restore/autoPersist/get）
	// 在 :memory: 下各自独立导致跨连接不可见，文件库与生产行为一致。
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	js := newJobStore(db)
	j := js.create(3, "fixEnc")
	// 快照应带创建/更新时间
	p := j.snapshot()
	if p.CreatedAt == "" || p.UpdatedAt == "" {
		t.Fatalf("snapshot should carry created/updated, got %+v", p)
	}
	if p.CreatedAt != p.UpdatedAt {
		t.Fatalf("fresh task created == updated, got %q vs %q", p.CreatedAt, p.UpdatedAt)
	}
	// 重启恢复：新 jobStore 从库回填 CreatedAt，进度响应含 duration
	js2 := newJobStore(db)
	restored := js2.get(j.ID)
	if restored == nil {
		t.Fatal("restored job missing")
	}
	if restored.CreatedAt.IsZero() {
		t.Fatal("restore should backfill CreatedAt from DB")
	}
	h := jobProgressHandler(js2)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+j.ID, nil)
	req.SetPathValue("id", j.ID)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("progress status = %d", rec.Code)
	}
	var out scrapeJob
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CreatedAt.IsZero() {
		t.Fatal("progress should carry createdAt")
	}
	if out.Duration <= 0 {
		t.Fatalf("progress should carry duration>0, got %d", out.Duration)
	}
	// 30 天前的终态任务应被保留策略清理（restore 末尾 DELETE）
	db.Exec(`UPDATE jobs SET status='done', updated_at=? WHERE id=?`,
		time.Now().AddDate(0, 0, -31).Format(time.RFC3339), j.ID)
	newJobStore(db)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE id=?`, j.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("30-day-old finished job should be pruned, n=%d err=%v", n, err)
	}
}

func TestFieldsFromList(t *testing.T) {
	f := fieldsFromList([]string{"cover", "歌名", "流派"}, false, false)
	if !f.Cover || !f.Title || !f.Genre {
		t.Fatalf("expected cover/title/genre, got %+v", f)
	}
	if f.Artist || f.Lyrics || f.Year || f.AlbumArtist || f.TrackNumber || f.Album {
		t.Fatalf("unexpected fields enabled: %+v", f)
	}
	// empty list -> all metadata + legacy fetch flags
	f2 := fieldsFromList(nil, true, true)
	if !f2.Title || !f2.Artist || !f2.Album || !f2.Cover || !f2.Lyrics || !f2.Genre || !f2.Year || !f2.AlbumArtist || !f2.TrackNumber {
		t.Fatalf("empty list should default to all, got %+v", f2)
	}
	// empty list + no fetch -> all metadata but no cover/lyrics
	f3 := fieldsFromList(nil, false, false)
	if f3.Cover || f3.Lyrics {
		t.Fatalf("no cover/lyrics when fetch flags off: %+v", f3)
	}
}

func TestApplySkipFilled(t *testing.T) {
	full := &model.Track{Tags: map[string]string{
		taglibx.Title: "x", taglibx.Artist: "x", taglibx.Album: "x",
		taglibx.AlbumArtist: "x", taglibx.Genre: "x", taglibx.Date: "2020",
		taglibx.TrackNumber: "1",
		taglibx.Composer:    "x", taglibx.Lyricist: "x", taglibx.ReleaseType: "x",
		taglibx.Language: "x", taglibx.Comment: "x", taglibx.DiscNumber: "1",
		taglibx.DiscTotal: "1", taglibx.TrackTotal: "1",
		taglibx.ReplayGainTrackGain: "1", taglibx.ReplayGainAlbumGain: "1",
		taglibx.ReplayGainTrackPeak: "1", taglibx.ReplayGainAlbumPeak: "1",
		taglibx.Bitrate: "320", taglibx.SampleRate: "44100",
		taglibx.BitDepth: "16", taglibx.Channels: "2",
		taglibx.Length: "300", taglibx.FileSize: "12345",
	}}
	full.HasCover = true
	// all metadata filled + cover -> nothing to do
	f := allMetadataFields()
	f.Cover = true
	if applySkipFilled(full, &f) {
		t.Fatal("all filled should return no work")
	}
	// cover missing -> work needed and cover stays enabled
	full.HasCover = false
	f2 := ScrapeFields{Cover: true, Title: true, Artist: true}
	if !applySkipFilled(full, &f2) {
		t.Fatal("cover missing should need work")
	}
	if !f2.Cover {
		t.Fatal("cover should remain enabled")
	}
	if f2.Title || f2.Artist {
		t.Fatal("filled title/artist should be dropped")
	}
	// lyrics missing -> work needed
	f3 := ScrapeFields{Lyrics: true}
	if !applySkipFilled(full, &f3) {
		t.Fatal("lyrics missing should need work")
	}
}
