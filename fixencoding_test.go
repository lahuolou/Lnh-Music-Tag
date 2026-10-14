package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"lnh-musictag/internal/model"
	"lnh-musictag/internal/taglibx"
)

func TestFixMojibake(t *testing.T) {
	// 已是正常中文 → 保持不动
	if got := fixMojibake("你好世界"); got != "你好世界" {
		t.Fatalf("正常中文被改动: %q", got)
	}
	// 情形 A：UTF-8 字节被按 GBK 读出（如"浣犲ソ"）→ 应还原为"你好"
	mojiA, _ := simplifiedchinese.GBK.NewDecoder().Bytes([]byte("你好"))
	if got := fixMojibake(string(mojiA)); got != "你好" {
		t.Fatalf("情形A失败: 输入%q 得到 %q", string(mojiA), got)
	}
	// 情形 B：GBK 字节被按 UTF-8 读出（如"ÄãºÃ"）→ 应还原为"你好"
	gbkBytes, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("你好"))
	if got := fixMojibake(string(gbkBytes)); got != "你好" {
		t.Fatalf("情形B失败: 输入%q 得到 %q", string(gbkBytes), got)
	}
	// 情形 C：UTF-8 字节被按 Latin-1 读出（如"ä½ å¥½"）→ 应还原为"你好"
	latin := ""
	for _, x := range []byte("你好") {
		latin += string(rune(x))
	}
	if got := fixMojibake(latin); got != "你好" {
		t.Fatalf("情形C失败: 输入%q 得到 %q", latin, got)
	}
}

func TestScriptConverter(t *testing.T) {
	trad := scriptConverter("trad")("广岛之恋 演唱会")
	if trad != "廣島之戀 演唱會" {
		t.Fatalf("简→繁失败: %q", trad)
	}
	simp := scriptConverter("simp")("廣島之戀 演唱會")
	if simp != "广岛之恋 演唱会" {
		t.Fatalf("繁→简失败: %q", simp)
	}
}

