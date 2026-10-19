package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lnh-music-tag/internal/audiofmt"
	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

func decodeBase64Raw(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	return b, nil
}

// stripListLyrics 批量剥离歌词正文（列表接口用）。
func stripListLyrics(list []*model.Track) []*model.Track {
	out := make([]*model.Track, len(list))
	for i, t := range list {
		out[i] = stripLyrics(t)
	}
	return out
}

// stripLyrics returns a shallow copy of t whose Tags omit the LYRICS body
// (列表/任务增量只下发育词标志 hasLyrics，正文由单曲接口按需取，避免
// 588 首 × 几 KB 歌词撑爆轮询与首载响应)。
func stripLyrics(t *model.Track) *model.Track {
	if t == nil {
		return nil
	}
	cp := *t
	if len(t.Tags) > 0 {
		tags := make(map[string]string, len(t.Tags))
		for k, v := range t.Tags {
			if k == "LYRICS" {
				continue
			}
			tags[k] = v
		}
		cp.Tags = tags
	}
	return &cp
}

// writeErr 统一错误响应结构：{ok:false, error:msg}（前端 api.js 读 data.error）。
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

// moveToTrash 将文件移入配置目录 trash/（先尝试同卷 rename；跨卷时复制后删除源），
// 返回回收站中的目标路径。去重/删除统一走回收站而非永久删除，便于人工恢复。
func moveToTrash(cfg *cstore.Config, path string) (string, error) {
	if cfg == nil || cfg.Dir() == "" {
		return "", errors.New("未配置回收站目录")
	}
	trash := filepath.Join(cfg.Dir(), "trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(trash, filepath.Base(path))
	if fileExists(dst) { // 重名加时间戳后缀，避免覆盖
		dst = filepath.Join(trash, fmt.Sprintf("%d_%s", time.Now().Unix(), filepath.Base(path)))
	}
	// 同卷才尝试 rename（Windows 的 os.Rename 跨卷必返回 EXDEV，无需白等重试）。
	// 跨卷直接走下方 copy+remove。
	if filepath.VolumeName(path) == filepath.VolumeName(trash) {
		// Windows 文件系统瞬时锁（刚读完的副本/Defender 扫描/句柄延迟释放）：
		// 短等后重试 rename，多数占用是瞬时的，重试即可成功，避免整首误报失败。
		// 窗口 8s：588 首库 ensureHashes 刚读完所有文件，Defender 对队列内
		// 大文件的实时扫描可能持续数秒。
		var renameErr error
		for attempt := 0; attempt < 8; attempt++ {
			time.Sleep(1 * time.Second)
			if err := os.Rename(path, dst); err == nil {
				return dst, nil
			} else {
				renameErr = err
			}
		}
		_ = renameErr // 忽略最终失败，走下方跨卷兜底（copy+remove 同样带重试）
	}
	// 跨卷：复制到回收站后删除源文件。注意 copy 完成后必须
	// 立即关闭源句柄再删除——Windows 上未释放的读句柄（尤其无 DELETE
	// 共享）会直接阻塞 os.Remove，defer Close 太晚。
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		in.Close()
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		os.Remove(dst)
		return "", err
	}
	out.Close()
	in.Close() // 关键：删除源文件前释放读句柄
	// 删除源文件同样带瞬时锁重试（8s 窗口）：copy 刚读完源文件，Defender
	// 实时扫描会短暂以无 DELETE 共享的句柄占用源文件，删除需等扫描完成。
	var rmErr error
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			time.Sleep(1 * time.Second)
		}
		if err := os.Remove(path); err == nil {
			return dst, nil
		} else {
			rmErr = err
		}
	}
	os.Remove(dst)
	return "", rmErr
}

// getenvDefault returns the env value or a fallback when unset/empty.
func getenvDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func fetchBytes(u string) ([]byte, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Lnh-Music-Tag/0.1 (local prototype)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d fetching %s", resp.StatusCode, strings.TrimPrefix(u, "http"))
	}
	return io.ReadAll(resp.Body)
}

// fileExists reports whether a regular file exists at path.
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// stripAudioExt removes trailing audio file extensions (case-insensitive),
// repeatedly, e.g. "广岛之恋.mp3" -> "广岛之恋" and "发如雪.mp3.flac" ->
// "发如雪". Non-audio extensions or strings without one are returned unchanged.
func stripAudioExt(s string) string {
	for {
		ext := strings.ToLower(filepath.Ext(s))
		if ext != "" && (model.AudioExts[ext] || audiofmt.ExtSet[ext]) {
			s = strings.TrimSuffix(s, ext)
			continue
		}
		break
	}
	return s
}

// sanitizeName removes characters that are illegal in file names and collapses
// whitespace, e.g. "a / b" -> "a _ b".
func sanitizeName(s string) string {
	s = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// renameFileToArtistTitle renames a track file to "艺术家 - 标题.后缀" when the
// current filename no longer matches its (new) artist/title tags. Used after
// editing tags and after batch scraping, so "Unknown - 开始恋爱.flac" becomes
// "E-kids - 开始恋爱.flac" once the real artist is known. Skips when the name
// already matches, the artist/title is unusable (Unknown/garbled/empty), or the
// target already exists (never overwrite). Returns the new path, or "" if no
// rename happened.
func renameFileToArtistTitle(store *store, t *model.Track, artist, title string) string {
	artist = strings.TrimSpace(artist)
	title = strings.TrimSpace(title)
	if artist == "" || title == "" || looksUnknown(artist) || looksGarbled(artist) || looksGarbled(title) {
		return ""
	}
	want := sanitizeName(artist) + " - " + sanitizeName(stripAudioExt(title))
	if stripAudioExt(t.FileName) == want {
		return ""
	}
	ext := strings.TrimPrefix(strings.ToLower(t.Ext), ".")
	if ext == "" {
		ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(t.FileName)), ".")
	}
	if ext == "" {
		ext = "mp3"
	}
	newPath := filepath.Join(filepath.Dir(t.Path), want+"."+ext)
	if newPath == t.Path {
		return ""
	}
	if _, err := os.Stat(newPath); err == nil {
		return "" // 目标已存在，避免覆盖其他文件
	}
	if err := os.Rename(t.Path, newPath); err != nil {
		return ""
	}
	store.removeByPath(t.Path)
	refreshTrack(store, newPath)
	return newPath
}

// renameCleanFile renames an audio file so it keeps only its real extension,
// determined from the actual encoding (magic bytes), not the current suffix.
//   - "发如雪.mp3.flac" (real FLAC) -> "发如雪.flac"
//   - "发如雪.mp3.mp3"   (real MP3)  -> "发如雪.mp3"
//   - "song.flac" that is really MP3 -> "song.mp3"
//
// It returns the (possibly unchanged) new path. Files that are already clean,
// undetectable, or whose clean target already exists are left untouched.
func renameCleanFile(path string) (string, error) {
	base := stripAudioExt(filepath.Base(path))
	ext := audiofmt.DetectExt(path)
	if ext == "" {
		return path, nil
	}
	dir := filepath.Dir(path)
	newPath := filepath.Join(dir, base+ext)
	if newPath == path {
		return path, nil
	}
	if _, err := os.Stat(newPath); err == nil {
		return path, fmt.Errorf("目标已存在，跳过重命名: %s", filepath.Base(newPath))
	}
	if err := os.Rename(path, newPath); err != nil {
		return path, err
	}
	return newPath, nil
}
