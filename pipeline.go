// 一键刮削流水线（pipeline）：
//
//	扫描文件 → 乱码修复 → 繁体转简体 → 文件名补艺术家 → 刮削元素
//
// 每个阶段是独立小函数（stageFixEncoding / stageScript / stageFixArtist /
// stageScrape），后续要新增步骤只需在 pipelineStages 里加一个函数。
// 任务在后台运行，逐曲目×逐阶段推进统一进度，前端通过
// GET /api/scrape/jobs/{id} 轮询（阶段名 + 进度 + 成功/失败/跳过统计）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/dedup"
	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

// pipelineStages 一键刮削的阶段总数（不含可选的自动去重阶段）。
const pipelineStages = 4

// oneKeyScrapeHandler 启动一键刮削任务：ids 为空 = 全部曲目。
// Dedup 可选：勾选后在流水线最前先自动去重（每组保留质量最好的一首，
// 其余删除）；默认不勾选。
// 进度按曲目计数（total = 曲目数），当前阶段由 Stage/StageLabel 单独显示，
// 避免出现"592 首歌却显示 2368"这类 4 倍进度造成的误解。
func oneKeyScrapeHandler(js *jobStore, s *store, ms *scrape.MultiSource, cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs   []string `json:"ids"`
			Dedup bool     `json:"dedup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		list := resolveTracks(s, req.IDs)
		if len(list) == 0 {
			writeErr(w, http.StatusBadRequest, "没有可处理的曲目")
			return
		}
		job := js.create(len(list), "onekey")
		go func() {
			runOneKeyPipeline(job, s, ms, list, req.Dedup, cfg)
			js.persist(job) // 完成/中断后落库，重启可查历史
		}()
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}

// stageFunc 是流水线中单个曲目的一个处理步骤。返回处理后的最新曲目
// （阶段内可能重命名产生新 ID；nil 表示该曲目已被删除）。
type stageFunc func(t *model.Track) (nt *model.Track, ok, skip bool, msg string)

// runOneKeyPipeline 依次执行每个阶段；每个（曲目 × 阶段）占一个进度点。
// 用户点击取消后立即停止，不再处理剩余曲目。
// dedup 为 true 时在流水线最前插入"自动去重"阶段。
// cfg 提供 AcoustID Key（存疑曲目自动识别开关）与 FFmpeg 路径。
func runOneKeyPipeline(job *scrapeJob, s *store, ms *scrape.MultiSource, list []*model.Track, dedup bool, cfg *cstore.Config) {
	defer func() {
		job.mu.Lock()
		if job.Cancel {
			job.Status = "cancelled"
		} else {
			job.Status = "done"
		}
		job.Current = ""
		job.Stage = ""
		job.StageLabel = ""
		job.mu.Unlock()
		logJobDone(job) // 一键刮削完成后写入操作日志（按曲目聚合）
	}()

	// pipelineStage 流水线的一个阶段（key/label 用于前端阶段显示）。
	type pipelineStage struct {
		key, label string
		fn         stageFunc
	}
	stages := []pipelineStage{
		{"fixEnc", "乱码修复", stageFixEncoding(s)},
		{"script", "繁体转简体", stageScript(s)},
		{"artist", "文件名补艺术家", stageFixArtist(s)},
		{"scrape", "刮削元素", stageScrape(s, ms, cfg)},
	}
	if dedup {
		stages = append([]pipelineStage{{"dedup", "自动去重", stageDedup(s, cfg, job)}}, stages...)
	}

	for si, st := range stages {
		// 进入新阶段：重置阶段内进度（阶段进度 = 本阶段已处理曲目数/总曲目数）。
		// 前端进度条据此显示"当前阶段"进度；自动去重阶段的哈希进度单独走
		// hashDone/hashTotal（哈希期间逐首删除判断尚未开始）。
		job.mu.Lock()
		job.Stage = st.key
		job.StageLabel = fmt.Sprintf("%s（%d/%d）", st.label, si+1, len(stages))
		job.StageDone = 0
		job.StageTotal = len(list)
		job.mu.Unlock()

		for i := range list {
			if jobCanceled(job) {
				return
			}
			t := list[i]
			// 每阶段从 store 取最新状态：前一阶段（如乱码修复/自动去重）可能已
			// 重写标签/重命名/删除，后续阶段要基于最新状态处理，而不是启动快照。
			cur := s.get(t.ID)
			job.mu.Lock()
			job.Current = ""
			if cur != nil {
				job.Current = cur.FileName
			}
			job.StageDone++
			job.mu.Unlock()
			if cur == nil {
				// 已被去重等阶段删除：该曲目视为已完成
				job.mu.Lock()
				job.Done++
				job.mu.Unlock()
				continue
			}

			nt, ok, skip, msg := st.fn(cur)
			if nt != nil && nt.ID != cur.ID {
				// 阶段内重命名产生了新 ID：后续阶段基于新曲目继续
				list[i] = nt
			}
			job.mu.Lock()
			if nt == nil {
				// 该曲目已被阶段删除（自动去重）
				job.Results = append(job.Results, scrapeResult{ID: cur.ID, FileName: cur.FileName, OK: ok, Skip: skip, Message: msg})
				if si == len(stages)-1 {
					job.Done++
				}
				job.mu.Unlock()
				continue
			}
			job.Results = append(job.Results, scrapeResult{ID: nt.ID, FileName: nt.FileName, OK: ok, Skip: skip, Message: msg})
			// 一首曲目的所有阶段全部走完后，曲目进度 +1
			if si == len(stages)-1 {
				job.Done++
			}
			// 增量快照：前端据此实时更新列表，不整表刷新
			if ntt := s.get(nt.ID); ntt != nil {
				job.Updated = append(job.Updated, ntt)
			}
			job.mu.Unlock()

			// 刮削阶段对源限流，避免触发频控
			if st.key == "scrape" && len(list) > 1 {
				time.Sleep(1100 * time.Millisecond)
			}
		}
	}
}

// stageDedup 阶段 0（可选）：自动去重。按三种口径合并分组（与去重页一致）：
// ① 艺术家+标题一致（同人同歌不同格式/版本）② 同名多格式 ③ SHA256 内容一致
// （完全相同的副本）。每组保留质量最好的一首（无损优先；hash 组保留第一首），
// 其余移入配置目录 trash/ 回收站并从列表移除（不永久删除，可人工恢复）。
// 仅当一键刮削勾选"自动去重"时启用；默认不勾选。
// job 用于：① 哈希阶段把进度（hashDone/hashTotal）与当前文件写回任务，
//
//	让用户在慢盘上看到"在动"而不是 0/947 假死；② 每文件哈希后检查取消，
//	使"取消"在去重阶段立即生效（此前 ensureHashes 同步阻塞，取消无效）。
func stageDedup(s *store, cfg *cstore.Config, job *scrapeJob) stageFunc {
	var once sync.Once
	remove := map[string]bool{} // track id → 待删除
	init := func() {
		tracks := s.all()
		// hash 分组依赖 SHA256：扫描时为轻量读未计算，先去重前补齐。
		// 提前暴露哈希总量，并把每文件进度写回 job（可中断）。
		need := 0
		for _, t := range tracks {
			if t.SHA256 == "" {
				need++
			}
		}
		if need > 0 {
			job.mu.Lock()
			job.HashTotal = need
			job.mu.Unlock()
		}
		s.ensureHashes(tracks, func(cur string, done int) {
			job.mu.Lock()
			job.HashDone = done
			job.Current = cur
			job.mu.Unlock()
		}, func() bool { return jobCanceled(job) })
		if jobCanceled(job) {
			return // 已被取消：remove 为空，逐首走"保留"分支，外层循环随即停止
		}
		tracks = s.all() // ensureHashes 可能替换了 Track 引用（写入新 SHA256）
		groups := append(append(dedup.GroupByArtistTitle(tracks), dedup.GroupBySameName(tracks)...), dedup.GroupByHash(tracks)...)
		// 先汇总所有组的保留者（一个曲目可能同时在多个组出现，任一组的
		// keep 都优先于其他组的删除），再标记删除：只删"不在任何 keep 集合"的
		keep := map[string]bool{}
		for _, g := range groups {
			k := g.KeepID
			if k == "" && len(g.IDs) > 0 {
				k = g.IDs[0]
			}
			if k != "" {
				keep[k] = true
			}
		}
		for _, g := range groups {
			for _, id := range g.IDs {
				if !keep[id] {
					remove[id] = true
				}
			}
		}
	}
	return func(t *model.Track) (*model.Track, bool, bool, string) {
		once.Do(init)
		if !remove[t.ID] {
			return t, false, true, "无重复，保留"
		}
		if _, err := moveToTrash(cfg, t.Path); err != nil {
			return t, false, false, "移入回收站失败: " + err.Error()
		}
		s.remove(t.ID)
		return nil, true, false, "重复文件，已移入回收站"
	}
}

// stageFixEncoding 阶段 1：修复标签与文件名中的乱码（无乱码则中性跳过）。
// 与批量页共用 repairTrackText（同一修复口径，修复结果统一转简体）。
func stageFixEncoding(s *store) stageFunc {
	return func(t *model.Track) (*model.Track, bool, bool, string) {
		changed, m, newBase := repairTrackText(t)
		if len(m) > 0 {
			if err := taglibx.WriteTags(t.Path, m, false); err != nil {
				return t, false, false, "写标签失败: " + err.Error()
			}
		}
		// 文件名乱码 → 修复并重命名（列表显示的就是文件名，不修不会消失）
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
					renameTrack(s, t, newPath)
				}
			}
		}
		if renamed != "" {
			return t, true, false, "修复标签，文件名→" + renamed
		}
		if len(m) > 0 {
			refreshTrack(s, t.Path)
			return t, true, false, fmt.Sprintf("修复 %d 个字段", len(changed))
		}
		return t, false, true, "无乱码，跳过"
	}
}

// stageScript 阶段 2：繁体转简体（一键默认繁→简，整理港台唱片）。
func stageScript(s *store) stageFunc {
	conv := scriptConverter("simp")
	return func(t *model.Track) (*model.Track, bool, bool, string) {
		m := map[string][]string{}
		changed := 0
		for _, k := range textFields {
			v := strings.TrimSpace(t.Tags[k])
			if v == "" {
				continue
			}
			c := conv(v)
			if c != v {
				m[k] = []string{c}
				changed++
			}
		}
		if len(m) > 0 {
			if err := taglibx.WriteTags(t.Path, m, false); err != nil {
				return t, false, false, "写标签失败: " + err.Error()
			}
			refreshTrack(s, t.Path)
			return t, true, false, fmt.Sprintf("转简体 %d 个字段", changed)
		}
		return t, false, true, "无繁体，跳过"
	}
}

// stageFixArtist 阶段 3：文件名不含艺术家时，从「艺术家 - 标题」文件名
// 提取艺术家（标题缺失时一并补），并重命名为规范格式。
func stageFixArtist(s *store) stageFunc {
	return func(t *model.Track) (*model.Track, bool, bool, string) {
		artist := strings.TrimSpace(t.Tags[taglibx.Artist])
		title := strings.TrimSpace(t.Tags[taglibx.Title])
		hasArtist := artist != "" && !looksUnknown(artist) && !looksGarbledText(artist)
		hasTitle := title != "" && !looksUnknown(title) && !looksGarbledText(title)
		if hasArtist && hasTitle {
			return t, false, true, "艺术家已存在，跳过"
		}
		fa, ft := parseFileNameArtistTitle(t.FileName)
		// "Unknown"/"未知" 是占位符不是真实艺术家，同样视为不含艺术家
		if fa == "" || looksGarbledText(fa) || looksUnknown(fa) {
			return t, false, true, "文件名不含真实艺术家，跳过"
		}
		m := map[string][]string{}
		if !hasArtist {
			m[taglibx.Artist] = []string{fa}
		}
		if !hasTitle && ft != "" && !looksGarbledText(ft) {
			m[taglibx.Title] = []string{ft}
		}
		if len(m) > 0 {
			if err := taglibx.WriteTags(t.Path, m, false); err != nil {
				return t, false, false, "写标签失败: " + err.Error()
			}
			refreshTrack(s, t.Path)
		}
		// 按「艺术家 - 标题.后缀」重命名（已规范则不动）
		if np := renameFileToArtistTitle(s, t, fa, ft); np != "" {
			return t, true, false, "补艺术家 " + fa + "，文件已重命名"
		}
		return t, true, false, "补艺术家 " + fa
	}
}

// stageScrape 阶段 4：一键刮削全部元素（与批量刮削元素字段一致，含海报/
// 歌词/文件名重命名 + 扩展标签 + 音频属性）。已有标签智能跳过；源无结果/
// 无海报自动降级下一条源。配置了 AcoustID Key 时，存疑曲目（Unknown/乱码/
// 缺艺术家）先走音频识别（指纹 → AcoustID → 写标签 → 重命名），再刮削兜底。
func stageScrape(s *store, ms *scrape.MultiSource, cfg *cstore.Config) stageFunc {
	fields := allScrapeFields()
	// 一键刮削全程共享的专辑级封面缓存（同专辑曲目复用封面）
	coverCache := map[string][]byte{}
	return func(t *model.Track) (*model.Track, bool, bool, string) {
		// 存疑自动识别：配了 AcoustID Key 才启用（识别含库内指纹回退，
		// 不额外消耗在线额度；未命中时继续刮削兜底）
		if cfg.GetDefault("acoustid_key", "") != "" && trackIsDoubtful(t) {
			res := identifyDoubtfulTrack(s, ms, t, cfg)
			if res.OK && res.NewID != "" {
				if nt := s.get(res.NewID); nt != nil {
					t = nt
				}
			}
		}
		msg, ok := scrapeOneTrack(t, ms, "auto", fields, true, s, coverCache)
		skip := strings.Contains(msg, "跳过")
		return t, ok, skip, msg
	}
}
