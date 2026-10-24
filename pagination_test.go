package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// ---- 解析层表驱动测试（契约：负数 clamp、上限 1000、非法按 0、多参数优先级）----

func TestClampStartLimit(t *testing.T) {
	cases := []struct {
		name         string
		start, limit int
		wantStart    int
		wantLimit    int
	}{
		{"正常", 0, 100, 0, 100},
		{"负起始归零", -5, 100, 0, 100},
		{"负页大小保留为不限制", 0, -1, 0, -1},
		{"零页大小保留为不限制", 0, 0, 0, 0},
		{"超大页截断到上限", 10, 5000, 10, maxPageSize},
		{"恰好上限保留", 0, maxPageSize, 0, maxPageSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, l := clampStartLimit(c.start, c.limit)
			if s != c.wantStart || l != c.wantLimit {
				t.Fatalf("clampStartLimit(%d,%d) = (%d,%d), want (%d,%d)",
					c.start, c.limit, s, l, c.wantStart, c.wantLimit)
			}
		})
	}
}

func TestParseStartLimit(t *testing.T) {
	mk := func(qs string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/Items?"+qs, nil)
	}
	cases := []struct {
		name      string
		query     string
		startKeys []string
		limitKeys []string
		wantStart int
		wantLimit int
	}{
		{"缺省全 0", "", []string{"StartIndex"}, []string{"Limit"}, 0, 0},
		{"正常参数", "StartIndex=20&Limit=50", []string{"StartIndex"}, []string{"Limit"}, 20, 50},
		{"非法字符串按 0", "StartIndex=abc&Limit=xyz", []string{"StartIndex"}, []string{"Limit"}, 0, 0},
		{"负起始归零", "StartIndex=-3&Limit=10", []string{"StartIndex"}, []string{"Limit"}, 0, 10},
		{"超大页截断", "StartIndex=0&Limit=99999", []string{"StartIndex"}, []string{"Limit"}, 0, maxPageSize},
		{"多拼写取首个合法", "startIndex=7&StartIndex=9", []string{"StartIndex", "startIndex"}, []string{"Limit"}, 9, 0},
		{"limit 拼写回退", "StartIndex=1&size=25", []string{"StartIndex"}, []string{"Limit", "size"}, 1, 25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, l := parseStartLimit(mk(c.query), c.startKeys, c.limitKeys)
			if s != c.wantStart || l != c.wantLimit {
				t.Fatalf("parseStartLimit(%q) = (%d,%d), want (%d,%d)",
					c.query, s, l, c.wantStart, c.wantLimit)
			}
		})
	}
}

func TestParseLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/rest/albumList?Limit=2000", nil)
	if got := parseLimit(r, []string{"Limit", "limit"}); got != maxPageSize {
		t.Fatalf("parseLimit(2000) = %d, want %d", got, maxPageSize)
	}
	r = httptest.NewRequest(http.MethodGet, "/rest/albumList?Limit=bad", nil)
	if got := parseLimit(r, []string{"Limit", "limit"}); got != 0 {
		t.Fatalf("parseLimit(bad) = %d, want 0", got)
	}
}

// ---- 集成：pageItems（Subsonic/Jellyfin 列表出口）边界行为 ----

func TestPageItemsBoundaries(t *testing.T) {
	items := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, i)
	}
	// 空列表必须为 []（非 null），且不 panic
	empty := pageItems(nil, httptest.NewRequest(http.MethodGet, "/Items?StartIndex=0&Limit=10", nil))
	if v, ok := empty["Items"].([]any); !ok || v == nil || len(v) != 0 {
		t.Fatalf("空列表应为 []any{}，got %#v", empty["Items"])
	}
	// start 超过总数 → clamp 到 total，返回空页（不 panic）
	over := pageItems(items, httptest.NewRequest(http.MethodGet, "/Items?StartIndex=99&Limit=10", nil))
	got := over["Items"].([]any)
	if len(got) != 0 {
		t.Fatalf("start>total 应返回空页，got %d 项", len(got))
	}
	if tr := over["TotalRecordCount"].(int); tr != 10 {
		t.Fatalf("total 应保持 10，got %d", tr)
	}
	// 超大 Limit 截断
	huge := pageItems(items, httptest.NewRequest(http.MethodGet, "/Items?StartIndex=0&Limit=50000", nil))
	got = huge["Items"].([]any)
	if len(got) != 10 {
		t.Fatalf("超大 limit 应返回全部 10 项（截断到上限后再与总数取小），got %d", len(got))
	}
	// 正常分页切片
	page := pageItems(items, httptest.NewRequest(http.MethodGet, "/Items?StartIndex=5&Limit=3", nil))
	got = page["Items"].([]any)
	if !reflect.DeepEqual(got, []any{5, 6, 7}) {
		t.Fatalf("分页切片错误: %#v", got)
	}
	// 多参数优先级：StartIndex 优先于 startIndex
	pri := pageItems(items, httptest.NewRequest(http.MethodGet, "/Items?startIndex=7&StartIndex=2&Limit=1", nil))
	got = pri["Items"].([]any)
	if !reflect.DeepEqual(got, []any{2}) {
		t.Fatalf("参数优先级错误: %#v", got)
	}
}
