// Package taglibx wraps go.senan.xyz/taglib (WASM TagLib) to read and write
// audio metadata tags and embedded cover art across many formats, with no CGo.
package taglibx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.senan.xyz/taglib"

	"lnh-music-tag/internal/model"
)

// ReadTrack reads tags, audio properties and cover presence for a single file
// and returns a model.Track with a SHA256 hash precomputed (expensive: reads
// the whole file). Prefer ReadTrackLight for scanning.
func ReadTrack(path string) (*model.Track, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	tags, err := taglib.ReadTags(path)
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	props, err := taglib.ReadProperties(path)
	if err != nil {
		return nil, fmt.Errorf("read properties: %w", err)
	}

	hash, err := sha256File(path)
	if err != nil {
		return nil, err
	}

	tr := trackFrom(path, st, tags, props)
	tr.SHA256 = hash
	return tr, nil
}

// ReadTrackLight reads tags, audio properties and cover presence WITHOUT
// hashing the file. It is the fast path used by scanning: a full-content
// SHA256 for a large FLAC costs a full disk read per file and dominates scan
// time. Hashing is deferred to the dedup page (see /api/duplicates), which
// computes and caches it on demand.
func ReadTrackLight(path string) (*model.Track, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	tags, err := taglib.ReadTags(path)
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	props, err := taglib.ReadProperties(path)
	if err != nil {
		return nil, fmt.Errorf("read properties: %w", err)
	}
	return trackFrom(path, st, tags, props), nil
}

func trackFrom(path string, st os.FileInfo, tags map[string][]string, props taglib.Properties) *model.Track {
	tr := &model.Track{
		Path:       path,
		FileName:   filepath.Base(path),
		Ext:        filepath.Ext(path),
		Size:       st.Size(),
		Duration:   props.Length.Seconds(),
		Bitrate:    props.BitRate,
		SampleRate: props.SampleRate,
		BitDepth:   props.BitDepth,
		Channels:   props.Channels,
		HasCover:   len(props.Images) > 0,
		HasLrcFile: LrcPath(path) != "",
		Tags:       flatten(tags),
	}
	tr.HasLyrics = strings.TrimSpace(tr.Tags["LYRICS"]) != ""
	tr.ID = pathID(path)
	tr.Quality = tr.ComputeQuality()
	return tr
}

// LrcPath returns the sibling .lrc lyric file path for an audio file
// (case-insensitive), or "" if none exists. Chinese music libraries commonly
// keep lyrics as external .lrc files next to the audio.
func LrcPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	dir := filepath.Dir(path)
	for _, ext := range []string{".lrc", ".LRC", ".Lrc", ".lRC"} {
		p := filepath.Join(dir, base+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// pathID derives a stable, unique-per-path id (unlike SHA256, which groups
// identical files together — that grouping belongs to dedup, not identity).
func pathID(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:8])
}

// ReadCover returns the first embedded image as bytes (nil if none).
func ReadCover(path string) ([]byte, error) {
	img, err := taglib.ReadImage(path)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// WriteTags writes metadata tags. If clear is true, tags not present in tagsMap
// are removed. Values are multi-valued (slice) for fields like artist.
func WriteTags(path string, tagsMap map[string][]string, clear bool) error {
	opts := taglib.WriteOption(0)
	if clear {
		opts = taglib.Clear
	}
	return taglib.WriteTags(path, tagsMap, opts)
}

// WriteCover embeds imageBytes as front cover (index 0). Pass nil to clear.
func WriteCover(path string, imageBytes []byte) error {
	return taglib.WriteImage(path, imageBytes)
}

// ---- 原子写：临时副本 → 写入 → 校验 → 替换（保护用户原始音频） ----

// SaveTagsAtomic 以「同目录临时副本 → 写标签 → 重读校验 → 原子替换」的方式
// 安全写标签。taglib 底层会原地改写原文件，进程中断（断电/崩溃/磁盘满）可能
// 损坏用户原始音频；本函数先写临时副本（同文件系统，rename 才是原子的），
// 校验可解析后再替换，替换失败自动恢复原文件，任何阶段失败原文件都保持完整。
func SaveTagsAtomic(path string, tagsMap map[string][]string, clear bool) error {
	tmpName, cleanup, err := makeTempCopy(path)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := WriteTags(tmpName, tagsMap, clear); err != nil {
		return fmt.Errorf("write tags: %w", err)
	}
	// 校验：重读临时文件确认可解析（损坏的标签区/音频帧会导致解析失败）
	if _, err := taglib.ReadTags(tmpName); err != nil {
		return fmt.Errorf("verify temp file: %w", err)
	}
	if err := replaceFile(path, tmpName); err != nil {
		return fmt.Errorf("replace original: %w", err)
	}
	return nil
}

// SaveCoverAtomic 与 SaveTagsAtomic 同理：封面（图片数据）写入也走临时副本+校验+替换。
func SaveCoverAtomic(path string, imageBytes []byte) error {
	tmpName, cleanup, err := makeTempCopy(path)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := WriteCover(tmpName, imageBytes); err != nil {
		return fmt.Errorf("write cover: %w", err)
	}
	if _, err := taglib.ReadTags(tmpName); err != nil {
		return fmt.Errorf("verify temp file: %w", err)
	}
	if err := replaceFile(path, tmpName); err != nil {
		return fmt.Errorf("replace original: %w", err)
	}
	return nil
}

// makeTempCopy copies path to a same-directory temp file (same filesystem so the
// final rename is atomic) and returns its name plus a cleanup closure.
func makeTempCopy(path string) (string, func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("make path abs: %w", err)
	}
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".lnh-tmp-*"+filepath.Ext(abs))
	if err != nil {
		return "", nil, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	if err := copyFile(abs, tmpName); err != nil {
		os.Remove(tmpName)
		return "", nil, fmt.Errorf("copy original: %w", err)
	}
	return tmpName, func() { os.Remove(tmpName) }, nil
}

