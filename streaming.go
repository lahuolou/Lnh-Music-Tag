// 统一实时流处理：直传优先，按客户端要求的格式/码率用 ffmpeg 管道实时转码。
// 供 Subsonic / Navidrome / Jellyfin / Emby 四类流端点复用。
package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

// bitRateParam 兼容标准驼峰 maxBitRate 与部分客户端小写 maxBitrate。
// URL query key 大小写敏感，只取小写会漏掉标准参数。
func bitRateParam(q url.Values) string {
	if v := q.Get("maxBitRate"); v != "" {
		return v
	}
	return q.Get("maxBitrate")
}

// encCache 按 ffmpeg 二进制路径缓存其可用音频编码器，避免每次流都跑 -encoders。
var (
	encMu    sync.Mutex
	encCache = map[string]encCacheEntry{}
)

// encProbeTimeout 单次编码器探测超时：异常二进制（权限/动态链接卡死）时
// 3 秒内返回失败而不是永久阻塞，避免整个流媒体服务在 availableEncoders 上排队。
const encProbeTimeout = 3 * time.Second

// encCacheTTL 编码器探测结果的有效期：成功结果 10 分钟内复用；
// 替换二进制后最多 10 分钟自动更新，不会永久固化旧结果。
const encCacheTTL = 10 * time.Minute

type encCacheEntry struct {
	m  map[string]bool
	at time.Time
}

// availableEncoders 解析 `ffmpeg -encoders`，返回该二进制实际支持的音频编码器集合。
func availableEncoders(bin string) map[string]bool {
	encMu.Lock()
	defer encMu.Unlock()
	if e, ok := encCache[bin]; ok && time.Since(e.at) < encCacheTTL {
		return e.m
	}
	m := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), encProbeTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "-hide_banner", "-encoders").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			for _, tok := range strings.Fields(ln) {
				if tok == "aac" || strings.Contains(tok, "mp3") || strings.Contains(tok, "opus") || strings.Contains(tok, "flac") {
					m[tok] = true
				}
			}
		}
		encCache[bin] = encCacheEntry{m: m, at: time.Now()}
	}
	// 探测失败不写缓存（返回空集合），下次请求重新探测，避免把失败结果永久固化
	return m
}

// pickCodec 为目标格式挑选一个当前 ffmpeg 真实支持的编码器；都不支持返回空。
// 偏好链：优先常见高质量编码器，回退到平台内置编码器。
func pickCodec(bin, format string) string {
	av := availableEncoders(bin)
	chain := map[string][]string{
		"mp3":  {"libmp3lame", "mp3_mf", "mp3"},
		"aac":  {"aac"},
		"opus": {"libopus", "opus"},
	}[format]
	for _, c := range chain {
		if av[c] {
			return c
		}
	}
	return ""
}

// transcodeTarget 描述客户端请求的输出格式。
type transcodeTarget struct {
	format    string // mp3 | aac | opus
	mime      string
	ext       string
	codec     string
	container string // ffmpeg -f 参数
	bitrate   int    // kbps
}

// parseTranscode 从请求参数解析目标格式；want 为空或与源一致时返回 nil（直传）。
func parseTranscode(t *model.Track, want string, maxBR string) *transcodeTarget {
	want = strings.ToLower(strings.TrimSpace(want))
	switch want {
	case "mp3", "mpeg":
		want = "mp3"
	case "aac", "m4a", "mp4":
		want = "aac"
	case "opus", "ogg", "ogopus":
		want = "opus"
	}
	if want == "" {
		return nil
	}
	// 源格式已满足，无需转码
	src := strings.TrimPrefix(strings.ToLower(t.Ext), ".")
	if src == want || (want == "aac" && (src == "m4a" || src == "aac")) {
		return nil
	}
	br := 192
	if v, err := strconv.Atoi(strings.TrimSpace(maxBR)); err == nil && v > 0 {
		br = v
	}
	tg := &transcodeTarget{bitrate: br}
	switch want {
	case "mp3":
		tg.format, tg.mime, tg.ext, tg.codec, tg.container = "mp3", "audio/mpeg", "mp3", "libmp3lame", "mp3"
	case "aac":
		tg.format, tg.mime, tg.ext, tg.codec, tg.container = "aac", "audio/aac", "aac", "aac", "adts"
	case "opus":
		tg.format, tg.mime, tg.ext, tg.codec, tg.container = "opus", "audio/ogg", "opus", "libopus", "ogg"
	default:
		return nil // 未知格式不转码，直传
	}
	return tg
}

