// organize.go：按专辑/歌曲名存放整理——扫描/刮削完成后把曲目移动到音乐库根目录
// 下的「专辑名/」或「歌曲名/」子目录（文件名保持不变，同目录 .lrc 外挂歌词跟随
// 移动）。三种模式由后台统一设置（organize_mode）：off=不整理（保持原目录，
// 默认）/ album=按专辑名 / title=按歌曲名。
// 幂等：已就位（目标路径与现路径相同）直接跳过；同名目标已存在时不覆盖。
package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

// organizeMode 整理方式：off=不整理 / album=按专辑名 / title=按歌曲名。
type organizeMode string

const (
	organizeOff   organizeMode = "off"
	organizeAlbum organizeMode = "album"
	organizeTitle organizeMode = "title"
)

// cleanDirName 清洗 Windows/路径非法字符与路径分隔符，避免名称破坏目录层级。
func cleanDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// organizeDirName 按模式返回目标子目录名：off 或标签无效（空/占位）返回 ""。
func organizeDirName(mode organizeMode, t *model.Track) string {
	var name string
	switch mode {
	case organizeAlbum:
		name = t.Tags["ALBUM"]
	case organizeTitle:
		name = t.Tags["TITLE"]
	default:
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	switch strings.ToLower(name) {
	case "various", "various artists", "unknown", "unknown album", "untitled", "单曲", "未知专辑":
		return ""
	}
	return cleanDirName(name)
}

// moveAcrossVol 跨卷移动：复制到目标后删除源文件（源删除带瞬时锁重试，
// 与 moveToTrash 同口径：Windows 上未释放的读句柄会阻塞 os.Remove）。
func moveAcrossVol(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	out.Close()
	in.Close() // 关键：删除源文件前释放读句柄
	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			time.Sleep(1 * time.Second)
		}
		if err := os.Remove(src); err == nil {
			return nil
		}
	}
	os.Remove(dst)
	return errors.New("跨卷移动：复制完成但删除源文件失败（文件锁占用）")
}

// moveSiblingLrc 把同目录同名 .lrc 外挂歌词移到目标旁（尽力而为，失败不阻断）。
func moveSiblingLrc(oldPath, newPath string) {
	ext := filepath.Ext(oldPath)
	lrc := strings.TrimSuffix(oldPath, ext) + ".lrc"
	if !fileExists(lrc) {
		return
	}
	dst := strings.TrimSuffix(newPath, filepath.Ext(newPath)) + ".lrc"
	if !fileExists(dst) {
		_ = os.Rename(lrc, dst)
	}
}

// organizeTrack 按模式整理单首曲目：移动到 root/<目录名>/<原文件名>，已就位返回 false。
// 同卷 rename（Windows 瞬时锁重试 8s 窗口），跨卷 copy+remove；成功后经
// renameTrack 同步更新内存与数据库路径（列表/勾选/去重索引不失效）。
func organizeTrack(s *store, t *model.Track, root string, mode organizeMode) (bool, error) {
	root = strings.TrimSpace(root)
	// 库根必须是已存在的目录（fileExists 排除目录，这里用 Stat 判断）
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return false, nil // 库根未配置或不存在，跳过整理
	}
	dir := organizeDirName(mode, t)
	if dir == "" {
		return false, nil
	}
	target := filepath.Join(root, dir, t.FileName)
	if filepath.Clean(target) == t.Path {
		return false, nil // 已就位
	}
	// 目标已存在（文件或同名目录）即冲突：不覆盖、不合并
	if _, err := os.Stat(target); err == nil {
		return false, fmt.Errorf("目标已存在，跳过（%s）", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	if filepath.VolumeName(t.Path) == filepath.VolumeName(target) {
		var re error
		for attempt := 0; attempt < 8; attempt++ {
			if attempt > 0 {
				time.Sleep(1 * time.Second)
			}
			if err := os.Rename(t.Path, target); err == nil {
				moveSiblingLrc(t.Path, target)
				renameTrack(s, t, target)
				return true, nil
			} else {
				re = err
			}
		}
		_ = re // 忽略最终失败，走下方跨卷兜底（copy+remove 同样带重试）
	}
	if err := moveAcrossVol(t.Path, target); err != nil {
		return false, err
	}
	moveSiblingLrc(t.Path, target)
	renameTrack(s, t, target)
	return true, nil
}

// organizeLibrary 全库按模式整理（幂等），返回移动数与失败数。
// 供扫描结束与刮削完成后调用；单首失败仅记录日志，不中断整体。
func organizeLibrary(s *store, root string, mode organizeMode, logf func(string)) (moved, failed int) {
	if s == nil || mode == organizeOff {
		return 0, 0
	}
	s.mu.RLock()
	ts := make([]*model.Track, 0, len(s.tracks))
	for _, t := range s.tracks {
		ts = append(ts, t)
	}
	s.mu.RUnlock()
	for _, t := range ts {
		ok, err := organizeTrack(s, t, root, mode)
		if err != nil {
			failed++
			if logf != nil {
				logf(fmt.Sprintf("整理失败 %s：%v", t.FileName, err))
			}
			continue
		}
		if ok {
			moved++
		}
	}
	return moved, failed
}

// organizeModeOf 读取配置的整理方式（organize_mode，默认 off=不整理）。
func organizeModeOf(cfg *cstore.Config) organizeMode {
	if cfg == nil {
		return organizeOff
	}
	return organizeMode(strings.TrimSpace(cfg.GetDefault("organize_mode", "off")))
}

// organizeScraped 刮削/一键流水线完成后按配置模式整理（幂等）。
// 整理根取 scan_dir 配置（音乐库根）；未配置或不存在时跳过。
func organizeScraped(s *store, cfg *cstore.Config) {
	if s == nil || cfg == nil {
		return
	}
	mode := organizeModeOf(cfg)
	if mode == organizeOff {
		return
	}
	root := strings.TrimSpace(cfg.GetDefault("scan_dir", ""))
	if root == "" {
		return
	}
	moved, failed := organizeLibrary(s, root, mode, func(m string) { log.Printf("整理: %s", m) })
	if moved > 0 || failed > 0 {
		log.Printf("刮削后整理完成（%s）：移动 %d，失败 %d", mode, moved, failed)
	}
}
