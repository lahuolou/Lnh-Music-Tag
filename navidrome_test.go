package main

// Navidrome 兼容端点分页边界测试：
// 覆盖负数/非法参数归零、多参数优先级、end<start、越界、空库响应头。
// 回归目标：外部传入 _start=-1 等值时不得触发切片越界 panic。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cstore "lnh-music-tag/internal/store"
)

func TestPaginationND(t *testing.T) {
	cases := []struct {
		name               string
		qs                 string
		wantStart, wantEnd int
	}{
		{"默认（无参数）", "", 0, 100},
		{"_start/_end 正常", "_start=2&_end=5", 2, 5},
		{"_start 负数归零", "_start=-1&_end=10", 0, 10},
		{"_end 负数归零", "_start=2&_end=-3", 2, 102},
		{"start 回退", "start=3", 3, 103},
		{"offset 回退", "offset=4", 4, 104},
		{"offset 负数归零后按 limit 推算", "offset=-9&limit=5", 0, 5},
		{"_limit 推算", "_start=2&_limit=7", 2, 9},
		{"limit 推算", "start=1&limit=3", 1, 4},
		{"size 推算", "start=1&size=3", 1, 4},
		{"end 小于 start 补默认", "_start=8&_end=2", 8, 108},
		{"非法值归零", "_start=abc&_end=xyz", 0, 100},
		{"超大页大小按上限钳制", "_start=0&_end=999999", 0, 1000},
		{"limit 超大按上限钳制", "_start=2&_limit=999999", 2, 1002},
		{"_start 优先级高于 start", "_start=5&start=1&_end=9", 5, 9},
		{"_end 优先级高于 _limit", "_start=0&_end=9&_limit=3", 0, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/song?"+c.qs, nil)
			s, e := paginationND(req)
			if s != c.wantStart || e != c.wantEnd {
				t.Fatalf("paginationND(%q) = (%d,%d), want (%d,%d)", c.qs, s, e, c.wantStart, c.wantEnd)
			}
		})
	}
}

func TestSliceRange(t *testing.T) {
	cases := []struct{ total, start, end, wantStart, wantEnd int }{
		{10, -1, 5, 0, 5},    // 负数 start 归零
		{10, 2, -3, 2, 2},    // 负数 end 裁到 start
		{10, 2, 1, 2, 2},     // end < start
		{10, 3, 9, 3, 9},     // 正常
		{10, 10, 15, 10, 10}, // start == total
		{10, 11, 15, 10, 10}, // start > total
		{0, 0, 5, 0, 0},      // 空库
		{0, -5, -1, 0, 0},    // 空库 + 负数
		{-1, 3, 5, 0, 0},     // 负 total（防御：任何输入都不产生负切片索引）
		{-1, -3, -1, 0, 0},   // 负 total + 负参数
	}
	for _, c := range cases {
		s, e := sliceRange(c.total, c.start, c.end)
		if s != c.wantStart || e != c.wantEnd {
			t.Fatalf("sliceRange(%d,%d,%d) = (%d,%d), want (%d,%d)", c.total, c.start, c.end, s, e, c.wantStart, c.wantEnd)
		}
	}
}

func TestListJSONContentRange(t *testing.T) {
	cfg, err := cstore.OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	n := &navidromeAPI{library: newStore(nil), cfg: cfg}

	// 空库：Content-Range 必须合法（items */0），body 为 [] 而非 null
	rec := httptest.NewRecorder()
	n.listJSON(rec, httptest.NewRequest(http.MethodGet, "/api/song", nil), nil, 0)
	if got := rec.Header().Get("Content-Range"); got != "items */0" {
		t.Fatalf("空库 Content-Range = %q, want %q", got, "items */0")
	}
	if got := rec.Body.String(); got != "[]\n" {
		t.Fatalf("空库 body = %q, want %q", got, "[]\n")
	}

	// 正常范围：items start-(end-1)/total
	rec = httptest.NewRecorder()
	n.listJSON(rec, httptest.NewRequest(http.MethodGet, "/api/song?_start=1&_end=3", nil), []any{"a", "b"}, 5)
	if got := rec.Header().Get("Content-Range"); got != "items 1-2/5" {
		t.Fatalf("Content-Range = %q, want %q", got, "items 1-2/5")
	}

	// 负数 start：归零后给出合法范围，绝不产生 items 0--1/x
	rec = httptest.NewRecorder()
	n.listJSON(rec, httptest.NewRequest(http.MethodGet, "/api/song?_start=-1&_end=2", nil), []any{"a", "b"}, 2)
	if got := rec.Header().Get("Content-Range"); got != "items 0-1/2" {
		t.Fatalf("负数 Content-Range = %q, want %q", got, "items 0-1/2")
	}
}