// serveDirect 直传原始文件，支持 HTTP Range。
func serveDirect(w http.ResponseWriter, r *http.Request, t *model.Track, attachment bool) {
	f, err := os.Open(t.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "media unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType(t.Ext))
	w.Header().Set("Accept-Ranges", "bytes")
	disposition := "inline"
	if attachment {
		disposition = "attachment"
		// 附件落盘场景一次性全量下发：忽略客户端 Range，强制 200 完整响应，
		// 避免部分下载器把 206 分段误判为失败而卡在 0%。
		r.Header.Del("Range")
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+urlEscape(t.FileName))
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
}

// serveTranscode 用 ffmpeg 实时转码并管道输出；不支持字节 Range，从头开始。
// 关键健壮性：先启动 ffmpeg 并读到首个有效输出后再写 200 响应头；若启动失败、
// 进程立即退出或零字节产出（解码/编码不支持该源），直接回退直传原始文件，
// 避免在已写 200 后吐出空流/截断流，被播放器判为“无法播放/下载失败”。
func serveTranscode(w http.ResponseWriter, r *http.Request, cfg *cstore.Config, t *model.Track, tg *transcodeTarget) {
	if !ffmpegActive(cfg) {
		serveDirect(w, r, t, false)
		return
	}
	bin := ffmpegBin(cfg)
	if bin == "" {
		serveDirect(w, r, t, false)
		return
	}
	// 选一个当前 ffmpeg 真实支持的编码器；都不支持则不启动必失败的命令，直接回退直传。
	codec := pickCodec(bin, tg.format)
	if codec == "" {
		serveDirect(w, r, t, false)
		return
	}
	br := strconv.Itoa(tg.bitrate)
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-threads", "2",
		"-i", t.Path,
		"-codec:a", codec,
		"-b:a", br + "k",
		"-map_metadata", "0",
		"-f", tg.container, "-",
	}
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		serveDirect(w, r, t, false)
		return
	}
	// 排空 stderr，避免错误缓冲写满导致 ffmpeg 阻塞
	stderr, _ := cmd.StderrPipe()
	go func() {
		if stderr != nil {
			_, _ = io.Copy(io.Discard, stderr)
		}
	}()
	if err := cmd.Start(); err != nil {
		serveDirect(w, r, t, false)
		return
	}

	// 先读首块：读到有效字节才确认转码可用；否则回退直传。
	first := make([]byte, 32*1024)
	n, rerr := stdout.Read(first)
	if n <= 0 || rerr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		serveDirect(w, r, t, false)
		return
	}

	w.Header().Set("Content-Type", tg.mime)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+urlEscape(stripExt(t.FileName)+"."+tg.ext))
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	writeChunk := func(b []byte) bool {
		if _, werr := w.Write(b); werr != nil {
			_ = cmd.Process.Kill()
			return false
		}
		if canFlush {
			flusher.Flush()
		}
		return true
	}
	if !writeChunk(first[:n]) {
		_ = cmd.Wait()
		return
	}
	buf := make([]byte, 32*1024)
	for {
		m, rerr := stdout.Read(buf)
		if m > 0 && !writeChunk(buf[:m]) {
			_ = cmd.Wait()
			return
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = cmd.Process.Kill()
			break
		}
	}
	_ = cmd.Wait()
}

// serveHLS 以 MPEG-TS 容器实时输出音频分片（供 Emby/HLS main.m3u8 客户端）。
// 音频统一转 mp3 后封装进 mpegts；ffmpeg 不可用时回退直传。
func serveHLS(w http.ResponseWriter, r *http.Request, cfg *cstore.Config, t *model.Track, bitrate int) {
	if !ffmpegActive(cfg) {
		serveDirect(w, r, t, false)
		return
	}
	bin := ffmpegBin(cfg)
	if bin == "" {
		serveDirect(w, r, t, false)
		return
	}
	if bitrate <= 0 {
		bitrate = 320
	}
	codec := pickCodec(bin, "mp3")
	if codec == "" {
		serveDirect(w, r, t, false)
		return
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", t.Path,
		"-c:a", codec,
		"-b:a", strconv.Itoa(bitrate) + "k",
		"-f", "mpegts", "-",
	}
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		serveDirect(w, r, t, false)
		return
	}
	if err := cmd.Start(); err != nil {
		serveDirect(w, r, t, false)
		return
	}
	w.Header().Set("Content-Type", "video/MP2T")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				_ = cmd.Process.Kill()
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = cmd.Process.Kill()
			break
		}
	}
	_ = cmd.Wait()
}

// streamTrack 是四协议流端点的统一入口：直传或转码。
// FFmpeg 插件未开启/未安装时一律直传原始文件，不中断第三方播放器播放。
func streamTrack(w http.ResponseWriter, r *http.Request, cfg *cstore.Config, t *model.Track, wantFormat, maxBitrate string, attachment bool) {
	tg := parseTranscode(t, wantFormat, maxBitrate)
	if tg == nil || !ffmpegActive(cfg) {
		serveDirect(w, r, t, attachment)
		return
	}
	serveTranscode(w, r, cfg, t, tg)
}

func stripExt(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return name
}