// copyFile copies src to dst, preserving the source file's permission bits.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// replaceFile atomically swaps src into dst: dst is first renamed to a unique
// backup (Windows rename does not overwrite), then src is moved in; on failure
// the original is restored. The backup is removed only after a successful swap.
func replaceFile(dst, src string) error {
	bak := fmt.Sprintf("%s.lnh-bak-%d", dst, time.Now().UnixNano())
	if err := os.Rename(dst, bak); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(bak, dst) // 替换失败：恢复原文件
		return err
	}
	_ = os.Remove(bak)
	return nil
}

// Useful tag key aliases re-exported for convenience.
const (
	Title       = taglib.Title
	Artist      = taglib.Artist
	Album       = taglib.Album
	AlbumArtist = taglib.AlbumArtist
	Genre       = taglib.Genre
	Date        = taglib.Date
	TrackNumber = taglib.TrackNumber
	DiscNumber  = taglib.DiscNumber
	Lyrics      = taglib.Lyrics
	Comment     = taglib.Comment
	Composer    = taglib.Composer
	Lyricist    = taglib.Lyricist
	Language    = taglib.Language
	ReleaseType = taglib.ReleaseType // 专辑类型（album/single/ep/compilation…）
	Length      = taglib.Length      // 时长（属性值写入标签）

	// 增益/峰值与总数：TagLib 无内置常量，使用跨格式通用自定义键
	// （FLAC/Vorbis 写为 VorbisComment，MP3 写为 TXXX，MP4 写为 iTunes 原子）。
	ReplayGainTrackGain = "REPLAYGAIN_TRACK_GAIN"
	ReplayGainTrackPeak = "REPLAYGAIN_TRACK_PEAK"
	ReplayGainAlbumGain = "REPLAYGAIN_ALBUM_GAIN"
	ReplayGainAlbumPeak = "REPLAYGAIN_ALBUM_PEAK"
	TrackTotal          = "TRACKTOTAL"
	DiscTotal           = "DISCTOTAL"

	// 音频属性键：勾选后把探测值写入标签（自定义键），播放器可读。
	Bitrate    = "BITRATE"
	SampleRate = "SAMPLERATE"
	BitDepth   = "BITDEPTH"
	Channels   = "CHANNELS"
	FileSize   = "FILESIZE"

	MBTrackID        = taglib.MusicBrainzTrackID
	MBAlbumID        = taglib.MusicBrainzAlbumID
	MBArtistID       = taglib.MusicBrainzArtistID
	MBReleaseGroupID = taglib.MusicBrainzReleaseGroupID
	MBReleaseTrackID = taglib.MusicBrainzReleaseTrackID
)

// SortedTagKeys returns the standardized keys present in a read tags map,
// sorted, excluding internal/technical keys, useful for UI rendering.
var technicalKeys = map[string]bool{
	"FILE": true, "FILETYPE": true, "LENGTH": true, "ORIGINALFILENAME": true,
	"METADATA_BLOCK_PICTURE": true,
}

// SortedTagKeys lists non-technical keys.
func SortedTagKeys(tags map[string][]string) []string {
	var out []string
	for k := range tags {
		if !technicalKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func flatten(tags map[string][]string) map[string]string {
	out := make(map[string]string, len(tags))
	for k, vs := range tags {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// HashFile returns the SHA256 hex digest of a file's full content. Used by the
// dedup page for on-demand hashing; expensive for large files, so it is never
// called during scanning.
func HashFile(path string) (string, error) {
	return sha256File(path)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
