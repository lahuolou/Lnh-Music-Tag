// 音频内容识别：对"无文件名信息、无标签"的曲目（如 track01.mp3）按文档
// 方案走 AcoustID → MusicBrainz 全链路流水线：
//
//	音频文件 → Chromaprint 指纹 → AcoustID 查询（3 QPS）→ MusicBrainz
//	Recording 补全（1 QPS）→ taglib 写标签 → 重命名"艺术家 - 标题.后缀"
//
// 需要转码的格式（m4a/aac/alac/aiff/wma/ape/wv/dsf/dff 等）先由 FFmpeg
// 转为 WAV 再送入指纹计算；DSD（.dsf/.dff）同样先转 PCM。AcoustID 未配置
// Key 或未命中时，回退到库内已标注曲目的指纹匹配（本地高速路径）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/finger"
	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

// identifyExts 是内容识别支持的音频格式（文档 14 种 + 兼容已有 .mpc/.mka）。
var identifyExts = map[string]bool{
	".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".alac": true,
	".wav": true, ".aiff": true, ".aif": true, ".ogg": true, ".opus": true,
	".wma": true, ".ape": true, ".wv": true, ".dsf": true, ".dff": true,
	".mpc": true, ".mka": true,
}

// identifyNeedsTranscode 报告该格式能否直接送入 fpcalc。
// Chromaprint 原生支持 MP3 / FLAC / WAV / OGG（Vorbis/Opus）；其余格式
// （含 DSD）需先经 FFmpeg 转为 WAV 再指纹。
func identifyNeedsTranscode(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp3", ".flac", ".wav", ".ogg", ".opus":
		return false
	default:
		return true
	}
}

// identifyTranscodeTimeout 识别前转码的单文件超时：ffmpeg 卡死（权限/坏文件）
// 时该曲目在超时后报错跳过，不阻塞整个识别任务。
const identifyTranscodeTimeout = 2 * time.Minute

