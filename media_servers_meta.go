package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/taglibx"
)

func (g *mediaGateway) albumGroups() []mediaAlbumGroup {
	tracks := g.library.all()
	groups := make([]mediaAlbumGroup, 0)
	index := make(map[string]int)
	for _, t := range tracks {
		id := albumID(t)
		i, ok := index[id]
		if !ok {
			i = len(groups)
			index[id] = i
			groups = append(groups, mediaAlbumGroup{id: id, first: t})
		}
		group := &groups[i]
		group.count++
		if t.HasCover {
			group.hasCover = true
		}
		if group.year == 0 {
			if y := tag(t, "YEAR", "DATE"); len(y) >= 4 {
				group.year, _ = strconv.Atoi(y[:4])
			}
		}
	}
	return groups
}

func (g *mediaGateway) albumDTO(group mediaAlbumGroup) map[string]any {
	t := group.first
	cover := map[string]string{}
	if group.hasCover {
		cover["Primary"] = "primary"
	}
	artist := songArtist(t)
	artistIDValue := artistID(t)
	return map[string]any{
		"Name": songAlbum(t), "Id": group.id, "Type": "MusicAlbum", "IsFolder": true,
		"AlbumArtist":  artist,
		"AlbumArtists": []any{map[string]any{"Name": artist, "Id": artistIDValue}},
		"ArtistItems":  []any{map[string]any{"Name": artist, "Id": artistIDValue}},
		"ChildCount":   group.count, "ServerId": "lnh-music-tag",
		"HasPrimaryImage": group.hasCover, "ImageTags": cover, "ProductionYear": group.year,
		"UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": g.isStarred(group.id), "Played": false},
	}
}

