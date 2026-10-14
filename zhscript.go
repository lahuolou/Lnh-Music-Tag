// 简繁体转换：对选中曲目的文本标签做简体⇄繁体转换（基于 OpenCC 的 Go 移植 gocc）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/liuzl/gocc"

	"lnh-musictag/internal/taglibx"
)

// scriptConverter builds a simp<->trad converter for the given target.
// to == "trad" converts Simplified -> Traditional; otherwise Traditional -> Simplified.
func scriptConverter(to string) func(string) string {
	conv := "s2t"
	if to != "trad" {
		conv = "t2s"
	}
	cc, err := gocc.New(conv)
	if err != nil {
		return func(s string) string { return s }
	}
	return func(s string) string {
		o, err := cc.Convert(s)
		if err != nil {
			return s
		}
		return o
	}
}

// detectTraditional reports whether any text tag contains Traditional Chinese,
// using OpenCC (t2s: converting to simplified changes the string if it contains
// traditional forms). A shared converter is reused across calls.
var tradDetector = func() func(string) string {
	cc, err := gocc.New("t2s")
	if err != nil {
		return func(s string) string { return s }
	}
	return func(s string) string {
		o, err := cc.Convert(s)
		if err != nil {
			return s
		}
		return o
	}
}()

// simpConverter converts Traditional Chinese text to Simplified (t2s).
// Reused by audio identification so identified titles arrive in Simplified.
var simpConverter = tradDetector

func detectTraditional(tags map[string]string) bool {
	for _, k := range textFields {
		v := strings.TrimSpace(tags[k])
		if v == "" {
			continue
		}
		if tradDetector(v) != v {
			return true
		}
	}
	return false
}

// convertScriptHandler converts the text fields of selected tracks between
// simplified and traditional Chinese. Runs as a background job for progress.
func convertScriptHandler(js *jobStore, s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
			To  string   `json:"to"` // "simp" | "trad"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		conv := scriptConverter(req.To)
		list := resolveTracks(s, req.IDs)
		if len(list) == 0 {
			writeErr(w, http.StatusBadRequest, "请至少勾选一首曲目")
			return
		}
		job := js.runProgressJob("script", len(list), func(j *scrapeJob, i int) scrapeResult {
			t := list[i]
			j.mu.Lock()
			j.Current = t.FileName
			j.mu.Unlock()
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
					return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "写标签失败: " + err.Error()}
				}
				refreshTrack(s, t.Path)
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Message: fmt.Sprintf("转换 %d 个字段", changed)}
			}
			// 无需要转换的字段：中性跳过，不算失败、不标红
			return scrapeResult{ID: t.ID, FileName: t.FileName, Skip: true, Message: "无变化，已跳过"}
		})
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}
