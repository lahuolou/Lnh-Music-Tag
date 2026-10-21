// 回收站管理：列出 / 恢复 / 永久删除 / 清空。
//
// 回收站 = 配置目录 trash/。删除/去重时文件由 moveToTrash 移入，并同步在
// _manifest.jsonl 记录原路径（供恢复放回原位）；v1.0.8 之前移入的旧文件
// 无清单记录，列表显示"原路径未知"，无法自动恢复（只能手动从回收站目录找回）。
// 恢复放回原目录后需重新扫描音乐目录，曲目才会重新入库。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	cstore "lnh-music-tag/internal/store"
)

// trashItem 回收站文件条目（GET /api/trash 返回）。
type trashItem struct {
	Name         string `json:"name"`
	OriginalPath string `json:"originalPath,omitempty"` // 清单记录的原路径；旧文件为空
	Size         int64  `json:"size"`
	ModTime      string `json:"modTime"`
	MovedAt      string `json:"movedAt,omitempty"`
}

func trashDir(cfg *cstore.Config) string {
	return filepath.Join(cfg.Dir(), "trash")
}

// loadTrashManifest 读取清单：回收站文件名 -> 原路径。
func loadTrashManifest(cfg *cstore.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil || cfg.Dir() == "" {
		return out
	}
	trashManifestMu.Lock()
	defer trashManifestMu.Unlock()
	b, err := os.ReadFile(trashManifestPath(cfg))
	if err != nil {
		return out // 文件不存在（旧版无清单）视为空清单
	}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var rec struct {
			N string `json:"n"`
			P string `json:"p"`
			T string `json:"t"`
		}
		if json.Unmarshal([]byte(ln), &rec) != nil || rec.N == "" {
			continue // 坏行跳过，不影响其余条目
		}
		out[rec.N] = rec.P
	}
	return out
}

// writeTrashManifest 全量重写清单（恢复/删除/清空后同步）。
func writeTrashManifest(cfg *cstore.Config, m map[string]string) {
	if cfg == nil || cfg.Dir() == "" {
		return
	}
	trashManifestMu.Lock()
	defer trashManifestMu.Unlock()
	var sb strings.Builder
	for name, path := range m {
		rec, err := json.Marshal(map[string]string{"n": name, "p": path, "t": time.Now().UTC().Format(time.RFC3339)})
		if err != nil {
			continue
		}
		sb.Write(rec)
		sb.WriteByte('\n')
	}
	_ = os.WriteFile(trashManifestPath(cfg), []byte(sb.String()), 0o600)
}

// validTrashName 校验请求中的回收站文件名：非空、不允许路径穿越、
// 不允许指向清单自身（防止恢复/删除元数据文件）。
func validTrashName(name string) bool {
	if name == "" || name == "_manifest.jsonl" {
		return false
	}
	return filepath.Base(name) == name
}

