// Package model defines the core data structures shared across the app.
package model

import "strings"

// AudioExts lists the audio file extensions the scanner accepts.
var AudioExts = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".aac": true,
	".ogg": true, ".opus": true, ".wav": true, ".wma": true,
	".ape": true, ".mpc": true, ".aiff": true, ".mka": true,
}

// Track is a scanned audio file with its tags, audio properties and dedup info.
type Track struct {
	ID            string            `json:"id"`       // stable id: sha256[:16]
	Path          string            `json:"path"`     // absolute path on server
	FileName      string            `json:"fileName"` // base name
	Ext           string            `json:"ext"`
	Size          int64             `json:"size"`
	SHA256        string            `json:"sha256"`   // full file hash (dedup strategy 1)
	Duration      float64           `json:"duration"` // seconds
	Bitrate       uint              `json:"bitrate"`  // kbit/s
	SampleRate    uint              `json:"sampleRate"`
	BitDepth      uint              `json:"bitDepth"` // bits per sample（无损；有损为 0）
	Channels      uint              `json:"channels"`
	Quality       string            `json:"quality"`  // 流畅/普通/高品/无损/Hi-Res（自动识别）
	HasCover      bool              `json:"hasCover"`
	HasLrcFile    bool              `json:"hasLrcFile"`    // 同目录存在外挂 .lrc 歌词文件
	HasLyrics     bool              `json:"hasLyrics"`     // 内嵌歌词非空（列表瘦身：歌词正文不下发，仅标志）
	HasTrad       bool              `json:"hasTrad"`       // 文本标签含繁体字（OpenCC 检测）
	Garbled       bool              `json:"garbled"`       // 文本标签疑似乱码（GBK/UTF-8 错乱），扫描时判定
	NeedsIdentify bool              `json:"needsIdentify"` // 标签与文件名均无有效信息，需按音频内容识别
	Tags          map[string]string `json:"tags"`          // flattened: first value per standardized key
}

// DuplicateGroup groups tracks that share the same dedup key (sha256 or fingerprint).
type DuplicateGroup struct {
	Key    string   `json:"key"`
	Method string   `json:"method"` // "hash" | "format" | "tags"
	Count  int      `json:"count"`
	IDs    []string `json:"ids"`
	KeepID string   `json:"keepId,omitempty"` // "format" 组的保留者（音质最佳）
}

// losslessExts lists extensions that always represent lossless audio,
// used as a fallback when BitDepth was not probed (e.g. some WAV/AIFF).
var losslessExts = map[string]bool{
	".flac": true, ".ape": true, ".wav": true, ".aiff": true, ".mka": true,
}

// lossyExts lists lossy encodings. A track with one of these extensions is never
// classified as lossless even if metadata carries an anomalous BitDepth (which
// happens with corrupted/foreign tags) — the extension is the format authority.
var lossyExts = map[string]bool{
	".mp3": true, ".mp2": true, ".aac": true, ".m4a": true, ".m4b": true,
	".ogg": true, ".opus": true, ".wma": true, ".ac3": true,
}

// ComputeQuality classifies a track's audio quality from its probed properties.
// Lossless files (BitDepth > 0 with a non-lossy extension, or a lossless
// extension) are 无损/Hi-Res; lossy files are tiered by bitrate:
// <128k 流畅, <=192k 普通, >=224k 高品.
func (t *Track) ComputeQuality() string {
	ext := strings.ToLower(t.Ext)
	if (t.BitDepth > 0 && !lossyExts[ext]) || losslessExts[ext] {
		if t.SampleRate >= 96000 || t.BitDepth >= 24 {
			return "Hi-Res"
		}
		return "无损"
	}
	switch {
	case t.Bitrate == 0:
		return "未知"
	case t.Bitrate < 128:
		return "流畅"
	case t.Bitrate <= 192:
		return "普通"
	default:
		return "高品"
	}
}
