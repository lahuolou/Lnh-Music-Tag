package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
)

func (n *navidromeAPI) listPlaylists(w http.ResponseWriter, r *http.Request) {
	ps := n.loadPlaylists()
	data := make([]any, 0, len(ps))
	for _, p := range ps {
		data = append(data, n.playlistJSON(p))
	}
	n.listJSON(w, r, data, len(ps))
}

// playlistTracksJSON 解析歌单内真实存在的曲目为 ND song 对象数组（失效 id 跳过）。
func (n *navidromeAPI) playlistTracksJSON(p subsonicPlaylist) []any {
	data := make([]any, 0, len(p.TrackIDs))
	for _, sid := range p.TrackIDs {
		if t := n.track(sid); t != nil {
			data = append(data, n.songJSON(t))
		}
	}
	return data
}
func (n *navidromeAPI) playlistOne(w http.ResponseWriter, r *http.Request, id string) {
	for _, p := range n.loadPlaylists() {
		if p.ID == id {
			obj := n.playlistJSON(p)
			// 部分 ND 客户端直接从详情对象读取内嵌 tracks，而不再单独请求 /tracks。
			obj["tracks"] = n.playlistTracksJSON(p)
			n.json(w, http.StatusOK, obj)
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "playlist not found", "ok": false})
}
func (n *navidromeAPI) playlistSongs(w http.ResponseWriter, r *http.Request, id string) {
	for _, p := range n.loadPlaylists() {
		if p.ID == id {
			data := n.playlistTracksJSON(p)
			n.listJSON(w, r, data, len(data))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "playlist not found", "ok": false})
}
func (n *navidromeAPI) loadPlaylists() []subsonicPlaylist {
	raw := n.cfg.GetDefault("streaming_playlists", "[]")
	var p []subsonicPlaylist
	if json.Unmarshal([]byte(raw), &p) != nil {
		return []subsonicPlaylist{}
	}
	if p == nil {
		return []subsonicPlaylist{}
	}
	return p
}
func (n *navidromeAPI) savePlaylists(ps []subsonicPlaylist) {
	if b, err := json.Marshal(ps); err == nil {
		_ = n.cfg.Set("streaming_playlists", string(b))
	}
}

