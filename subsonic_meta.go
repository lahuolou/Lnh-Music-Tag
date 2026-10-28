package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	cstore "lnh-music-tag/internal/store"
)

func (a *subsonicAPI) loadPlaylists() []subsonicPlaylist {
	raw := a.cfg.GetDefault("streaming_playlists", "[]")
	var p []subsonicPlaylist
	if json.Unmarshal([]byte(raw), &p) != nil {
		return []subsonicPlaylist{}
	}
	if p == nil {
		return []subsonicPlaylist{}
	}
	return p
}
func (a *subsonicAPI) savePlaylists(p []subsonicPlaylist) {
	b, err := json.Marshal(p)
	if err == nil {
		_ = a.cfg.Set("streaming_playlists", string(b))
	}
}

// appendUniqueSongs 把待加入曲目去重后追加到歌单（忽略空 id 与已存在项）。
func appendUniqueSongs(dst []string, ids []string) []string {
	have := make(map[string]bool, len(dst)+len(ids))
	for _, s := range dst {
		have[s] = true
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || have[id] {
			continue
		}
		have[id] = true
		dst = append(dst, id)
	}
	return dst
}

// addSongIDs 汇总各客户端别名：标准 songIdToAdd，兼容 songId（部分客户端
// 在已有歌单上追加时使用）。playlistId 是歌单自身 id，不会混入。
func addSongIDs(q url.Values) []string {
	ids := q["songIdToAdd"]
	if len(ids) == 0 {
		ids = q["songId"]
	}
	return ids
}

func (a *subsonicAPI) playlistNode(p subsonicPlaylist, includeSongs bool) sxNode {
	// getPlaylist 标准把曲目序列化为 <entry> 子节点（不是 <song>）；
	// 音流等客户端按 entry 解析，输出 song 会导致详情页读不到任何曲目。
	// songCount 始终以实际能解析到的有效曲目数为准：失效/已删除的残留 id 不计入，
	// 保证列表(getPlaylists)与详情(getPlaylist)的计数一致，不出现“列表3首/详情2首”。
	resolved := 0
	for _, id := range p.TrackIDs {
		if a.track(id) != nil {
			resolved++
		}
	}
	parts := []sxNode{attr("id", p.ID), attr("name", p.Name), attr("owner", a.cfg.GetDefault("admin_user", "admin")), attr("public", "false"), attr("songCount", strconv.Itoa(resolved)), attr("created", p.Created), attr("changed", p.Changed)}
	if includeSongs {
		for _, id := range p.TrackIDs {
			if t := a.track(id); t != nil {
				e := a.songNode(t)
				e.Name = "entry"
				parts = append(parts, e)
			}
		}
	}
	return node("playlist", parts...)
}
func (a *subsonicAPI) playlists() sxNode {
	ps := a.loadPlaylists()
	items := make([]sxNode, 0, len(ps))
	for _, p := range ps {
		items = append(items, a.playlistNode(p, false))
	}
	plChildren := append([]sxNode{attr("total", strconv.Itoa(len(ps)))}, items...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("playlists", node("playlists", plChildren...)))
}

// statistics 返回全库歌曲/专辑/歌手/歌单真实计数，供客户端概览页展示。
// 专辑按“艺人+专辑名”去重，歌手按歌手 ID 去重，与 getArtists/getAlbumList 口径一致。
func (a *subsonicAPI) statistics() sxNode {
	songs := a.all()
	albums := make(map[string]bool)
	artists := make(map[string]bool)
	for _, t := range songs {
		if t == nil {
			continue
		}
		albums[albumID(t)] = true
		artists[artistID(t)] = true
	}
	pl := a.loadPlaylists()
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion),
		child("statistics", node("statistics",
			attr("songCount", strconv.Itoa(len(songs))),
			attr("albumCount", strconv.Itoa(len(albums))),
			attr("artistCount", strconv.Itoa(len(artists))),
			attr("playlistCount", strconv.Itoa(len(pl))),
			attr("playCount", "0"),
		)))
}
func (a *subsonicAPI) playlist(id string) sxNode {
	for _, p := range a.loadPlaylists() {
		if p.ID == id {
			return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("playlist", a.playlistNode(p, true)))
		}
	}
	return apiError(70, "Playlist not found")
}
func (a *subsonicAPI) createPlaylist(r *http.Request) sxNode {
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	id := q.Get("playlistId")
	ps := a.loadPlaylists()
	now := time.Now().UTC().Format(time.RFC3339)
	var p subsonicPlaylist
	found := -1
	for i, x := range ps {
		if x.ID == id && id != "" {
			found = i
			p = x
			break
		}
	}
	if found < 0 {
		if name == "" {
			return apiError(10, "Missing parameter: name")
		}
		p = subsonicPlaylist{ID: stableID("pl-", name+strconv.FormatInt(time.Now().UnixNano(), 10)), Name: name, Created: now, TrackIDs: []string{}}
		ps = append(ps, p)
		found = len(ps) - 1
	}
	if name != "" {
		ps[found].Name = name
	}
	addIDs := addSongIDs(q)
	for _, songID := range addIDs {
		if a.track(songID) == nil {
			return apiError(70, "Song not found: "+songID)
		}
	}
	ps[found].TrackIDs = appendUniqueSongs(ps[found].TrackIDs, addIDs)
	ps[found].Changed = now
	a.savePlaylists(ps)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("playlist", a.playlistNode(ps[found], true)))
}
func (a *subsonicAPI) updatePlaylist(r *http.Request) sxNode {
	q := r.URL.Query()
	id := q.Get("playlistId")
	ps := a.loadPlaylists()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range ps {
		if ps[i].ID != id {
			continue
		}
		addIDs := addSongIDs(q)
		for _, sid := range addIDs {
			if a.track(sid) == nil {
				return apiError(70, "Song not found: "+sid)
			}
		}
		if q.Get("name") != "" {
			ps[i].Name = q.Get("name")
		}
		ps[i].TrackIDs = appendUniqueSongs(ps[i].TrackIDs, addIDs)
		remove := map[int]bool{}
		for _, v := range q["songIndexToRemove"] {
			n, e := strconv.Atoi(v)
			if e == nil {
				remove[n] = true
			}
		}
		if len(remove) > 0 {
			next := []string{}
			for n, sid := range ps[i].TrackIDs {
				if !remove[n] {
					next = append(next, sid)
				}
			}
			ps[i].TrackIDs = next
		}
		ps[i].Changed = now
		a.savePlaylists(ps)
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
	}
	return apiError(70, "Playlist not found")
}
func (a *subsonicAPI) deletePlaylist(id string) sxNode {
	ps := a.loadPlaylists()
	out := ps[:0]
	found := false
	for _, p := range ps {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return apiError(70, "Playlist not found")
	}
	a.savePlaylists(out)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}