func TestFixMojibakeDoubleCorrupt(t *testing.T) {
	// 双重损坏不应把串改得更糟：输入保持原样或变得更接近中文
	bad := "姒荤◈锇ㄣ储锟◈妞圭粮簆◈控权塲ジ锟◈濮栴◈锟斤拷妤f控铍勤"
	got := fixMojibake(bad)
	if cjkCount(got) == 0 {
		t.Fatalf("修复结果无汉字: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("修复结果含 U+FFFD: %q", got)
	}
}

func TestLooksGarbled(t *testing.T) {
	cases := map[string]bool{
		"姒荤◈锇ㄣ储锟◈妞圭粮簆◈控权塲ジ锟◈濮栴◈锟斤拷妤f控铍勤": true, // 用户实拍：含 ◈/ㄣ/锟斤拷
		"蓝色蝴蝶":        false,
		"周杰伦 七里香":     false,
		"hello world": false,
		"��测试":        true, // U+FFFD
	}
	for in, want := range cases {
		if got := looksGarbled(in); got != want {
			t.Fatalf("looksGarbled(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseFileNameArtistTitle(t *testing.T) {
	cases := map[string][2]string{
		"Unknown - 蓝色蝴蝶.mp3":   {"Unknown", "蓝色蝴蝶"},
		"陈慧娴 - 傻女.flac":        {"陈慧娴", "傻女"},
		"发如雪.mp3":              {"", "发如雪"},
		"周杰伦 - 七里香 - live.mp3": {"周杰伦", "七里香 - live"},
		"廣島之戀 - 演唱會.flac":      {"廣島之戀", "演唱會"},
	}
	for in, want := range cases {
		a, tt := parseFileNameArtistTitle(in)
		if a != want[0] || tt != want[1] {
			t.Fatalf("parseFileNameArtistTitle(%q) = (%q,%q), want (%q,%q)", in, a, tt, want[0], want[1])
		}
	}
}

func TestEffectiveSearchTerms(t *testing.T) {
	mk := func(title, artist, fn string) *model.Track {
		return &model.Track{Tags: map[string]string{"TITLE": title, "ARTIST": artist}, FileName: fn}
	}
	cases := []struct {
		in    *model.Track
		wantT string
		wantA string
	}{
		{mk("蓝色蝴蝶", "Unknown", "Unknown - 蓝色蝴蝶.mp3"), "蓝色蝴蝶", ""},
		{mk("", "", "陈慧娴 - 傻女.flac"), "傻女", "陈慧娴"},
		{mk("七里香", "周杰伦", "周杰伦 - 七里香.flac"), "七里香", "周杰伦"},
		{mk("姒荤◈锟斤拷", "未知", "蓝色蝴蝶.mp3"), "蓝色蝴蝶", ""},
		{mk("", "", "发如雪.mp3"), "发如雪", ""},
	}
	for _, c := range cases {
		tit, art := effectiveSearchTerms(c.in)
		if tit != c.wantT || art != c.wantA {
			t.Fatalf("effectiveSearchTerms(%q) = (%q,%q), want (%q,%q)", c.in.FileName, tit, art, c.wantT, c.wantA)
		}
	}
}

func TestDetectTraditional(t *testing.T) {
	if !detectTraditional(map[string]string{"TITLE": "陳慧嫻"}) {
		t.Fatal("繁体标签未检出")
	}
	if !detectTraditional(map[string]string{"ARTIST": "張學友"}) {
		t.Fatal("繁体艺术家未检出")
	}
	if detectTraditional(map[string]string{"TITLE": "陈慧娴"}) {
		t.Fatal("简体标签误判为繁体")
	}
	if detectTraditional(map[string]string{"TITLE": "周杰伦", "ALBUM": "七里香"}) {
		t.Fatal("纯简体专辑误判为繁体")
	}
}

func TestFixEncodingHandlerFilenameFallback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg 不可用，跳过集成测试")
	}
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "Unknown - 蓝色蝴蝶.m4a")
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.2", "-c:a", "aac", "-b:a", "64k", mp3)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 生成测试文件失败: %v %s", err, out)
	}
	// 写入模拟的严重乱码标签（无法自动还原的多级损坏）
	bad := "姒荤◈锇ㄣ储锟◈妞圭粮簆◈控权塲ジ锟◈濮栴◈锟斤拷妤f控铍勤"
	if err := taglibx.WriteTags(mp3, map[string][]string{
		taglibx.Title:  {bad},
		taglibx.Artist: {"濞肩◈锜τ綾澪"},
		taglibx.Album:  {"濞肩◈锜τ綾澪炒娓濮栴锟◈错愦薜澪"},
	}, false); err != nil {
		t.Fatalf("写入乱码标签失败: %v", err)
	}
	tr, err := taglibx.ReadTrack(mp3)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	s := newStore(nil)
	s.add(tr)

	js := newJobStore(nil)
	body, _ := json.Marshal(map[string]any{"ids": []string{tr.ID}})
	req := httptest.NewRequest(http.MethodPost, "/api/fix-encoding", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	fixEncodingHandler(js, s)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handler 状态码 %d", w.Code)
	}
	// job 模式：POST 返回 jobId，结果异步在 job 上
	var jr struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &jr); err != nil || jr.JobID == "" {
		t.Fatalf("未返回 jobId: %v body=%s", err, w.Body.String())
	}
	job := js.get(jr.JobID)
	if job == nil {
		t.Fatal("job 不存在")
	}
	// job 异步执行：轮询等待完成
	deadline := time.Now().Add(5 * time.Second)
	for {
		job.mu.Lock()
		done := job.Status == "done"
		n := len(job.Results)
		job.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job 超时未完成，结果数 %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(job.Results) != 1 {
		t.Fatalf("结果数 %d, want 1", len(job.Results))
	}
	if !job.Results[0].OK {
		t.Fatalf("乱码修复未成功: %+v", job.Results[0])
	}
	// 写回后文件标签应已可读（下面断言标题/艺术家已还原）
	tr2, err := taglibx.ReadTrack(mp3)
	if err != nil {
		t.Fatalf("修复后重读失败: %v", err)
	}
	if tr2.Tags[taglibx.Title] != "蓝色蝴蝶" {
		t.Fatalf("文件内标题未修复: %q", tr2.Tags[taglibx.Title])
	}
}