func (n *navidromeAPI) decodeSongIDs(r *http.Request) []string {
	var body struct {
		Ids     []string `json:"ids"`
		SongIds []string `json:"songIds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ids := append(body.Ids, body.SongIds...)
	for _, v := range r.URL.Query()["id"] {
		ids = append(ids, v)
	}
	for _, v := range r.URL.Query()["songId"] {
		ids = append(ids, v)
	}
	return ids
}

// createPlaylist 处理 POST /api/playlist：body {name, songIds|ids} 或 query name。
func (n *navidromeAPI) createPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		SongIds []string `json:"songIds"`
		Ids     []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if name == "" {
		n.json(w, http.StatusBadRequest, map[string]any{"error": "name required", "ok": false})
		return
	}
	ids := append(body.SongIds, body.Ids...)
	for _, sid := range ids {
		if n.track(sid) == nil {
			n.json(w, http.StatusNotFound, map[string]any{"error": "song not found: " + sid, "ok": false})
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pl := subsonicPlaylist{ID: stableID("pl-", name+strconv.FormatInt(time.Now().UnixNano(), 10)), Name: name, Created: now, Changed: now, TrackIDs: append([]string{}, ids...)}
	ps := n.loadPlaylists()
	ps = append(ps, pl)
	n.savePlaylists(ps)
	n.json(w, http.StatusOK, n.playlistJSON(pl))
}

// playlistRenameDelete 处理 PUT /api/playlist/{id}（改名）与 DELETE /api/playlist/{id}（删除）。
func (n *navidromeAPI) playlistRenameDelete(w http.ResponseWriter, r *http.Request, id string) {
	ps := n.loadPlaylists()
	idx := -1
	for i := range ps {
		if ps[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		n.json(w, http.StatusNotFound, map[string]any{"error": "playlist not found", "ok": false})
		return
	}
	if r.Method == http.MethodDelete {
		out := append([]subsonicPlaylist{}, ps[:idx]...)
		out = append(out, ps[idx+1:]...)
		n.savePlaylists(out)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if name := strings.TrimSpace(body.Name); name != "" {
		ps[idx].Name = name
	}
	ps[idx].Changed = time.Now().UTC().Format(time.RFC3339)
	n.savePlaylists(ps)
	n.json(w, http.StatusOK, n.playlistJSON(ps[idx]))
}

// playlistSongsWrite 处理 POST（去重追加）/ DELETE（按 id 移除）/api/playlist/{id}/songs。
func (n *navidromeAPI) playlistSongsWrite(w http.ResponseWriter, r *http.Request, id string) {
	ps := n.loadPlaylists()
	idx := -1
	for i := range ps {
		if ps[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		n.json(w, http.StatusNotFound, map[string]any{"error": "playlist not found", "ok": false})
		return
	}
	ids := n.decodeSongIDs(r)
	if r.Method == http.MethodDelete {
		kill := map[string]bool{}
		for _, x := range ids {
			kill[x] = true
		}
		next := make([]string, 0, len(ps[idx].TrackIDs))
		for _, sid := range ps[idx].TrackIDs {
			if !kill[sid] {
				next = append(next, sid)
			}
		}
		ps[idx].TrackIDs = next
	} else {
		have := map[string]bool{}
		for _, sid := range ps[idx].TrackIDs {
			have[sid] = true
		}
		for _, sid := range ids {
			if n.track(sid) == nil {
				n.json(w, http.StatusNotFound, map[string]any{"error": "song not found: " + sid, "ok": false})
				return
			}
			if !have[sid] {
				ps[idx].TrackIDs = append(ps[idx].TrackIDs, sid)
				have[sid] = true
			}
		}
	}
	ps[idx].Changed = time.Now().UTC().Format(time.RFC3339)
	n.savePlaylists(ps)
	w.WriteHeader(http.StatusNoContent)
}

// ---- search / starred / genres -------------------------------------------

func (n *navidromeAPI) searchTerm(r *http.Request) string {
	q := strings.ToLower(strings.TrimSpace(strings.Trim(r.URL.Query().Get("query"), `"`)))
	if q == "" {
		q = strings.ToLower(strings.TrimSpace(strings.Trim(r.URL.Query().Get("q"), `"`)))
	}
	return q
}
func (n *navidromeAPI) searchSongs(w http.ResponseWriter, r *http.Request) {
	q := n.searchTerm(r)
	var matched []*model.Track
	for _, t := range n.all() {
		hay := strings.ToLower(songTitle(t) + " " + songArtist(t) + " " + songAlbum(t) + " " + t.FileName)
		if q == "" || strings.Contains(hay, q) {
			matched = append(matched, t)
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(matched), start, end)
	data := make([]any, 0, end-start)
	for _, t := range matched[start:end] {
		data = append(data, n.songJSON(t))
	}
	n.listJSON(w, r, data, len(matched))
}
func (n *navidromeAPI) searchAlbums(w http.ResponseWriter, r *http.Request) {
	q := n.searchTerm(r)
	var matched []ndAlbum
	for _, a := range n.albumGroups() {
		hay := strings.ToLower(a.name + " " + a.artist)
		if q == "" || strings.Contains(hay, q) {
			matched = append(matched, a)
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(matched), start, end)
	data := make([]any, 0, end-start)
	for _, a := range matched[start:end] {
		data = append(data, n.albumJSON(a))
	}
	n.listJSON(w, r, data, len(matched))
}
func (n *navidromeAPI) searchArtists(w http.ResponseWriter, r *http.Request) {
	q := n.searchTerm(r)
	var matched []ndArtist
	for _, a := range n.artistList() {
		if q == "" || strings.Contains(strings.ToLower(a.name), q) {
			matched = append(matched, a)
		}
	}
	start, end := paginationND(r)
	start, end = sliceRange(len(matched), start, end)
	data := make([]any, 0, end-start)
	for _, a := range matched[start:end] {
		data = append(data, n.artistJSON(a))
	}
	n.listJSON(w, r, data, len(matched))
}
func (n *navidromeAPI) searchAll(w http.ResponseWriter, r *http.Request) {
	q := n.searchTerm(r)
	var songs, albums, artists []any
	for _, t := range n.all() {
		hay := strings.ToLower(songTitle(t) + " " + songArtist(t) + " " + songAlbum(t) + " " + t.FileName)
		if q == "" || strings.Contains(hay, q) {
			songs = append(songs, n.songJSON(t))
			if len(songs) >= 100 {
				break
			}
		}
	}
	for _, a := range n.albumGroups() {
		hay := strings.ToLower(a.name + " " + a.artist)
		if q == "" || strings.Contains(hay, q) {
			albums = append(albums, n.albumJSON(a))
			if len(albums) >= 100 {
				break
			}
		}
	}
	for _, a := range n.artistList() {
		if q == "" || strings.Contains(strings.ToLower(a.name), q) {
			artists = append(artists, n.artistJSON(a))
			if len(artists) >= 100 {
				break
			}
		}
	}
	n.json(w, http.StatusOK, map[string]any{
		"songs":   songs,
		"albums":  albums,
		"artists": artists,
	})
}
func (n *navidromeAPI) starred(w http.ResponseWriter, r *http.Request) {
	data := make([]any, 0)
	for _, id := range n.loadStarred() {
		if t := n.track(id); t != nil {
			data = append(data, n.songJSON(t))
		}
	}
	n.listJSON(w, r, data, len(data))
}
func (n *navidromeAPI) loadStarred() []string {
	var ids []string
	_ = json.Unmarshal([]byte(n.cfg.GetDefault("streaming_starred", "[]")), &ids)
	return ids
}
func (n *navidromeAPI) saveStarred(ids []string) {
	if b, err := json.Marshal(ids); err == nil {
		_ = n.cfg.Set("streaming_starred", string(b))
	}
	entityCacheClear() // 收藏变更：实体响应（含 starred 字段）缓存失效
}

// starWrite 处理 Navidrome 原生收藏：PUT /api/starred/{id} 加星，DELETE 取消，与 Subsonic 共用 streaming_starred。
func (n *navidromeAPI) starWrite(w http.ResponseWriter, r *http.Request, id string) {
	if n.track(id) == nil {
		n.json(w, http.StatusNotFound, map[string]any{"error": "song not found", "ok": false})
		return
	}
	set := make(map[string]bool)
	for _, x := range n.loadStarred() {
		set[x] = true
	}
	if r.Method == http.MethodDelete {
		delete(set, id)
	} else {
		set[id] = true
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	n.saveStarred(out)
	w.WriteHeader(http.StatusNoContent)
}
