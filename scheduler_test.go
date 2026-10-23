package main

import (
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	cases := []struct {
		expr string
		ok   bool
	}{
		{"0 3 * * *", true},         // 每天 03:00
		{"*/15 * * * *", true},      // 每 15 分钟
		{"30 2 * * 1-5", true},      // 工作日 02:30
		{"0,30 8 * * 0,6", true},    // 周末 08:00 与 08:30
		{"5 4 * * 7", true},         // 周字段 7 = 周日
		{"0 3 * *", false},          // 缺一段
		{"61 3 * * *", true},        // 61 超界但语法可解析（match 永不命中）
		{"a 3 * * *", false},        // 非法值
		{"*/0 3 * * *", false},      // 步进 0
	}
	for _, c := range cases {
		_, err := parseCron(c.expr)
		if (err == nil) != c.ok {
			t.Errorf("parseCron(%q): got err=%v, want ok=%v", c.expr, err, c.ok)
		}
	}
}

func TestCronNextAfter(t *testing.T) {
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)

	// 每 15 分钟：下一个应为 10:15。
	e, err := parseCron("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.nextAfter(base); !got.Equal(time.Date(2026, 10, 22, 10, 15, 0, 0, time.Local)) {
		t.Errorf("*/15: got %v", got)
	}

	// 每天 03:00：从 10:00 起下一个是次日 03:00。
	e, _ = parseCron("0 3 * * *")
	got := e.nextAfter(base)
	want := time.Date(2026, 10, 23, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("0 3 * * *: got %v want %v", got, want)
	}

	// 恰好命中时：应严格大于 after。
	e, _ = parseCron("0 10 * * *")
	got = e.nextAfter(time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local))
	want = time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("0 10 * * * at exact: got %v want %v", got, want)
	}

	// 工作日 02:30：周四 10:00 起 → 周五 02:30（2026-10-22 是周四）。
	e, _ = parseCron("30 2 * * 1-5")
	got = e.nextAfter(base)
	want = time.Date(2026, 10, 23, 2, 30, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("30 2 * * 1-5: got %v want %v", got, want)
	}

	// 周日 7 等价 0：2026-10-25 是周日 00:00。
	e, _ = parseCron("0 0 * * 7")
	got = e.nextAfter(time.Date(2026, 10, 22, 0, 0, 0, 0, time.Local))
	want = time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("0 0 * * 7: got %v want %v", got, want)
	}
}

func TestCronFieldWeekday7(t *testing.T) {
	e, err := parseCron("0 0 * * 7")
	if err != nil {
		t.Fatal(err)
	}
	// 周日（Sunday）对应 time.Weekday()==0，也应命中。
	sun := time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local)
	if !e.match(sun) {
		t.Error("dow=7 should also match Sunday (0)")
	}
}
