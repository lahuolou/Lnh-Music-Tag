package main

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"lnh-musictag/internal/audiofmt"
)

// sameFileContent 比较两个文件是否内容相同（先比大小，再比 SHA256）。
// 用于目标文件已存在时区分“纯重复副本”和“同名不同版本”。
func sameFileContent(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	if fa.Size() != fb.Size() {
		return false
	}
	ha, err := sha256File(a)
	if err != nil {
		return false
	}
	hb, err := sha256File(b)
	if err != nil {
		return false
	}
	return ha == hb
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return string(h.Sum(nil)), nil
}

// cleanTarget 计算修正后的目标路径：真实编码后缀 + 去全部音频后缀的基名。
func cleanTarget(path string) (string, string) {
	ext := audiofmt.DetectExt(path)
	if ext == "" {
		return path, ""
	}
	base := stripAudioExt(filepath.Base(path))
	return filepath.Join(filepath.Dir(path), base+ext), ext
}

// renameCleanHandler 批量修正文件后缀：先探测音频文件的真实编码格式，
// 再重命名为正确的单一后缀，例如：
//   - "发如雪.mp3.flac"（真实编码为 FLAC）→ "发如雪.flac"
//   - "发如雪.mp3.mp3"（真实编码为 MP3） → "发如雪.mp3"
//   - "song.flac" 实为 MP3 内容           → "song.mp3"
//
// ids 为空 = 全部曲目；以后台任务运行，前端轮询实时进度。
// 目标文件已存在时不删除、不改动：内容相同按“重复副本”提示可去重；
// 内容不同按“同名不同版本”提示去重页手动选择，绝不默认删除。
func renameCleanHandler(js *jobStore, s *store) http.HandlerFunc {
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
		job := js.runProgressJob("renameClean", len(list), func(j *scrapeJob, i int) scrapeResult {
			t := list[i]
			j.mu.Lock()
			j.Current = t.FileName
			j.mu.Unlock()

			target, _ := cleanTarget(t.Path)
			if target != t.Path && fileExists(target) {
				// 目标已存在：不自动处理，按内容相同/不同给出可执行提示
				if sameFileContent(t.Path, target) {
					return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Skip: true,
						Message: "同名 " + filepath.Base(target) + " 已存在且内容相同（重复副本），可在去重中删除"}
				}
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Skip: true,
					Message: "同名 " + filepath.Base(target) + " 已存在（不同版本），请在去重中选择保留哪个"}
			}
			np, err := renameCleanFile(t.Path)
			if err != nil {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Skip: true, Message: err.Error()}
			}
			if np == t.Path {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Skip: true, Message: "后缀已正确"}
			}
			// 更新 store：保留原 ID、路径/文件名更新、重读标签并落库
			renameTrack(s, t, np)
			return scrapeResult{ID: t.ID, FileName: filepath.Base(np), OK: true, Message: "→ " + filepath.Base(np)}
		})
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}
