package main

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/taglibx"
)

func (n *navidromeAPI) listSongs(w http.ResponseWriter, r *http.Request) {
	all := append([]*model.Track{}, n.all()...)
	// Navidrome 过滤参数：starred=true 仅返回收藏歌曲
	if strings.EqualFold(r.URL.Query().Get("starred"), "true") {
		starred := map[string]bool{}
		for _, id := range n.loadStarred() {
			starred[id] = true
		}
		filtered := all[:0]
		for _, t := range all {
			if starred[t.ID] {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	// Navidrome 过滤参数（兼容下划线/驼峰）：album_id/albumId 仅返回该专辑歌曲；
	// artist_id/artistId 返回该艺术家名下歌曲（艺术家详情「歌曲」页）。
	aid := r.URL.Query().Get("album_id")
	if aid == "" {
		aid = r.URL.Query().Get("albumId")
	}
	if aid != "" {
		filtered := all[:0]
		for _, t := range all {
			if albumID(t) == aid {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	av := r.URL.Query().Get("artist_id")
	if av == "" {
		av = r.URL.Query().Get("artistId")
	}
	if av != "" {
		filtered := all[:0]
		for _, t := range all {
			if artistID(t) == av {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	field, desc := ndSortField(r)
	ndSortTracks(all, field)
	if desc {
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(all), start, end)
	data := make([]any, 0, end-start)
	for _, t := range all[start:end] {
		data = append(data, n.songJSON(t))
	}
	n.listJSON(w, r, data, len(all))
}
func (n *navidromeAPI) songOne(w http.ResponseWriter, r *http.Request, id string) {
	t := n.track(id)
	if t == nil {
		n.json(w, http.StatusNotFound, map[string]any{"error": "song not found", "ok": false})
		return
	}
	// 单曲详情缓存（30s）：同一首歌在播放队列/详情页被重复请求时复用序列化字节。
	key := "navidrome:song:" + id
	if b, ok := entityCacheGet(key); ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}
	b, _ := json.Marshal(n.songJSON(t))
	entityCachePut(key, b)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

// ---- album ----------------------------------------------------------------

func (n *navidromeAPI) listAlbums(w http.ResponseWriter, r *http.Request) {
	albums := n.albumGroups()
	field, desc := ndSortField(r)
	switch field {
	case "name":
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].name < albums[j].name })
	case "artist":
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].artist < albums[j].artist })
	case "year":
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].year < albums[j].year })
	case "random":
		rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })
	}
	if desc {
		for i, j := 0, len(albums)-1; i < j; i, j = i+1, j-1 {
			albums[i], albums[j] = albums[j], albums[i]
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(albums), start, end)
	data := make([]any, 0, end-start)
	for _, a := range albums[start:end] {
		data = append(data, n.albumJSON(a))
	}
	n.listJSON(w, r, data, len(albums))
}
func (n *navidromeAPI) albumOne(w http.ResponseWriter, r *http.Request, id string) {
	for _, a := range n.albumGroups() {
		if a.id == id {
			n.json(w, http.StatusOK, n.albumJSON(a))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "album not found", "ok": false})
}
func (n *navidromeAPI) albumSongs(w http.ResponseWriter, r *http.Request, id string) {
	for _, a := range n.albumGroups() {
		if a.id == id {
			data := make([]any, 0, len(a.songs))
			for _, t := range a.songs {
				data = append(data, n.songJSON(t))
			}
			n.listJSON(w, r, data, len(a.songs))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "album not found", "ok": false})
}
func (n *navidromeAPI) albumList(w http.ResponseWriter, r *http.Request, typ string) {
	albums := n.albumGroups()
	// random 也保持稳定顺序（与 Subsonic 网关一致）
	switch strings.ToLower(typ) {
	case "recent", "frequent":
		// 无播放历史，回退为新近（按名称排序已稳定）
	case "newest":
	default:
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(albums), start, end)
	data := make([]any, 0, end-start)
	for _, a := range albums[start:end] {
		data = append(data, n.albumJSON(a))
	}
	n.listJSON(w, r, data, len(albums))
}

// ---- artist ---------------------------------------------------------------

