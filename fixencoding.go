// 乱码修复：针对中文标签常见的 GBK/UTF-8 编码错乱，把文本字段中的乱码还原。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/taglibx"
)

// textFields 是需要承载人读文本、参与乱码修复 / 简繁转换的标签字段。
// 覆盖全部可编辑文本元素（29 元素中的文本型字段）：歌名/艺术家/专辑/
// 专辑艺术家/风格/作曲家/作词/简介/歌词/语言/专辑类型。
// 数值与属性键（曲目号/年代/增益/比特率等）不参与——无文本可转。
var textFields = []string{
	taglibx.Title, taglibx.Artist, taglibx.Album, taglibx.AlbumArtist,
	taglibx.Genre, taglibx.Composer, taglibx.Lyricist, taglibx.Comment,
	taglibx.Lyrics, taglibx.Language, taglibx.ReleaseType,
}

// resolveTracks converts an id list into the store's tracks; an empty list
// means all tracks.
func resolveTracks(s *store, ids []string) []*model.Track {
	if len(ids) == 0 {
		return s.all()
	}
	out := []*model.Track{}
	for _, id := range ids {
		if t := s.get(id); t != nil {
			out = append(out, t)
		}
	}
	return out
}

// cjkCount returns how many CJK (Han) characters are in s.
func cjkCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

// rareHanCount 统计生僻汉字数量：不在 GBK 字符集、或 GBK 码位不在 GB2312
// 一级区（首字节 0xB0–0xD7）的字视为生僻。GBK/UTF-8 误读产生的"误读字"大多
// 落在二级区/扩展区，而正常歌曲标题/歌手名基本用一级常用字——这是区分"正常中文"
// 与"误读中文"最可靠的廉价信号（纯按 CJK 数量评分会把误读当正常）。
var rareCache sync.Map // rune -> int（1=生僻）

func rareHanCount(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			continue
		}
		if v, ok := rareCache.Load(r); ok {
			n += v.(int)
			continue
		}
		rare := 1
		if b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(string(r))); err == nil && len(b) == 2 && b[0] >= 0xB0 && b[0] <= 0xD7 {
			rare = 0 // GB2312 一级区（常用字）
		}
		rareCache.Store(r, rare)
		n += rare
	}
	return n
}

// rareHanSignal 生僻字信号：一段文本里 ≥3 个汉字且**超过一半**是生僻字 →
// 极可能是编码误读（正常歌名/歌手名不会大量使用 GB2312 常用区之外的罕见字）。
// 用于捕获"全 CJK 型误读"——它们没有可还原的编码链（信息已丢失），只能靠
// 检测筛出、再走刮削/识别补全。
// 注意：
//   - ≥3 而不是 ≥2——GB2312 常用区之外还有大量"繁体常用字"（陳慧嫻/龍/鳳
//     等），阈值过低会把正常繁体标签误判成乱码（真实库验证过）。
//   - 严格 >50% 而不是 ≥50%——正常歌名可能恰好一半含生僻（如"陳志朋 - 红蜻蜓
//     .flac"：紅/蜻/蜓 3 个生僻对 6 个汉字恰 50%），≥50% 会把正常曲目误标
//     乱码；而信息丢失型乱码（"浜洪潛鋆澞"）生僻占比普遍 60% 以上。
func rareHanSignal(s string) bool {
	cjk, rare := 0, 0
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			continue
		}
		cjk++
		if rareHanCount(string(r)) > 0 {
			rare++
		}
	}
	return cjk >= 3 && rare >= 3 && rare*2 > cjk
}

