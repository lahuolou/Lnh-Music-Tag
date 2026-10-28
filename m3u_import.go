package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	cstore "lnh-music-tag/internal/store"
)

// m3uPlaylistPrefix 标记由 .m3u/.m3u8 导入的网关歌单 ID 前缀。
// 每次扫描全量重建该前缀歌单：歌单文件删除/改名后网关歌单随之消失，
// 不会残留幽灵歌单；手工通过网关创建的歌单（pl- 前缀）不受影响。
const m3uPlaylistPrefix = "m3u-"

// importM3UPlaylists 扫描音乐库根目录（含子目录）中的 .m3u/.m3u8 歌单文件，
// 解析其中的曲目并导入为网关歌单（写入 streaming_playlists 配置，
// Subsonic / Navidrome / Jellyfin 三协议共享，"服务器歌单"即可看到）。
//
// 幂等：每次调用都按当前文件集合重建 m3u- 前缀歌单——歌单文件删除/改名后
// 网关歌单随之消失，不会残留幽灵歌单；无 .m3u 文件时同样清理旧导入。
// 手工通过网关创建的歌单（pl- 前缀）不受影响。返回本次导入的歌单数量。
//
// 应在 scanDirLive 与 organizeLibrary 之后调用：曲目已入库、路径已是
// 整理后的最终形态，才能按路径匹配到曲目 ID。
func importM3UPlaylists(s *store, cfg *cstore.Config, dir string) int {
	if s == nil || cfg == nil || dir == "" {
		return 0
	}
	files := m3uFiles(dir)
	now := time.Now().UTC().Format(time.RFC3339)
	var imported []subsonicPlaylist
	for _, f := range files {
		ids := m3uTrackIDs(s, f, dir)
		if len(ids) == 0 {
			continue // 歌单没有可匹配的本地曲目：跳过，避免空歌单污染列表
		}
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		imported = append(imported, subsonicPlaylist{
			ID:       stableID(m3uPlaylistPrefix, name),
			Name:     name,
			Created:  now,
			Changed:  now,
			TrackIDs: ids,
		})
	}
	// 重建合并：移除旧的 m3u- 歌单，保留手工创建的 pl- 歌单
	oldJSON := cfg.GetDefault("streaming_playlists", "[]")
	var kept []subsonicPlaylist
	if err := json.Unmarshal([]byte(oldJSON), &kept); err != nil {
		kept = nil
	}
	out := make([]subsonicPlaylist, 0, len(kept)+len(imported))
	for _, p := range kept {
		if !strings.HasPrefix(p.ID, m3uPlaylistPrefix) {
			out = append(out, p)
		}
	}
	out = append(out, imported...)
	b, err := json.Marshal(out)
	if err != nil {
		log.Printf("m3u 歌单序列化失败: %v", err)
		return 0
	}
	// 无变化（JSON 相同）不写库，避免每次扫描无谓的配置写入；
	// 仍返回当前存在的 m3u 导入歌单数（幂等报告一致）
	if string(b) == oldJSON {
		return len(imported)
	}
	if err := cfg.Set("streaming_playlists", string(b)); err != nil {
		log.Printf("m3u 歌单保存失败: %v", err)
		return 0
	}
	return len(imported)
}

// m3uFiles 递归收集 dir 下所有 .m3u/.m3u8 文件，
// 与扫描同规则跳过隐藏目录/文件及 NAS 系统目录。
func m3uFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可读条目
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || skipScanDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".m3u", ".m3u8":
			out = append(out, path)
		}
		return nil
	})
	return out
}

// m3uTrackIDs 解析单个 .m3u 歌单文件，返回可匹配到本地曲库的曲目 ID 列表
// （保持歌单内出现顺序，去重）。
func m3uTrackIDs(s *store, file, root string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var ids []string
	base := filepath.Dir(file)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // #EXTM3U / #EXTINF / #PLAYLIST 等注释与元信息行
		}
		// 远程 URL 条目（http/https/mms）不支持，跳过
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "mms://") {
			continue
		}
		line = strings.Trim(line, `"`)
		if id := s.pathIDFold(m3uCandidates(line, base, root)); id != "" {
			if !slicesContains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// m3uCandidates 生成 m3u 条目的候选绝对路径：
// 绝对路径原样；相对路径分别相对歌单文件目录与音乐库根目录解析。
func m3uCandidates(entry, base, root string) []string {
	if filepath.IsAbs(entry) {
		return []string{filepath.Clean(entry)}
	}
	cands := []string{filepath.Clean(filepath.Join(base, entry))}
	if r := filepath.Clean(filepath.Join(root, entry)); r != cands[0] {
		cands = append(cands, r)
	}
	return cands
}

// pathIDFold 按候选路径解析曲目 ID：先精确匹配（byPath），
// 再对曲库全表做大小写不敏感匹配（Windows 磁盘 / m3u 中路径大小写常不一致）。
// 仅 m3u 导入等小规模场景使用（全表扫描兜底）。
func (s *store) pathIDFold(cands []string) string {
	for _, c := range cands {
		if id := s.byPathID(c); id != "" {
			return id
		}
	}
	cl := make([]string, 0, len(cands))
	for _, c := range cands {
		cl = append(cl, strings.ToLower(filepath.Clean(c)))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for p, id := range s.byPath {
		lp := strings.ToLower(p)
		for _, c := range cl {
			if lp == c {
				return id
			}
		}
	}
	return ""
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