// ffmpegToWAV 用 FFmpeg 把任意音频转成 16bit/44.1kHz/双声道 WAV（指纹输入）。
func ffmpegToWAV(bin, src, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), identifyTranscodeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-y", "-loglevel", "error", "-i", src,
		"-f", "wav", "-acodec", "pcm_s16le", "-ar", "44100", "-ac", "2", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg 转码超时（>%s）", identifyTranscodeTimeout)
		}
		return fmt.Errorf("ffmpeg 转码失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// fpForTrack 计算曲目指纹：需要转码的格式先转 WAV（临时文件，用完删除）。
// 返回压缩指纹（AcoustID 查询用）与 raw 指纹（库内海明距离匹配用）。
func fpForTrack(fp *finger.Fpcalc, ffBin string, t *model.Track) (compressed, raw string, err error) {
	src := t.Path
	if identifyNeedsTranscode(t.Ext) {
		if ffBin == "" {
			return "", "", fmt.Errorf("格式 %s 需 FFmpeg 转码为 WAV 后识别，但未检测到 FFmpeg（可在设置/插件页配置）", t.Ext)
		}
		tmp, terr := os.CreateTemp("", "lnh-identify-*.wav")
		if terr != nil {
			return "", "", fmt.Errorf("创建临时文件失败: %w", terr)
		}
		tmpPath := tmp.Name()
		tmp.Close()
		defer os.Remove(tmpPath)
		if terr := ffmpegToWAV(ffBin, t.Path, tmpPath); terr != nil {
			return "", "", terr
		}
		src = tmpPath
	}
	compressed, err = fp.CompressedFingerprint(src)
	if err != nil {
		return "", "", err
	}
	raw, err = fp.Fingerprint(src)
	if err != nil {
		return "", "", err
	}
	return compressed, raw, nil
}

// filenameNoInfo reports whether a filename carries no usable title info:
// empty, placeholder ("Unknown"), garbled, or a bare track number (01, track01).
// A placeholder artist (Unknown/未知) also counts as "no info": the file
// has no real artist attribution and should go through audio identification.
func filenameNoInfo(fn string) bool {
	artist, title := parseFileNameArtistTitle(fn)
	t := strings.TrimSpace(title)
	if t == "" || looksUnknown(t) || looksGarbled(t) {
		return true
	}
	if looksUnknown(artist) {
		return true
	}
	low := strings.ToLower(t)
	low = strings.TrimPrefix(low, "track")
	if low != "" {
		allDigit := true
		for _, r := range low {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	return false
}

// needsIdentify reports whether a track has no usable metadata at all: a
// filename that carries no real title (track01/01/Unknown/乱码/空) whose tags
// are either empty or just taglib's filename fallback (title == basename).
// Only then does the track need audio-content identification.
func needsIdentify(t *model.Track) bool {
	if !filenameNoInfo(t.FileName) {
		return false
	}
	title := strings.TrimSpace(t.Tags["TITLE"])
	if title != "" && !looksUnknown(title) && !looksGarbled(title) {
		base := strings.TrimSuffix(filepath.Base(t.FileName), filepath.Ext(t.FileName))
		if !strings.EqualFold(title, base) {
			return false // 标签是真实信息（如 01.mp3 内嵌"七里香"）
		}
	}
	return true
}

// identifyBase caches fingerprints of tagged tracks so repeated identify runs
// do not re-analyze the whole library (keyed by id; built once per process).
var (
	identifyBaseOnce sync.Once
	identifyBaseMu   sync.Mutex
	identifyBase     = map[string]string{} // trackID -> fingerprint
)

func buildIdentifyBase(s *store, fp *finger.Fpcalc) {
	identifyBaseOnce.Do(func() {
		tracks := s.all()
		workers := runtime.NumCPU()
		if workers > 4 {
			workers = 4
		}
		if workers < 1 {
			workers = 1
		}
		ch := make(chan *model.Track)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range ch {
					title := strings.TrimSpace(t.Tags["TITLE"])
					if title == "" || looksUnknown(title) {
						continue
					}
					if f, err := fp.Fingerprint(t.Path); err == nil && f != "" {
						identifyBaseMu.Lock()
						identifyBase[t.ID] = f
						identifyBaseMu.Unlock()
					}
				}
			}()
		}
		for _, t := range tracks {
			ch <- t
		}
		close(ch)
		wg.Wait()
	})
}

type identifyResult struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
	OK       bool   `json:"ok"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Message  string `json:"message"`
	// NewID 为重命名后新曲目的 ID（批量刮削后续步骤据其继续处理）
	NewID string `json:"newId,omitempty"`
}

// identifyHandler matches selected (or all unknown) tracks against the tagged
// library by audio fingerprint, copies the matched tags and fetches lyrics.
// Runs as a background job with per-track progress.
func identifyHandler(js *jobStore, s *store, ms *scrape.MultiSource, cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		fp := &finger.Fpcalc{Bin: "fpcalc"}
		if !fp.Available() {
			writeErr(w, http.StatusBadRequest, "音频识别引擎不可用：容器内需安装 chromaprint（fpcalc），请更新镜像")
			return
		}
		ffBin := ffmpegBin(cfg)

		var targets []*model.Track
		if len(req.IDs) > 0 {
			targets = resolveTracks(s, req.IDs)
		} else {
			for _, t := range s.all() {
				if needsIdentify(t) {
					targets = append(targets, t)
				}
			}
		}
		if len(targets) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"jobId": "", "total": 0})
			return
		}

		job := js.runProgressJob("identify", len(targets), func(j *scrapeJob, i int) scrapeResult {
			// 首次识别时在任务内异步构建全库指纹缓存（POST 立即返回，
			// 前端进度显示"构建指纹库…"；构建完成后才开始识别）
			if i == 0 {
				j.mu.Lock()
				j.Current = "构建指纹库…"
				j.mu.Unlock()
				buildIdentifyBase(s, fp)
			}
			t := targets[i]
			j.mu.Lock()
			j.Current = t.FileName
			j.mu.Unlock()
			compressed, rawFp, err := fpForTrack(fp, ffBin, t)
			if err != nil {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Message: err.Error()}
			}
			res := identifyTrack(s, ms, t, compressed, rawFp, identifyBase)
			if res.OK {
				return scrapeResult{ID: t.ID, FileName: t.FileName, OK: true, Message: res.Message}
			}
			// 未匹配/无标题：中性跳过，不算失败、不标红；仅写标签失败等真错误计入失败。
			skip := strings.Contains(res.Message, "未匹配") || strings.Contains(res.Message, "无标题")
			return scrapeResult{ID: t.ID, FileName: t.FileName, OK: false, Skip: skip, Message: res.Message}
		})
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}

// identifyTrack 是单曲识别主流程（文档流水线）：
//  1. AcoustID 指纹查询（3 QPS）→ 命中后 MusicBrainz Recording 补全（1 QPS）
//     → 写标签 → 重命名；
//  2. AcoustID 未配置 Key 或未命中 → 回退库内已标注曲目指纹匹配。
//
// fpComp 为 AcoustID 需要的压缩指纹；fpRaw 为库内匹配用的 raw 指纹。
func identifyTrack(s *store, ms *scrape.MultiSource, t *model.Track, fpComp, fpRaw string, base map[string]string) identifyResult {
	// 主路径：AcoustID → MusicBrainz
	if rec, aerr := ms.Client().LookupByFingerprint(fpComp, int(t.Duration)); aerr == nil && rec.Title != "" && len(rec.Artists) > 0 {
		var det *scrape.RecordingDetail
		if d, derr := ms.Client().LookupRecording(rec.MBID); derr == nil {
			det = d
			slog.Info("identify: acoustid+musicbrainz", "file", t.FileName, "mbid", rec.MBID, "album", d.Album)
		} else {
			slog.Warn("identify: musicbrainz recording lookup failed", "file", t.FileName, "mbid", rec.MBID, "err", derr)
		}
		return writeIdentifyFull(s, ms, t, rec, det)
	} else if aerr != nil {
		slog.Warn("identify: acoustid lookup failed", "file", t.FileName, "err", aerr)
	} else {
		slog.Warn("identify: acoustid no usable recording", "file", t.FileName)
	}
	// 回退：库内指纹匹配（识别准确、无需外部 Key）
	slog.Info("identify: fallback library match", "file", t.FileName)
	return matchAndWrite(s, ms, t, fpRaw, base)
}

// matchAndWrite finds the best library fingerprint match, copies tags and
// fetches lyrics for a track. It returns the per-track result.
func matchAndWrite(s *store, ms *scrape.MultiSource, t *model.Track, fp string, base map[string]string) identifyResult {
	bestID, bestSim := "", 0.0
	for id, f := range base {
		if id == t.ID {
			continue
		}
		if sim := finger.Similarity(fp, f); sim > bestSim {
			bestID, bestSim = id, sim
		}
	}
	const threshold = 0.85
	var title, artist string
	var src *model.Track
	if bestID != "" && bestSim >= threshold {
		src = s.get(bestID)
		if src != nil {
			title = strings.TrimSpace(src.Tags["TITLE"])
			artist = strings.TrimSpace(src.Tags["ARTIST"])
		}
	}
	if src == nil {
		msg := "音频内容未匹配到库内曲目（AcoustID 未配置 Key 或未命中）"
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: msg}
	}
	if title == "" {
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "匹配来源无标题"}
	}
	// 与 AcoustID 路径一致：写标签统一转简体
	title = simpConverter(title)
	artist = simpConverter(artist)
	extra := map[string]string{}
	for k, srcKey := range map[string]string{
		taglibx.Album:       "ALBUM",
		taglibx.AlbumArtist: "ALBUMARTIST",
		taglibx.Genre:       "GENRE",
		taglibx.Date:        "DATE",
		taglibx.TrackNumber: "TRACKNUMBER",
	} {
		if v := simpConverter(strings.TrimSpace(src.Tags[srcKey])); v != "" {
			extra[k] = v
		}
	}
	artists := []string(nil)
	if artist != "" {
		artists = []string{artist}
	}
	return finalizeIdentify(s, ms, t, title, artists, extra)
}

// writeIdentifyFull 按 AcoustID 命中（+ MusicBrainz Recording 补全）写标签、
// 抓歌词并按"艺术家 - 标题.后缀"重命名。
func writeIdentifyFull(s *store, ms *scrape.MultiSource, t *model.Track, rec scrape.RecordingResult, det *scrape.RecordingDetail) identifyResult {
	title := strings.TrimSpace(rec.Title)
	artists := rec.Artists
	album, albumArtist, date, genre, trackNo := "", "", "", "", ""
	if det != nil {
		if len(det.Artists) > 0 {
			artists = det.Artists
		}
		album = det.Album
		albumArtist = strings.Join(det.AlbumArtist, " / ")
		date = det.Date
		if len(det.Genres) > 0 {
			genre = det.Genres[0]
		}
		trackNo = det.TrackNumber
	}
	if title == "" || len(artists) == 0 {
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "识别结果无标题/无艺术家"}
	}
	// MusicBrainz 原始数据常含繁体（如「圓」），识别写入统一转简体
	title = simpConverter(title)
	album = simpConverter(album)
	albumArtist = simpConverter(albumArtist)
	genre = simpConverter(genre)
	for i := range artists {
		artists[i] = simpConverter(artists[i])
	}
	extra := map[string]string{
		taglibx.Album:       album,
		taglibx.AlbumArtist: albumArtist,
		taglibx.Genre:       genre,
		taglibx.Date:        date,
		taglibx.TrackNumber: trackNo,
	}
	return finalizeIdentify(s, ms, t, title, artists, extra)
}

// finalizeIdentify 识别成功后的公共收尾（AcoustID 主路径与库内回退共用）：
// 写标签 → 抓歌词 → 刷新列表 → 按「艺术家 - 标题.后缀」重命名。
func finalizeIdentify(s *store, ms *scrape.MultiSource, t *model.Track, title string, artists []string, extra map[string]string) identifyResult {
	tags := map[string][]string{taglibx.Title: {title}}
	if len(artists) > 0 {
		tags[taglibx.Artist] = artists
	}
	for k, v := range extra {
		if strings.TrimSpace(v) != "" {
			tags[k] = []string{v}
		}
	}
	if err := taglibx.SaveTagsAtomic(t.Path, tags, false); err != nil {
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "写标签失败: " + err.Error()}
	}
	sr := scrape.SearchResult{Title: title, Artists: artists}
	if lyric, err := ms.FetchLyrics(sr); err == nil && strings.TrimSpace(lyric) != "" {
		taglibx.SaveTagsAtomic(t.Path, map[string][]string{taglibx.Lyrics: {lyric}}, false)
	}
	refreshTrack(s, t.Path)
	artistLabel := strings.Join(artists, " / ")
	newPath := renameFileToArtistTitle(s, t, artistLabel, title)
	res := identifyResult{ID: t.ID, FileName: t.FileName, OK: true, Title: title, Artist: artistLabel, Message: "已识别：" + title + " - " + artistLabel}
	if newPath != "" {
		res.NewID = s.byPathID(newPath)
	}
	return res
}

// trackIsDoubtful 判定曲目"存疑"：文件名艺术家为占位（Unknown/未知/track01）、
// 标签艺术家为占位/乱码/空、或无有效标题。这类文件在配置了 AcoustID Key 时，
// 批量刮削/一键刮削会先走音频识别（指纹 → AcoustID → MusicBrainz → 写标签
// → 重命名），再进入刮削，避免"Unknown - 歌名"这类错误艺术家被源误配。
func trackIsDoubtful(t *model.Track) bool {
	fa, _ := parseFileNameArtistTitle(t.FileName)
	artist := strings.TrimSpace(t.Tags[taglibx.Artist])
	title := strings.TrimSpace(t.Tags[taglibx.Title])
	if fa == "" || looksUnknown(fa) {
		return true
	}
	if artist == "" || looksUnknown(artist) || looksGarbled(artist) {
		return true
	}
	if title == "" || looksUnknown(title) || looksGarbled(title) {
		return true
	}
	return false
}

// identifyDoubtfulTrack 对单首存疑曲目执行音频识别（AcoustID 主路径 +
// 库内指纹回退）。供批量刮削/一键刮削在配置 AcoustID Key 时调用；
// 未命中时返回 OK=false，调用方继续走刮削兜底。
func identifyDoubtfulTrack(s *store, ms *scrape.MultiSource, t *model.Track, cfg *cstore.Config) identifyResult {
	fp := &finger.Fpcalc{Bin: "fpcalc"}
	if !fp.Available() {
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: "音频识别引擎不可用（未安装 fpcalc）"}
	}
	buildIdentifyBase(s, fp)
	compressed, rawFp, err := fpForTrack(fp, ffmpegBin(cfg), t)
	if err != nil {
		return identifyResult{ID: t.ID, FileName: t.FileName, OK: false, Message: err.Error()}
	}
	return identifyTrack(s, ms, t, compressed, rawFp, identifyBase)
}