// fixMojibake attempts to repair garbled Chinese tags caused by GBK/UTF-8
// encoding mixups. Each candidate is scored by how much it looks like clean
// common Chinese: more common (GB2312 level-1) Han, fewer non-Han / kana /
// rare characters. Mojibake output is full of rare characters, so clean text
// is left untouched while genuine corruption is rewritten. Candidates that
// cannot be encoded back to GBK (e.g. contain Cyrillic) are rejected. It also
// tries double-pass chains (e.g. GBK->UTF8->GBK->UTF8) that produce the
// classic "锟斤拷" corruption.
func fixMojibake(s string) string {
	if s == "" {
		return s
	}
	score := func(c string) int {
		total := utf8.RuneCountInString(c)
		cj := cjkCount(c)
		kana := 0
		for _, r := range c {
			if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
				kana++
			}
		}
		rare := rareHanCount(c)
		return cj*3 - (total-cj)*3 - kana*5 - rare*10
	}

	// 候选统一走得分机制：正常中文得分最高，乱码/生僻/非中文字符扣分。
	best, bestScore := s, score(s)
	try := func(c string) {
		if c == "" || c == s || !utf8.ValidString(c) || strings.ContainsRune(c, '\uFFFD') {
			return
		}
		// 候选含 GBK 之外的字符（如西里尔字母）→ 不可信，丢弃
		if _, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(c)); err != nil {
			return
		}
		if sc := score(c); sc > bestScore {
			best, bestScore = c, sc
		}
	}

	// 字符串全在 Latin-1 范围：很可能是 UTF-8/GBK 字节被按 Latin-1 读出。
	if latin, ok := latinBytesOf(s); ok {
		// 情形 C：字节即原始 UTF-8，重组解码（作为候选参与比分——GBK 字节
		// 恰好构成合法 UTF-8 序列时重组会得到错误汉字串，靠得分机制纠正）
		if utf8.Valid(latin) {
			try(string(latin))
		}
		// 情形 B：字节是 GBK，按 GBK 解码
		if b, err := simplifiedchinese.GBK.NewDecoder().Bytes(latin); err == nil {
			try(string(b))
		}
		return best
	}

	// 情形 A：UTF-8 字节被按 GBK 读出（"浣犲ソ"）→ 再编码为 GBK、按 UTF-8 解码
	if b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)); err == nil && utf8.Valid(b) {
		try(string(b))
	}
	// 情形 B：GBK 字节被按 UTF-8 读出 → 按 GBK 解码
	if b, err := simplifiedchinese.GBK.NewDecoder().Bytes([]byte(s)); err == nil {
		try(string(b))
	}
	// 双重链：s -> GBK 编码 -> UTF-8 解码（串含 FFFD）-> 再 GBK 编码 -> UTF-8 解码
	// 覆盖"锟斤拷"类二次损坏（中间有替换字符丢失，能还原的大部分仍可救回）。
	if b1, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)); err == nil {
		if mid, err2 := simplifiedchinese.GBK.NewDecoder().Bytes(b1); err2 == nil {
			if b2, err3 := simplifiedchinese.GBK.NewEncoder().Bytes(mid); err3 == nil {
				if out, err4 := simplifiedchinese.GBK.NewDecoder().Bytes(b2); err4 == nil {
					try(string(out))
				}
			}
		}
	}
	return best
}

// latinBytesOf returns s's runes as raw bytes when every rune is in 0x00-0xFF
// (i.e. the string looks Latin-1), plus ok=false otherwise.
func latinBytesOf(s string) ([]byte, bool) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return nil, false
		}
		out = append(out, byte(r))
	}
	return out, true
}

// looksGarbled reports whether a text field shows typical mojibake markers:
// replacement chars, Latin-1 misread characters, bopomofo or geometric symbols
// that never appear in a clean Chinese tag.
func looksGarbled(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsRune(s, '\uFFFD') {
		return true
	}
	for _, r := range s {
		// 注音符号 U+3100–312F（ㄅㄆㄇ…）
		if r >= 0x3100 && r <= 0x312F {
			return true
		}
		// 几何图形 U+25A0–25FF（◈★▣）与杂项符号 U+2600–26FF（☀♫）
		if (r >= 0x25A0 && r <= 0x25FF) || (r >= 0x2600 && r <= 0x26FF) {
			return true
		}
		if r >= 0x80 && r <= 0xA0 { // Latin-1 控制/符号区（Ã/â 等误读特征）
			return true
		}
	}
	return false
}

// garbledCache 缓存 fixMojibake 的判定结果：同一字符串反复出现（曲目标题
// 重复、列表反复刷新）时避免重复做 GBK/UTF-8 编解码链，列表接口动态重算
// 乱码标记时不拖慢响应。
var garbledCache sync.Map // string -> bool

// looksGarbledText 统一判定"乱码文本"：命中乱码特征标记（FFFD/注音/符号/
// Latin-1 误读区），或大量生僻字（全 CJK 型误读，编码信息已丢失），或
// （≥4 字时）fixMojibake 能还原（GBK/UTF-8 错读但字节信息仍在）。
// 智能跳过、乱码筛选、文件名修复、编辑页回退共用同一套口径。
// 注意：fix 还原只对 ≥4 字的文本启用——短串（2-3 个常用汉字，如"许嵩"）
// 的 GBK 字节恰好构成合法 UTF-8 序列时会被误"还原"成另一个中文串
// （"许嵩"→"璁稿旦"），真实库验证过会把正常标签误判成乱码。
func looksGarbledText(s string) bool {
	if s == "" {
		return false
	}
	if v, ok := garbledCache.Load(s); ok {
		return v.(bool)
	}
	b := looksGarbled(s) || rareHanSignal(s)
	if !b && utf8.RuneCountInString(s) >= 4 {
		b = fixMojibake(s) != s
	}
	garbledCache.Store(s, b)
	return b
}

