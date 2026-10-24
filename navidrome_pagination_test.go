package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestPaginationNDClampsNegativeParameters(t *testing.T) {
	cases := []struct {
		query              string
		wantStart, wantEnd int
	}{
		{"?_start=-1&_end=10", 0, 10},
		{"?start=-7&limit=20", 0, 20},
		{"?offset=5&_end=-9", 5, 105},
		{"?_start=2&_limit=999999", 2, 1002},
		{"?_start=10&_end=4", 10, 110},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/api/song"+tc.query, nil)
		start, end := paginationND(r)
		if start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("%s: got (%d,%d), want (%d,%d)", tc.query, start, end, tc.wantStart, tc.wantEnd)
		}
	}
}

func TestSliceRangeNeverReturnsInvalidBounds(t *testing.T) {
	cases := [][3]int{{0, -1, -1}, {5, -3, 2}, {5, 9, 10}, {5, 4, 2}, {5, 2, -1}}
	for _, c := range cases {
		a, b := sliceRange(c[0], c[1], c[2])
		if a < 0 || b < a || b > c[0] {
			t.Errorf("sliceRange%v = (%d,%d)", c, a, b)
		}
	}
}

func TestListJSONNilDataIsArrayAndEmptyRangeIsValid(t *testing.T) {
	n := &navidromeAPI{}
	r := httptest.NewRequest("GET", "/api/song", nil)
	w := httptest.NewRecorder()
	n.listJSON(w, r, nil, 0)
	var got any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	arr, ok := got.([]any)
	if !ok || len(arr) != 0 {
		t.Fatalf("empty response must be [], got %#v", got)
	}
	if w.Header().Get("Content-Range") != "items */0" {
		t.Fatalf("Content-Range = %q", w.Header().Get("Content-Range"))
	}
}
