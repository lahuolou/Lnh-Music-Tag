// 格式转换：把选中曲目转码为 mp3/flac/m4a/ogg/opus/wav（依赖 ffmpeg），
// 转换后重新登记曲目（保留标签元数据），可选删除原文件。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	cstore "lnh-music-tag/internal/store"
)

// convertCodecs maps target extension -> ffmpeg audio codec args.
var convertCodecs = map[string]string{
	"mp3":  "libmp3lame -q:a 2",
	"flac": "flac",
	"m4a":  "aac -b:a 192k",
	"ogg":  "libvorbis -q:a 5",
	"opus": "libopus -b:a 128k",
	"wav":  "pcm_s16le",
	"aiff": "pcm_s16be",
	"aac":  "aac -b:a 192k",
	"wma":  "wmav2 -b:a 192k",
	"tta":  "tta",
}

// losslessCodecs ignore bitrate settings.
var losslessCodecs = map[string]bool{"flac": true, "wav": true, "aiff": true, "tta": true}

// convertHandler transcodes selected tracks to a target format. It starts a
// background job so the frontend can show live progress per track.
// FFmpeg is a plugin: missing binary returns a clear 400 pointing at Settings.
func convertHandler(js *jobStore, s *store, cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bin := ffmpegBin(cfg)
		if bin == "" {
			writeErr(w, http.StatusBadRequest, "未检测到 FFmpeg，请到「设置 → FFmpeg」配置路径或一键安装后再试")
			return
		}
		var req struct {
			IDs          []string `json:"ids"`
			Target       string   `json:"target"`
			InputFormats []string `json:"inputFormats"` // 留空=全部；按源文件扩展名过滤
			Bitrate      string   `json:"bitrate"`      // 如 192k / keep / ""
			SampleRate   string   `json:"sampleRate"`   // 如 44100 / keep / ""
			RemoveSource bool     `json:"removeSource"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		inSet := map[string]bool{}
		for _, f := range req.InputFormats {
			f = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(f, ".")))
			if f != "" {
				inSet[f] = true
			}
		}
		br := strings.ToLower(strings.TrimSpace(req.Bitrate))
		if br == "keep" {
			br = ""
		}
		// 白名单：只允许常见比特率，拒绝任何畸形/注入输入
		switch br {
		case "", "64k", "128k", "192k", "256k", "320k":
		default:
			writeErr(w, http.StatusBadRequest, "invalid bitrate")
			return
		}
		sr := strings.ToLower(strings.TrimSpace(req.SampleRate))
		if sr == "keep" {
			sr = ""
		}
		switch sr {
		case "", "44100", "48000", "88200", "96000":
		default:
			writeErr(w, http.StatusBadRequest, "invalid sample rate")
			return
		}
		list := resolveTracks(s, req.IDs)
		if len(list) == 0 {
			writeErr(w, http.StatusBadRequest, "请至少勾选一首曲目")
			return
		}
		target0 := strings.ToLower(strings.TrimSpace(req.Target))
		if target0 == "keep" {
			target0 = ""
		}
		job := js.runProgressJob("convert", len(list), func(j *scrapeJob, i int) scrapeResult {
			t := list[i]
			j.mu.Lock()
			j.Current = t.FileName
			j.mu.Unlock()
			srcExt := strings.ToLower(strings.TrimPrefix(t.Ext, "."))
			if len(inSet) > 0 && !inSet[srcExt] {
				return scrapeResult{ID: t.ID, FileName: t.FileName, Skip: true, Message: "跳过（输入格式不在所选范围）"}
			}
			target := target0
			if target == "" {
				target = srcExt
			}
			codec, ok := convertCodecs[target]
			if !ok {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "不支持的目标格式: " + target}
			}
			// 已是目标格式：仍可按比特率/采样率重编码
			same := strings.EqualFold(srcExt, target)
			dir := filepath.Dir(t.Path)
			base := stripAudioExt(filepath.Base(t.Path))
			dst := filepath.Join(dir, base+"."+target)
			// 同格式重编码（dst == 源路径）时 ffmpeg 拒绝读写同一文件，
			// 先转临时文件再原子替换。
			tmp := dst
			if same {
				f, terr := os.CreateTemp(dir, ".lnh-conv-*")
				if terr != nil {
					return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: terr.Error()}
				}
				tmp = f.Name()
				f.Close()
				defer os.Remove(tmp)
			}
			if err := ffmpegConvert(bin, t.Path, tmp, codec, br, sr, losslessCodecs[target]); err != nil {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: err.Error()}
			}
			if tmp != dst {
				if err := os.Rename(tmp, dst); err != nil {
					return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "替换文件失败: " + err.Error()}
				}
			}
			// 重新登记：删除原文件则替换曲目，否则新增一首
			if req.RemoveSource && !same {
				s.remove(t.ID)
				os.Remove(t.Path)
			}
			refreshTrack(s, dst)
			return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Message: "已转换 → " + strings.ToUpper(target)}
		})
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}

// ffmpegConvert re-encodes src to dst with the given audio codec args,
// keeping metadata tags via -map_metadata 0. bin is the resolved ffmpeg path.
func ffmpegConvert(bin, src, dst, codecArgs, bitrate, sampleRate string, lossless bool) error {
	parts := strings.Fields(codecArgs)
	args := []string{"-y", "-i", src, "-map_metadata", "0", "-codec:a"}
	args = append(args, parts...)
	if bitrate != "" && !lossless {
		args = append(args, "-b:a", bitrate)
	}
	if sampleRate != "" {
		args = append(args, "-ar", sampleRate)
	}
	args = append(args, dst)
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