func (g *mediaGateway) latestItems(r *http.Request) []any {
	// Jellyfin/Emby clients expect MusicAlbum ItemDTOs in a bare JSON array.
	groups := g.albumGroups()
	items := make([]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, g.albumDTO(group))
	}
	limit := parseLimit(r, []string{"Limit", "limit"})
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}
func (g *mediaGateway) artistItems(r *http.Request) map[string]any {
	seen := map[string]bool{}
	albumsOf := map[string]map[string]bool{}
	songsOf := map[string]int{}
	names := map[string]string{}
	hasCover := map[string]bool{}
	for _, t := range g.library.all() {
		id := artistID(t)
		if !seen[id] {
			seen[id] = true
			names[id] = songArtist(t)
		}
		if albumsOf[id] == nil {
			albumsOf[id] = map[string]bool{}
		}
		albumsOf[id][albumID(t)] = true
		songsOf[id]++
		if t.HasCover {
			hasCover[id] = true
		}
	}
	var items []any
	q := strings.ToLower(strings.TrimSpace(qv(r, "SearchTerm", "searchTerm")))
	favOnly := strings.EqualFold(qv(r, "IsFavorite", "isFavorite"), "true") || strings.Contains(qv(r, "Filters", "filters"), "IsFavorite")
	for id := range seen {
		if favOnly && !g.isStarred(id) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(names[id]), q) {
			continue
		}
		dto := map[string]any{"Name": names[id], "Id": id, "Type": "MusicArtist", "IsFolder": true, "ServerId": "lnh-music-tag", "AlbumCount": len(albumsOf[id]), "SongCount": songsOf[id], "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": g.isStarred(id), "Played": false}}
		if hasCover[id] {
			dto["HasPrimaryImage"] = true
			dto["ImageTags"] = map[string]string{"Primary": "primary"}
		}
		items = append(items, dto)
	}
	sort.Slice(items, func(i, j int) bool {
		a := items[i].(map[string]any)["Name"].(string)
		b := items[j].(map[string]any)["Name"].(string)
		return strings.ToLower(a) < strings.ToLower(b)
	})
	return pageItems(items, r)
}
func (g *mediaGateway) artistItem(id string) map[string]any {
	name := ""
	albums := map[string]bool{}
	songs := 0
	hasCover := false
	for _, t := range g.library.all() {
		if artistID(t) == id {
			name = songArtist(t)
			albums[albumID(t)] = true
			songs++
			if t.HasCover {
				hasCover = true
			}
		}
	}
	if name == "" {
		return nil
	}
	dto := map[string]any{"Name": name, "Id": id, "Type": "MusicArtist", "IsFolder": true, "ServerId": "lnh-music-tag", "AlbumCount": len(albums), "SongCount": songs, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": g.isStarred(id), "Played": false}}
	if hasCover {
		dto["HasPrimaryImage"] = true
		dto["ImageTags"] = map[string]string{"Primary": "primary"}
	}
	return dto
}
func (g *mediaGateway) albumItem(id string) map[string]any {
	for _, group := range g.albumGroups() {
		if group.id == id {
			return g.albumDTO(group)
		}
	}
	return nil
}
func (g *mediaGateway) isStarred(id string) bool {
	var ids []string
	_ = json.Unmarshal([]byte(g.cfg.GetDefault("streaming_starred", "[]")), &ids)
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// loadPlaylists 读取持久化歌单（与 Subsonic 网关共享 streaming_playlists）。
func (g *mediaGateway) loadPlaylists() []subsonicPlaylist {
	var p []subsonicPlaylist
	raw := g.cfg.GetDefault("streaming_playlists", "[]")
	if json.Unmarshal([]byte(raw), &p) != nil || p == nil {
		return []subsonicPlaylist{}
	}
	return p
}

// playlistByID 兼容三种标识：存储原生 ID（自带 pl- 前缀）、展示前缀 "pl-<id>"、裸 <id>。
func (g *mediaGateway) playlistByID(id string) (subsonicPlaylist, int) {
	cands := []string{id}
	if stripped := strings.TrimPrefix(id, "pl-"); stripped != id {
		cands = append(cands, stripped)
	}
	for i, pl := range g.loadPlaylists() {
		for _, c := range cands {
			if pl.ID == c {
				return pl, i
			}
		}
	}
	return subsonicPlaylist{}, -1
}

func (g *mediaGateway) savePlaylists(p []subsonicPlaylist) {
	if b, err := json.Marshal(p); err == nil {
		_ = g.cfg.Set("streaming_playlists", string(b))
	}
}

// splitIDList 汇总 query 里逗号分隔的条目 id（Ids / EntryIds），去空白、去空。
func splitIDList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// createPlaylistJf 建歌单：POST /Users/{uid}/Playlists 或 POST /Playlists，
// body {"Name":..,"Ids":[..]}，兼容 query Name/Ids；与 Subsonic 共享 streaming_playlists。
func (g *mediaGateway) createPlaylistJf(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("Name"))
	var body struct {
		Name string   `json:"Name"`
		Ids  []string `json:"Ids"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	if body.Name != "" {
		name = strings.TrimSpace(body.Name)
	}
	ids := body.Ids
	if len(ids) == 0 {
		ids = splitIDList(r.URL.Query().Get("Ids"))
	}
	for _, sid := range ids {
		if g.library.get(sid) == nil {
			g.json(w, http.StatusNotFound, map[string]any{"Message": "Item not found: " + sid})
			return
		}
	}
	if name == "" {
		g.json(w, http.StatusBadRequest, map[string]any{"Message": "Missing Name"})
		return
	}
	ps := g.loadPlaylists()
	now := time.Now().UTC().Format(time.RFC3339)
	pl := subsonicPlaylist{ID: stableID("pl-", name+strconv.FormatInt(time.Now().UnixNano(), 10)), Name: name, Created: now, Changed: now, TrackIDs: []string{}}
	pl.TrackIDs = appendUniqueSongs(pl.TrackIDs, ids)
	ps = append(ps, pl)
	g.savePlaylists(ps)
	g.json(w, http.StatusOK, g.playlistDTO(pl))
}

// mutatePlaylistItems 加/删歌：POST /Playlists/{id}/Items?Ids=.. 追加（去重），
// DELETE /Playlists/{id}/Items?EntryIds=.. 移除。
func (g *mediaGateway) mutatePlaylistItems(w http.ResponseWriter, r *http.Request, id string) {
	_, idx := g.playlistByID(id)
	if idx < 0 {
		g.json(w, http.StatusNotFound, map[string]any{"Message": "Playlist not found"})
		return
	}
	ps := g.loadPlaylists()
	now := time.Now().UTC().Format(time.RFC3339)
	if r.Method == http.MethodDelete {
		remove := map[string]bool{}
		for _, x := range splitIDList(r.URL.Query().Get("EntryIds")) {
			remove[x] = true
		}
		next := []string{}
		for _, sid := range ps[idx].TrackIDs {
			if !remove[sid] {
				next = append(next, sid)
			}
		}
		ps[idx].TrackIDs = next
	} else {
		add := splitIDList(r.URL.Query().Get("Ids"))
		for _, sid := range add {
			if g.library.get(sid) == nil {
				g.json(w, http.StatusNotFound, map[string]any{"Message": "Item not found: " + sid})
				return
			}
		}
		ps[idx].TrackIDs = appendUniqueSongs(ps[idx].TrackIDs, add)
	}
	ps[idx].Changed = now
	g.savePlaylists(ps)
	g.json(w, http.StatusOK, g.playlistDTO(ps[idx]))
}

// validSongCount 统计歌单里当前库中仍存在的有效曲目数；失效/已删除的残留 id 不计入，
// 使列表 ChildCount 与实际可见曲目数一致，避免“列表N首/详情M首”漂移。
func (g *mediaGateway) validSongCount(pl subsonicPlaylist) int {
	n := 0
	for _, id := range pl.TrackIDs {
		if g.library.get(id) != nil {
			n++
		}
	}
	return n
}

func (g *mediaGateway) playlistDTO(pl subsonicPlaylist) map[string]any {
	return map[string]any{
		"Name": pl.Name, "Id": pl.ID, "Type": "Playlist", "IsFolder": true,
		"ChildCount": g.validSongCount(pl), "ServerId": "lnh-music-tag",
		"HasPrimaryImage": true, "ImageTags": map[string]string{"Primary": "primary"},
		"UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false},
	}
}

func (g *mediaGateway) playlistItems(r *http.Request) map[string]any {
	p := g.loadPlaylists()
	var items []any
	for _, pl := range p {
		items = append(items, map[string]any{"Name": pl.Name, "Id": pl.ID, "Type": "Playlist", "IsFolder": true, "ChildCount": g.validSongCount(pl), "ServerId": "lnh-music-tag", "HasPrimaryImage": true, "ImageTags": map[string]string{"Primary": "primary"}, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}})
	}
	return pageItems(items, r)
}

// cloneWithQuery 克隆请求并覆盖指定 query 参数，供复用 itemQuery 按歌单/艺术家过滤。
func (g *mediaGateway) cloneWithQuery(r *http.Request, kv map[string]string) *http.Request {
	r2 := r.Clone(r.Context())
	u := *r.URL
	q := u.Query()
	for k, v := range kv {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	r2.URL = &u
	return r2
}

// matchObjectRoute 统一处理歌单/艺术家对象的详情、曲目列表与加删歌，兼容
// /Playlists/{id} 与 /Users/{uid}/Playlists/{id} 两种前缀；已响应返回 true。
// /Artists/{id}/Albums、列表 /Playlists、建歌单等不在此处理，返回 false 走原分支。
func (g *mediaGateway) matchObjectRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	norm := p
	// 去掉 users/{uid}/ 前缀（标准客户端常带用户路径）
	if strings.HasPrefix(norm, "users/") {
		segs0 := strings.SplitN(norm, "/", 3)
		if len(segs0) == 3 {
			norm = segs0[2]
		}
	}
	segs := strings.Split(norm, "/")

	// ---- 歌单：/Playlists/{id} 详情、/Playlists/{id}/Items 曲目与加删 ----
	if segs[0] == "playlists" {
		if len(segs) == 2 {
			if pl, idx := g.playlistByID(segs[1]); idx >= 0 {
				g.json(w, 200, g.playlistDTO(pl))
			} else {
				http.NotFound(w, r)
			}
			return true
		}
		if len(segs) == 3 && segs[2] == "items" {
			id := segs[1]
			if r.Method == http.MethodGet {
				if _, idx := g.playlistByID(id); idx < 0 {
					http.NotFound(w, r)
					return true
				}
				g.json(w, 200, g.itemQuery(g.cloneWithQuery(r, map[string]string{"ParentId": id})))
				return true
			}
			// POST 追加 / DELETE 移除
			g.mutatePlaylistItems(w, r, id)
			return true
		}
	}

	// ---- 艺术家：/Artists/{id} 详情、/Artists/{id}/Items 曲目 ----
	if segs[0] == "artists" {
		if len(segs) == 2 {
			if it := g.artistItem(segs[1]); it != nil {
				g.json(w, 200, it)
			} else {
				http.NotFound(w, r)
			}
			return true
		}
		if len(segs) == 3 && segs[2] == "items" {
			g.json(w, 200, g.itemQuery(g.cloneWithQuery(r, map[string]string{"ArtistIds": segs[1]})))
			return true
		}
	}
	return false
}
func (g *mediaGateway) albumItems(r *http.Request, artists string) map[string]any {
	artistSet := map[string]bool{}
	for _, id := range strings.Split(artists, ",") {
		if id = strings.TrimSpace(id); id != "" {
			artistSet[id] = true
		}
	}
	favOnly := strings.EqualFold(qv(r, "IsFavorite", "isFavorite"), "true") || strings.Contains(qv(r, "Filters", "filters"), "IsFavorite")
	groups := g.albumGroups()
	items := make([]any, 0, len(groups))
	for _, group := range groups {
		if len(artistSet) > 0 && !artistSet[artistID(group.first)] {
			continue
		}
		if favOnly && !g.isStarred(group.id) {
			continue
		}
		items = append(items, g.albumDTO(group))
	}
	return pageItems(items, r)
}
func pageItems(items []any, r *http.Request) map[string]any {
	if items == nil {
		items = []any{}
	}
	start, limit := parseStartLimit(r, []string{"StartIndex", "startIndex"}, []string{"Limit", "limit"})
	total := len(items)
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return map[string]any{"Items": items[start:end], "TotalRecordCount": total, "StartIndex": start}
}
func (g *mediaGateway) genreItems(r *http.Request) map[string]any {
	seen := map[string]bool{}
	var items []any
	for _, t := range g.library.all() {
		n := tag(t, "GENRE")
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			items = append(items, map[string]any{"Name": n, "Id": stableID("gn-", n), "Type": "MusicGenre"})
		}
	}
	return pageItems(items, r)
}
func (g *mediaGateway) stream(w http.ResponseWriter, r *http.Request, id string) {
	t := g.library.get(id)
	if t == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	// 下载语义（download=1/true、static=true）：始终直传原始文件并以附件形式下发，不转码。
	dl := strings.EqualFold(q.Get("download"), "1") ||
		strings.EqualFold(q.Get("download"), "true") ||
		q.Get("static") == "true"
	if dl {
		streamTrack(w, r, g.cfg, t, "", "", true)
		return
	}
	streamTrack(w, r, g.cfg, t, q.Get("format"), bitRateParam(q), false)
}

// directDownload 以附件形式直传原始文件（JF/Emby 的 /audio/{id}/download、/items/{id}/Download）。
func (g *mediaGateway) directDownload(w http.ResponseWriter, r *http.Request, id string) {
	t := g.library.get(id)
	if t == nil {
		http.NotFound(w, r)
		return
	}
	streamTrack(w, r, g.cfg, t, "", "", true)
}

// embyHLSPlaylist 输出 Emby HLS 单分片播放列表（VOD，整曲一个分片）。
func (g *mediaGateway) embyHLSPlaylist(w http.ResponseWriter, r *http.Request, t *model.Track) {
	dur := t.Duration
	if dur <= 0 {
		dur = 1
	}
	body := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDUR:270\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:" +
		strconv.FormatFloat(dur, 'f', 2, 64) + ",\nhls0.ts\n#EXT-X-ENDLIST\n"
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

// embyHLSSegment 以 mpegts 实时转码输出 Emby HLS 分片。
func (g *mediaGateway) embyHLSSegment(w http.ResponseWriter, r *http.Request, t *model.Track) {
	br := 320
	if v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("MaxStreamingBitrate"))); err == nil && v >= 1000 {
		br = v / 1000
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	serveHLS(w, r, g.cfg, t, br)
}

func (g *mediaGateway) cover(w http.ResponseWriter, r *http.Request, id string) {
	t := g.library.get(id)
	if t == nil {
		for _, x := range g.library.all() {
			if albumID(x) == id {
				t = x
				break
			}
		}
	}
	if t == nil && strings.HasPrefix(id, "ar-") {
		// 优先取已标记 HasCover 的曲目；没有标记时退而取该艺术家任一曲目，
		// 交由下方 ReadCover 直接尝试读内嵌封面，避免“有封面却因标记缺失 404”。
		var fallback *model.Track
		for _, x := range g.library.all() {
			if artistID(x) == id {
				if fallback == nil {
					fallback = x
				}
				if x.HasCover {
					t = x
					break
				}
			}
		}
		if t == nil {
			t = fallback
		}
	}
	if t == nil && strings.HasPrefix(id, "pl-") {
		// 歌单封面：取歌单首个仍存在的曲目，读其内嵌封面。
		if pl, idx := g.playlistByID(id); idx >= 0 {
			for _, sid := range pl.TrackIDs {
				if x := g.library.get(sid); x != nil {
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
	_, _ = w.Write(img)
}
func (g *mediaGateway) playback(r *http.Request) {
	var v struct {
		ItemID        string `json:"ItemId"`
		PositionTicks int64  `json:"PositionTicks"`
		CanSeek       bool   `json:"CanSeek"`
	}
	_ = json.NewDecoder(r.Body).Decode(&v)
	if v.ItemID == "" {
		v.ItemID = r.URL.Query().Get("ItemId")
	}
	if v.ItemID == "" {
		return
	}
	g.mu.Lock()
	g.plays[v.ItemID] = playState{UserID: gatewayUserID, ItemID: v.ItemID, Position: v.PositionTicks, Started: time.Now(), Updated: time.Now()}
	g.mu.Unlock()
}
func (g *mediaGateway) stopPlayback(r *http.Request) {
	var v struct {
		ItemID        string `json:"ItemId"`
		PositionTicks int64  `json:"PositionTicks"`
	}
	_ = json.NewDecoder(r.Body).Decode(&v)
	g.mu.Lock()
	delete(g.plays, v.ItemID)
	g.mu.Unlock()
}
