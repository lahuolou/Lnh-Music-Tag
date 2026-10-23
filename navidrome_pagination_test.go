package main

import (
	"net/http/httptest"
	"testing"
)

func TestPaginationNDBounds(t *testing.T) {
	tests := []struct {
		name, query string
		start, end  int
	}{
		{name: "negative start", query: "?_start=-1&_end=10", start: 0, end: 10},
		{name: "negative end defaults safely", query: "?_start=2&_end=-1", start: 2, end: 102},
		{name: "negative offset alias", query: "?offset=-9&limit=5", start: 0, end: 5},
		{name: "normal pagination", query: "?_start=10&_end=20", start: 10, end: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/song"+tt.query, nil)
			start, end := paginationND(r)
			start, end = sliceRange(200, start, end)
			if start != tt.start || end != tt.end {
				t.Fatalf("range=(%d,%d), want (%d,%d)", start, end, tt.start, tt.end)
			}
		})
	}
}

func TestSliceRangeClampsAllBounds(t *testing.T) {
	tests := []struct {
		name                                  string
		total, start, end, wantStart, wantEnd int
	}{
		{"negative values", 10, -3, -1, 0, 0},
		{"start beyond total", 10, 99, 100, 10, 10},
		{"end before start", 10, 7, 2, 7, 7},
		{"empty collection", 0, -1, 10, 0, 0},
		{"negative total", -1, 3, 5, 0, 0},
		{"ordinary range", 10, 2, 5, 2, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := sliceRange(tt.total, tt.start, tt.end)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("sliceRange(%d,%d,%d)=(%d,%d), want (%d,%d)", tt.total, tt.start, tt.end, start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestNDListJSONEmptyContentRange(t *testing.T) {
	n := &navidromeAPI{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/song?_start=-4&_end=5", nil)
	n.listJSON(w, r, []any{}, 0)
	if got := w.Header().Get("Content-Range"); got != "items */0" {
		t.Fatalf("empty Content-Range=%q, want %q", got, "items */0")
	}
	if got := w.Header().Get("x-total-count"); got != "0" {
		t.Fatalf("x-total-count=%q, want 0", got)
	}
}
