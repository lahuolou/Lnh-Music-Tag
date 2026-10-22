package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
)

// lyricsCacheTTL 歌词短 TTL：播放器/歌词页高频重复请求同一曲目时，
// 避免每次重新读 .lrc 文件；30 秒自动过期重读（用户编辑 LRC 后短时内
// 仍是旧值，可接受——TTL 与文档建议的 30–60 秒一致）。
const lyricsCacheTTL = 30 * time.Second

var (
	lyricsMu    sync.Mutex
	lyricsCache = map[string]lyricsEntry{}
)

type lyricsEntry struct {
	text string
	at   time.Time
}

// cachedLyrics 返回曲目歌词文本：内嵌 LYRICS 标签优先，其次同名 .lrc 文件。
// 结果带短 TTL 缓存（键=曲目 ID），四协议网关（Subsonic/Navidrome/Jellyfin/Emby）
// 的歌词端点共用，避免同一首歌在一次播放会话中被反复读盘。
func cachedLyrics(t *model.Track) string {
	if t == nil {
		return ""
	}
	lyricsMu.Lock()
	if e, ok := lyricsCache[t.ID]; ok && time.Since(e.at) < lyricsCacheTTL {
		text := e.text
		lyricsMu.Unlock()
		return text
	}
	lyricsMu.Unlock()
	text := ""
	if v := tag(t, "LYRICS", "UNSYNCEDLYRICS", "UNSYNCED LYRICS"); v != "" {
		text = v
	}
	if text == "" {
		b, err := os.ReadFile(strings.TrimSuffix(t.Path, filepath.Ext(t.Path)) + ".lrc")
		if err == nil {
			text = string(b)
		}
	}
	text = strings.TrimSpace(text)
	lyricsMu.Lock()
	lyricsCache[t.ID] = lyricsEntry{text: text, at: time.Now()}
	lyricsMu.Unlock()
	return text
}