// trashListHandler GET /api/trash 列出回收站全部文件（按移入时间倒序）。
func trashListHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := trashDir(cfg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, map[string]any{"items": []trashItem{}})
				return
			}
			writeErr(w, http.StatusInternalServerError, "读取回收站失败: "+err.Error())
			return
		}
		manifest := loadTrashManifest(cfg)
		items := make([]trashItem, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || e.Name() == "_manifest.jsonl" {
				continue
			}
			it := trashItem{Name: e.Name(), OriginalPath: manifest[e.Name()]}
			if st, err := e.Info(); err == nil {
				it.Size = st.Size()
				it.ModTime = st.ModTime().Format("2006-01-02 15:04:05")
			}
			items = append(items, it)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

// moveFileBack 把回收站文件放回原位：同卷 rename，跨卷复制后删除源。
// 返回放回后的完整路径。
func moveFileBack(src, dst string) error {
	if filepath.VolumeName(src) == filepath.VolumeName(dst) {
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
		// rename 失败（如瞬时锁）时回退跨卷路径（copy+remove）
	}
	if err := copyFileMode(src, dst, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}

// trashRestoreHandler POST /api/trash/restore {name} 把文件放回原位置。
// 无清单记录（旧版移入）或原目录已不存在时明确报错，不静默乱放。
func trashRestoreHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if !validTrashName(req.Name) {
			writeErr(w, http.StatusBadRequest, "invalid trash name")
			return
		}
		src := filepath.Join(trashDir(cfg), req.Name)
		if _, err := os.Stat(src); err != nil {
			writeErr(w, http.StatusBadRequest, "回收站中不存在该文件")
			return
		}
		manifest := loadTrashManifest(cfg)
		orig, ok := manifest[req.Name]
		if !ok || strings.TrimSpace(orig) == "" {
			writeErr(w, http.StatusBadRequest, "该文件无原路径记录（旧版本移入），无法自动恢复")
			return
		}
		if st, err := os.Stat(orig); err == nil && !st.IsDir() {
			writeErr(w, http.StatusBadRequest, "原位置已存在同名文件，请先处理后再恢复")
			return
		} else if err != nil && !os.IsNotExist(err) {
			writeErr(w, http.StatusInternalServerError, "检查原位置失败: "+err.Error())
			return
		}
		if dir := filepath.Dir(orig); dir != "." {
			if _, err := os.Stat(dir); err != nil {
				if os.IsNotExist(err) {
					writeErr(w, http.StatusBadRequest, "原目录已不存在（"+dir+"），无法恢复")
					return
				}
				writeErr(w, http.StatusInternalServerError, "检查原目录失败: "+err.Error())
				return
			}
		}
		if err := moveFileBack(src, orig); err != nil {
			writeErr(w, http.StatusInternalServerError, "恢复失败: "+err.Error())
			return
		}
		delete(manifest, req.Name)
		writeTrashManifest(cfg, manifest)
		addLog("act_trash_restore", true, 1, 0, 0, "从回收站恢复 "+req.Name+" → "+orig)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": orig})
	}
}

// trashDeleteHandler POST /api/trash/delete {name} 永久删除回收站中的单个文件。
func trashDeleteHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if !validTrashName(req.Name) {
			writeErr(w, http.StatusBadRequest, "invalid trash name")
			return
		}
		src := filepath.Join(trashDir(cfg), req.Name)
		if err := os.Remove(src); err != nil {
			if os.IsNotExist(err) {
				writeErr(w, http.StatusBadRequest, "回收站中不存在该文件")
				return
			}
			writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
			return
		}
		manifest := loadTrashManifest(cfg)
		delete(manifest, req.Name)
		writeTrashManifest(cfg, manifest)
		addLog("act_trash_delete", true, 1, 0, 0, "永久删除回收站文件 "+req.Name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// trashClearHandler POST /api/trash/clear 清空回收站（全部永久删除）。
func trashClearHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := trashDir(cfg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0})
				return
			}
			writeErr(w, http.StatusInternalServerError, "读取回收站失败: "+err.Error())
			return
		}
		// Windows 瞬时锁（Defender 扫描等）：删除失败重试数次，避免一次失败
		// 就误报"清空失败"并留下半清状态。
		removed := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			full := filepath.Join(dir, e.Name())
			var rmErr error
			for attempt := 0; attempt < 8; attempt++ {
				if attempt > 0 {
					time.Sleep(500 * time.Millisecond)
				}
				if err := os.Remove(full); err == nil {
					removed++
					break
				} else {
					rmErr = err
				}
			}
			if rmErr != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Sprintf("清空失败（%s）: %v", e.Name(), rmErr))
				return
			}
		}
		_ = os.Remove(trashManifestPath(cfg)) // 清空后清单一并移除
		addLog("act_trash_clear", true, removed, 0, 0, "清空回收站，共删除 "+fmt.Sprint(removed)+" 个文件")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	}
}