func (n *navidromeAPI) listArtists(w http.ResponseWriter, r *http.Request) {
	artists := n.artistList()
	field, desc := ndSortField(r)
	switch field {
	case "name":
		sort.SliceStable(artists, func(i, j int) bool { return artists[i].name < artists[j].name })
	case "random":
		rand.Shuffle(len(artists), func(i, j int) { artists[i], artists[j] = artists[j], artists[i] })
	}
	if desc {
		for i, j := 0, len(artists)-1; i < j; i, j = i+1, j-1 {
			artists[i], artists[j] = artists[j], artists[i]
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(artists), start, end)
	data := make([]any, 0, end-start)
	for _, a := range artists[start:end] {
		data = append(data, n.artistJSON(a))
	}
	n.listJSON(w, r, data, len(artists))
}
func (n *navidromeAPI) artistOne(w http.ResponseWriter, r *http.Request, id string) {
	for _, a := range n.artistList() {
		if a.id == id {
			obj := n.artistJSON(a)
			// 部分客户端直接从艺术家详情读取内嵌专辑，而不再单独请求 /albums。
			albums := make([]any, 0, len(a.albums))
			for _, al := range a.albums {
				albums = append(albums, n.albumJSON(al))
			}
			obj["albums"] = albums
			n.json(w, http.StatusOK, obj)
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "artist not found", "ok": false})
}
func (n *navidromeAPI) artistAlbums(w http.ResponseWriter, r *http.Request, id string) {
	for _, a := range n.artistList() {
		if a.id == id {
			data := make([]any, 0, len(a.albums))
			for _, al := range a.albums {
				data = append(data, n.albumJSON(al))
			}
			n.listJSON(w, r, data, len(a.albums))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "artist not found", "ok": false})
}

// artistTracks 返回该艺术家名下全部歌曲，兼容直接请求 /api/artist/{id}/tracks 的客户端。
func (n *navidromeAPI) artistTracks(w http.ResponseWriter, r *http.Request, id string) {
	data := make([]any, 0)
	for _, t := range n.all() {
		if artistID(t) == id {
			data = append(data, n.songJSON(t))
		}
	}
	n.listJSON(w, r, data, len(data))
}

// ---- playlist -------------------------------------------------------------

func (n *navidromeAPI) genres(w http.ResponseWriter, r *http.Request) {
	seen := map[string]int{}
	for _, t := range n.all() {
		if g := tag(t, "GENRE"); g != "" {
			seen[g]++
		}
	}
	data := make([]any, 0, len(seen))
	for g, c := range seen {
		data = append(data, map[string]any{"id": g, "name": g, "songCount": c, "albumCount": 0})
	}
	sort.Slice(data, func(i, j int) bool {
		return data[i].(map[string]any)["name"].(string) < data[j].(map[string]any)["name"].(string)
	})
	n.listJSON(w, r, data, len(data))
}
func (n *navidromeAPI) songList(w http.ResponseWriter, r *http.Request, typ string) {
	// recent/frequent/random 无播放历史，统一按全量曲目返回（与 Subsonic 一致）
	all := n.all()
	start, end := paginationND(r)
	start, end = sliceRange(len(all), start, end)
	data := make([]any, 0, end-start)
	for _, t := range all[start:end] {
		data = append(data, n.songJSON(t))
	}
	n.listJSON(w, r, data, len(all))
}

// ---- misc ------------------------------------------------------------------

func (n *navidromeAPI) scanStatus(w http.ResponseWriter, r *http.Request) {
	total := len(n.all())
	n.json(w, http.StatusOK, map[string]any{
		"scanning": false, "progress": 100, "done": true,
		"total": total, "missing": 0, "added": total, "errors": 0,
	})
}
func (n *navidromeAPI) statistics(w http.ResponseWriter, r *http.Request) {
	songs := n.all()
	albums := n.albumGroups()
	artistSet := map[string]bool{}
	for _, t := range songs {
		artistSet[artistID(t)] = true
	}
	n.json(w, http.StatusOK, map[string]any{
		"songCount": len(songs), "albumCount": len(albums), "artistCount": len(artistSet),
		"totalSong": len(songs), "totalAlbum": len(albums), "totalArtist": len(artistSet),
		"playCount": 0, "totalDuration": 0,
	})
}
func (n *navidromeAPI) nowPlaying(w http.ResponseWriter, r *http.Request) {
	n.listJSON(w, r, []any{}, 0)
}
func (n *navidromeAPI) lyrics(w http.ResponseWriter, r *http.Request, songID string) {
	t := n.track(songID)
	if t == nil {
		n.json(w, http.StatusNotFound, map[string]any{"error": "song not found", "ok": false})
		return
	}
	text := cachedLyrics(t) // 复用全局短 TTL 歌词缓存（内嵌 LYRICS 优先，其次同名 .lrc）
	synced := false
	for _, ln := range strings.Split(text, "\n") {
		if lrcLineRe.MatchString(ln) {
			synced = true
			break
		}
	}
	n.json(w, http.StatusOK, map[string]any{
		"id": songID, "songId": songID, "song": songTitle(t),
		"artist": songArtist(t), "lyrics": text,
		"provider": "lnh-music-tag", "external": false, "synced": synced,
	})
}

// ---- stream / cover -------------------------------------------------------

func (n *navidromeAPI) stream(w http.ResponseWriter, r *http.Request, id string) {
	t := n.track(id)
	if t == nil {
		http.Error(w, "song not found", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	dl := strings.EqualFold(q.Get("download"), "1") ||
		strings.EqualFold(q.Get("download"), "true") ||
		q.Get("static") == "true"
	if dl {
		streamTrack(w, r, n.cfg, t, "", "", true)
		return
	}
	streamTrack(w, r, n.cfg, t, q.Get("format"), bitRateParam(q), false)
}

// songDownload 以附件形式直传原始文件（/api/song/{id}/download），不转码。
func (n *navidromeAPI) songDownload(w http.ResponseWriter, r *http.Request, id string) {
	t := n.track(id)
	if t == nil {
		http.Error(w, "song not found", http.StatusNotFound)
		return
	}
	streamTrack(w, r, n.cfg, t, "", "", true)
}

// coverQuery 处理 /api/coverArt?id= /?artistId= /?coverArtId= /?albumId= 的查询式取封面。
func (n *navidromeAPI) coverQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		id = q.Get("coverArtId")
	}
	if id == "" {
		id = q.Get("artistId")
	}
	if id == "" {
		id = q.Get("albumId")
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	n.cover(w, r, id)
}
func (n *navidromeAPI) cover(w http.ResponseWriter, r *http.Request, id string) {
	t := n.track(id)
	if t == nil {
		for _, x := range n.all() {
			if albumID(x) == id {
				t = x
				break
			}
		}
	}
	if t == nil && strings.HasPrefix(id, "ar-") {
		// 优先取带 HasCover 标记的曲目；标记缺失时退而取该艺人任一曲目直接读内嵌封面。
		for _, x := range n.all() {
			if artistID(x) == id && x.HasCover {
				t = x
				break
			}
		}
		if t == nil {
			for _, x := range n.all() {
				if artistID(x) == id {
					t = x
					break
				}
			}
		}
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	img, err := taglibx.ReadCover(t.Path)
	if err != nil || len(img) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", http.DetectContentType(img))
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	_, _ = w.Write(img)
}