func (a *subsonicAPI) loadStarred() []string {
	var ids []string
	_ = json.Unmarshal([]byte(a.cfg.GetDefault("streaming_starred", "[]")), &ids)
	return ids
}
func (a *subsonicAPI) saveStarred(ids []string) {
	b, err := json.Marshal(ids)
	if err == nil {
		_ = a.cfg.Set("streaming_starred", string(b))
	}
	entityCacheClear() // 收藏变更：实体响应（含 starred 字段）缓存失效
}
func (a *subsonicAPI) loadRatings() map[string]int {
	out := map[string]int{}
	_ = json.Unmarshal([]byte(a.cfg.GetDefault("streaming_ratings", "{}")), &out)
	return out
}
func (a *subsonicAPI) saveRatings(m map[string]int) {
	if b, err := json.Marshal(m); err == nil {
		_ = a.cfg.Set("streaming_ratings", string(b))
	}
	entityCacheClear() // 评分变更：实体响应（含 userRating 字段）缓存失效
}

// ratingFor 读取与四协议共享的星级评分（streaming_ratings: {歌曲ID: 0-5}），
// 未评分或不存在时返回 0。供 Navidrome / Jellyfin / Emby 视图回传，避免重复解析。
func ratingFor(cfg *cstore.Config, id string) int {
	m := map[string]int{}
	_ = json.Unmarshal([]byte(cfg.GetDefault("streaming_ratings", "{}")), &m)
	return m[id]
}
func (a *subsonicAPI) validStarID(id string) bool {
	if a.track(id) != nil {
		return true
	}
	for _, t := range a.all() {
		if albumID(t) == id || artistID(t) == id {
			return true
		}
	}
	return false
}

func (a *subsonicAPI) star(r *http.Request, add bool) sxNode {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		return apiError(10, "Missing required parameter: id")
	}
	stars := a.loadStarred()
	set := make(map[string]bool, len(stars)+len(ids))
	for _, id := range stars {
		set[id] = true
	}
	for _, id := range ids {
		if !a.validStarID(id) {
			return apiError(70, "Song, album, or artist not found: "+id)
		}
		if add {
			set[id] = true
		} else {
			delete(set, id)
		}
	}
	stars = stars[:0]
	for id := range set {
		stars = append(stars, id)
	}
	sort.Strings(stars)
	a.saveStarred(stars)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}

// setRating：标准 Subsonic 星级评分（0~5）。rating=0 表示清除。仅对真实
// 曲目生效，评分持久化到配置并在 songNode 以 userRating 回传。
func (a *subsonicAPI) setRating(r *http.Request) sxNode {
	ids := r.URL.Query()["id"]
	rating, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("rating")))
	if err != nil || rating < 0 || rating > 5 {
		return apiError(10, "Missing or invalid parameter: rating (0-5)")
	}
	if len(ids) == 0 {
		return apiError(10, "Missing required parameter: id")
	}
	ratings := a.loadRatings()
	for _, id := range ids {
		if a.track(id) == nil {
			return apiError(70, "Song not found: "+id)
		}
		if rating == 0 {
			delete(ratings, id)
		} else {
			ratings[id] = rating
		}
	}
	a.saveRatings(ratings)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}

func (a *subsonicAPI) isStarred(id string) bool {
	for _, s := range a.loadStarred() {
		if s == id {
			return true
		}
	}
	return false
}

// getUser 返回单一内置用户（管理员），字段对齐 Subsonic API 的 user DTO。