// looksGarbledTags reports whether any of a track's visible text fields
// (filename + title/artist/album/albumartist tags) is mojibake. It combines
// the cheap marker check with "could be repaired by fixMojibake", so GBK/UTF-8
// misread CJK sequences that are still valid Han characters are also caught.
// Used at scan time to set Track.Garbled (the "乱码" filter then works server-side).
func looksGarbledTags(t *model.Track) bool {
	fields := []string{
		t.FileName,
		t.Tags["TITLE"], t.Tags["ARTIST"],
		t.Tags["ALBUM"], t.Tags["ALBUMARTIST"],
	}
	for _, s := range fields {
		if s == "" {
			continue
		}
		if looksGarbledText(s) {
			return true
		}
	}
	return false
}

// parseFileNameArtistTitle splits "艺术家 - 标题.ext" from a filename, falling
// back to (base, "") when no separator is present. Used to recover readable
// artist/title from an unrecoverable garbled tag.
func parseFileNameArtistTitle(fn string) (artist, title string) {
	base := fn
	for {
		ext := strings.ToLower(filepath.Ext(base))
		if ext == "" || !model.AudioExts[ext] {
			break
		}
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.TrimSpace(base)
	// 文件名规范为「艺术家 - 歌曲标题」，标题本身可含“-”，因此按首个分隔符拆分
	for _, sep := range []string{" - ", "—", "–", "-"} {
		if i := strings.Index(base, sep); i > 0 {
			a := strings.TrimSpace(base[:i])
			t := strings.TrimSpace(base[i+len(sep):])
			if a != "" && t != "" {
				return a, t
			}
		}
	}
	return "", base
}

// looksUnknown reports whether a tag value is a placeholder that carries no
// real metadata ("Unknown", "未知", 空值等)。刮削搜索时这些值没有检索价值。
func looksUnknown(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	switch strings.ToLower(s) {
	case "unknown", "unknown artist", "unknown artists", "未知", "未知艺术家", "未知歌手", "<unknown>", "n/a", "none":
		return true
	}
	return false
}

// effectiveSearchTerms returns the title/artist that should be used to search
// for a track: real tag values when present, otherwise values parsed from the
// filename. This lets files like "Unknown - 蓝色蝴蝶.mp3" (placeholder artist,
// clean title) still be scraped by their actual song name. If a tag is empty,
// "Unknown" or clearly garbled, the filename wins.
func effectiveSearchTerms(t *model.Track) (title, artist string) {
	title = strings.TrimSpace(t.Tags["TITLE"])
	artist = strings.TrimSpace(t.Tags["ARTIST"])
	fa, ft := parseFileNameArtistTitle(t.FileName) // 返回 (artist, title)
	// 文件名解析出的标题/艺术家更可信：标签缺失、占位、乱码或与文件名
	// 不一致时一律优先文件名。典型场景 "Unknown - 蓝色蝴蝶.mp3"：标签可能
	// 残留错误的艺人/标题，用文件名歌名去刮削才能匹配到正确的歌。
	if ft != "" && (title == "" || looksUnknown(title) || looksGarbled(title) || title != ft) {
		title = ft
	}
	if artist == "" || looksUnknown(artist) || looksGarbled(artist) {
		// 标签艺术家为空/占位（Unknown/未知）/乱码：尝试从文件名取真实
		// 艺术家；文件名也只有占位或为空时置空，仅凭歌名去匹配
		// （如 "Unknown - 蓝色蝴蝶.mp3" → 搜 "蓝色蝴蝶"）。
		if fa != "" && !looksGarbled(fa) && !looksUnknown(fa) {
			artist = fa
		} else {
			artist = ""
		}
	}
	return title, artist
}

// repairTrackText 是"乱码修复"的单曲公共逻辑（批量页 fixEncodingHandler 与
// 一键流水线 stageFixEncoding 复用同一实现）：
//  1. 修复文本标签中的乱码；有明确乱码特征的标题/艺术家回退文件名解析结果；
//  2. 修复结果统一转简体（与音频识别一致，整理后的文本应为简体）；
//  3. 计算乱码文件名的修复目标基名（不实际写盘，重命名由调用方执行）。
//
// 返回：修复的字段集合、待写标签表、文件名修复后的新基名（空=无需重命名）。
func repairTrackText(t *model.Track) (changed map[string]string, m map[string][]string, newBase string) {
	changed = map[string]string{}
	m = map[string][]string{}
	for _, k := range textFields {
		v := strings.TrimSpace(t.Tags[k])
		if v == "" {
			continue
		}
		f := fixMojibake(v)
		// 有明确乱码特征（◈/注音/FFFD/Latin-1 控制区）的字段：文件名解析结果
		// 远比"尽力修复"可信，标题/艺术家直接回退文件名；其他字段仍尽力修复。
		if looksGarbled(v) && (k == taglibx.Title || k == taglibx.Artist) {
			fArtist, fTitle := parseFileNameArtistTitle(t.FileName)
			if k == taglibx.Title && fTitle != "" {
				f = fTitle
			} else if k == taglibx.Artist && fArtist != "" {
				f = fArtist
			}
			if f == v { // 文件名解析不出有效值，退回尽力修复
				f = fixMojibake(v)
			}
		}
		// 修复结果仍含替换符/乱码特征时丢弃，避免以乱易乱；统一转简体
		if f != v && !strings.ContainsRune(f, '\uFFFD') && !looksGarbled(f) {
			f = simpConverter(f)
			if f != v {
				changed[k] = f
				m[k] = []string{f}
			}
		}
	}
	// 文件名乱码 → 计算修复目标基名（列表显示的就是文件名，不修不会消失）
	base := stripAudioExt(t.FileName)
	if looksGarbledText(base) {
		if f := fixMojibake(base); f != "" && f != base && !strings.ContainsRune(f, '\uFFFD') && !looksGarbled(f) {
			newBase = simpConverter(f)
		}
	}
	return changed, m, newBase
}

// fixEncodingHandler rewrites the text fields of selected tracks, repairing
// any mojibake it can detect. When a tag cannot be repaired (double-corrupted,
// information lost) but the filename carries a clean "artist - title", the
// artist/title tags fall back to the filename so the track stays readable.
// Repaired text is converted to Simplified Chinese (same as identification).
// Runs as a background job for live progress.
func fixEncodingHandler(js *jobStore, s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		list := resolveTracks(s, req.IDs)
		if len(list) == 0 {
			writeErr(w, http.StatusBadRequest, "请至少勾选一首曲目")
			return
		}
		job := js.runProgressJob("fixEnc", len(list), func(j *scrapeJob, i int) scrapeResult {
			t := list[i]
			j.mu.Lock()
			j.Current = t.FileName
			j.mu.Unlock()
			changed, m, newBase := repairTrackText(t)
			// 先写标签（写入当前路径；若有重命名，标签先写好再移动文件）
			if len(m) > 0 {
				if err := taglibx.WriteTags(t.Path, m, false); err != nil {
					return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "写标签失败: " + err.Error()}
				}
			}
			// 文件名乱码修复并重命名
			renamed := ""
			if newBase != "" {
				ext := t.Ext
				if ext == "" {
					ext = filepath.Ext(t.FileName)
				}
				newPath := filepath.Join(filepath.Dir(t.Path), newBase+ext)
				if newPath != t.Path && !fileExists(newPath) {
					if err := os.Rename(t.Path, newPath); err == nil {
						renamed = newBase
						// renameTrack 更新 store 路径 + 重读新路径标签 + 落库，
						// 保持原 ID（避免路径变化产生重复行）
						renameTrack(s, t, newPath)
					}
				}
			}
			if renamed != "" {
				parts := []string{}
				if len(changed) > 0 {
					parts = append(parts, fmt.Sprintf("修复 %d 个字段", len(changed)))
				}
				parts = append(parts, "文件名→"+renamed)
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Message: strings.Join(parts, "，")}
			}
			if len(m) > 0 {
				refreshTrack(s, t.Path)
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Message: fmt.Sprintf("修复 %d 个字段", len(changed))}
			}
			// 无乱码/无可修复内容：中性跳过，不算失败、不标红
			return scrapeResult{ID: t.ID, FileName: t.FileName, Skip: true, Message: "无变化，已跳过"}
		})
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}
