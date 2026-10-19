// 统一实时流处理：直传优先，按客户端要求的格式/码率用 ffmpeg 管道实时转码。
// 供 Subsonic / Navidrome / Jellyfin / Emby 四类流端点复用。
package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

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
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+urlEscape(t.FileName))
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
}

// serveTranscode 用 ffmpeg 实时转码并管道输出；不支持字节 Range，从头开始。
// ffmpeg 不可用或启动失败时回退直传。
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
	br := strconv.Itoa(tg.bitrate)
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", t.Path,
		"-codec:a", tg.codec,
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
	if err := cmd.Start(); err != nil {
		serveDirect(w, r, t, false)
		return
	}
	w.Header().Set("Content-Type", tg.mime)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+urlEscape(stripExt(t.FileName)+"."+tg.ext))
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
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", t.Path,
		"-c:a", "libmp3lame",
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
