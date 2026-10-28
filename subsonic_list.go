package main

import (
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
)

func (a *subsonicAPI) songs(r *http.Request) sxNode {
	q := r.URL.Query()
	typ := strings.ToLower(strings.TrimSpace(q.Get("type")))
	if typ == "" {
		return apiError(10, "Missing parameter: type")
	}
	start, size := parseStartLimit(r, []string{"offset"}, []string{"size"})
	// all() 每次返回独立切片，可安全排序/分片
	all := a.all()
	switch typ {
	case "byfrequent":
		a.mu.Lock()
		sort.SliceStable(all, func(i, j int) bool { return a.playCounts[all[i].ID] > a.playCounts[all[j].ID] })
		n := 0
		for _, t := range all {
			if a.playCounts[t.ID] > 0 {
				all[n] = t
				n++
			}
		}
		all = all[:n]
		a.mu.Unlock()
	case "bynewest":
		// all() 按扫描（添加）顺序返回：逆序后"最近添加"在前
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	case "byrandom":
		rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	case "bygenre":
		g := strings.ToLower(strings.TrimSpace(q.Get("genre")))
		n := 0
		for _, t := range all {
			if strings.ToLower(songGenre(t)) == g {
				all[n] = t
				n++
			}
		}
		all = all[:n]
	case "byyear":
		from, _ := strconv.Atoi(q.Get("fromYear"))
		to, _ := strconv.Atoi(q.Get("toYear"))
		if to == 0 {
			to = from
		}
		n := 0
		for _, t := range all {
			if y := songYear(t); y >= from && y <= to {
				all[n] = t
				n++
			}
		}
		all = all[:n]
		sort.Slice(all, func(i, j int) bool { return songYear(all[i]) > songYear(all[j]) })
	case "byartist":
		sort.Slice(all, func(i, j int) bool { return strings.ToLower(songArtist(all[i])) < strings.ToLower(songArtist(all[j])) })
	case "byalbum":
		sort.Slice(all, func(i, j int) bool { return strings.ToLower(songAlbum(all[i])) < strings.ToLower(songAlbum(all[j])) })
	default: // alphabetical / alphabeticalByName 等扩展值兜底按标题
		sort.Slice(all, func(i, j int) bool { return strings.ToLower(songTitle(all[i])) < strings.ToLower(songTitle(all[j])) })
	}
	if start > len(all) {
		start = len(all)
	}
	end := len(all)
	if size > 0 && start+size < end {
		end = start + size
	}
	children := make([]sxNode, 0, end-start)
	for _, t := range all[start:end] {
		children = append(children, a.songNode(t))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("songs", node("songs", children...)))
}
func (a *subsonicAPI) musicFolders() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("musicFolders", node("musicFolders", child("musicFolder", node("musicFolder", attr("id", "1"), attr("name", "Music"))))))
}
func artistIndexName(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "#"
	}
	first := strings.ToUpper(string(r[0]))
	if len(first) == 1 && first[0] >= 'A' && first[0] <= 'Z' {
		return first
	}
	return "#"
}
func (a *subsonicAPI) indexes() sxNode {
	tracks := a.all()
	songCounts := map[string]int{}
	albumSets := map[string]map[string]bool{}
	for _, t := range tracks {
		name := songArtist(t)
		songCounts[name]++
		if albumSets[name] == nil {
			albumSets[name] = map[string]bool{}
		}
		albumSets[name][songAlbum(t)] = true
	}
	groups := map[string][]sxNode{}
	seen := map[string]bool{}
	for _, t := range tracks {
		name := songArtist(t)
		if seen[name] {
			continue
		}
		seen[name] = true
		key := artistIndexName(name)
		attrs := []sxNode{
			attr("id", artistID(t)), attr("name", name), attr("coverArt", albumID(t)),
			attr("childCount", strconv.Itoa(songCounts[name])),
			attr("songCount", strconv.Itoa(songCounts[name])),
			attr("trackCount", strconv.Itoa(songCounts[name])),
			attr("albumCount", strconv.Itoa(len(albumSets[name]))),
		}
		groups[key] = append(groups[key], node("artist", attrs...))
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var indices []sxNode
	for _, k := range keys {
		sort.Slice(groups[k], func(i, j int) bool { return groups[k][i].Attr["name"] < groups[k][j].Attr["name"] })
		parts := append([]sxNode{attr("name", k)}, groups[k]...)
		indices = append(indices, node("index", parts...))
	}
	indexParts := append([]sxNode{attr("lastModified", strconv.FormatInt(time.Now().UnixMilli(), 10)), attr("ignoredArticles", "The El La Los Las Le Les")}, indices...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("indexes", node("indexes", indexParts...)))
}
func (a *subsonicAPI) artists() sxNode {
	groups := map[string][]sxNode{}
	albumSets := map[string]map[string]bool{}
	songCounts := map[string]int{}
	repCover := map[string]string{}
	for _, t := range a.all() {
		ar := songArtist(t)
		songCounts[ar]++
		if albumSets[ar] == nil {
			albumSets[ar] = map[string]bool{}
		}
		albumSets[ar][songAlbum(t)] = true
		if repCover[ar] == "" {
			repCover[ar] = albumID(t)
		}
	}
	for ar, albums := range albumSets {
		attrs := []sxNode{attr("id", stableID("ar-", ar)), attr("name", ar), attr("albumCount", strconv.Itoa(len(albums))), attr("songCount", strconv.Itoa(songCounts[ar])), attr("trackCount", strconv.Itoa(songCounts[ar]))}
		if repCover[ar] != "" {
			attrs = append(attrs, attr("coverArt", repCover[ar]))
		}
		groups[artistIndexName(ar)] = append(groups[artistIndexName(ar)], node("artist", attrs...))
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var indices []sxNode
	for _, k := range keys {
		sort.Slice(groups[k], func(i, j int) bool { return groups[k][i].Attr["name"] < groups[k][j].Attr["name"] })
		parts := append([]sxNode{attr("name", k)}, groups[k]...)
		indices = append(indices, node("index", parts...))
	}
	artistChildren := append([]sxNode{attr("total", strconv.Itoa(len(albumSets)))}, indices...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("artists", node("artists", artistChildren...)))
}
func (a *subsonicAPI) genres() sxNode {
	counts := map[string]int{}
	for _, t := range a.all() {
		if g := tag(t, "GENRE"); g != "" {
			counts[g]++
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	var values []sxNode
	for _, name := range names {
		values = append(values, node("genre", attr("songCount", strconv.Itoa(counts[name])), attr("albumCount", "0"), leaf("value", name)))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("genres", node("genres", values...)))
}

func albumNode(ts []*model.Track, id, name, artist, artistId string) sxNode {
	// 专辑名同时写 name 与 title：部分客户端（音流等）只读 title，缺 title
	// 会把专辑名渲染成占位"?"。
	attrs := []sxNode{attr("id", id), attr("name", name), attr("title", name), attr("isDir", "true"), attr("artist", artist), attr("artistId", artistId), attr("songCount", strconv.Itoa(len(ts))), attr("coverArt", ts[0].ID)}
	var dur float64
	for _, t := range ts {
		dur += t.Duration
	}
	attrs = append(attrs, attr("duration", strconv.FormatInt(int64(dur), 10)))
	if y := tag(ts[0], "YEAR", "DATE"); y != "" {
		attrs = append(attrs, attr("year", firstYear(y)))
	} else {
		attrs = append(attrs, attr("year", "0"))
	}
	if g := tag(ts[0], "GENRE"); g != "" {
		attrs = append(attrs, attr("genre", g))
	}
	attrs = append(attrs, attr("created", time.Now().UTC().Format(time.RFC3339)))
	return node("album", attrs...)
}
func (a *subsonicAPI) artist(id string) sxNode {
	var ar string
	var repCover string
	for _, t := range a.all() {
		if artistID(t) == id {
			ar = songArtist(t)
			repCover = t.ID // coverArt 引用曲目 ID（与 albumNode 口径一致），不能用专辑 ID
			break
		}
	}
	if ar == "" {
		return apiError(70, "Artist not found")
	}
	albums := map[string][]*model.Track{}
	for _, t := range a.all() {
		if songArtist(t) == ar {
			albums[songAlbum(t)] = append(albums[songAlbum(t)], t)
			if repCover == "" {
				repCover = albumID(t)
			}
		}
	}
	var arr []sxNode
	var songTotal int
	for name, ts := range albums {
		songTotal += len(ts)
		arr = append(arr, albumNode(ts, stableID("al-", ar+"\x00"+name), name, ar, id))
	}
	head := []sxNode{attr("id", id), attr("name", ar), attr("albumCount", strconv.Itoa(len(albums))), attr("songCount", strconv.Itoa(songTotal)), attr("trackCount", strconv.Itoa(songTotal))}
	if repCover != "" {
		head = append(head, attr("coverArt", repCover))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("artist", node("artist", append(head, arr...)...)))
}
func (a *subsonicAPI) albumList(r *http.Request, v2 bool) sxNode {
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 || size > 500 {
		size = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	typ := r.URL.Query().Get("type")
	groups := map[string][]*model.Track{}
	for _, t := range a.all() {
		k := songArtist(t) + "\x00" + songAlbum(t)
		groups[k] = append(groups[k], t)
	}
	type ent struct {
		k  string
		ts []*model.Track
	}
	var list []ent
	for k, ts := range groups {
		list = append(list, ent{k, ts})
	}
	albumYear := func(ts []*model.Track) int {
		if y := tag(ts[0], "YEAR", "DATE"); y != "" {
			if n, err := strconv.Atoi(firstYear(y)); err == nil {
				return n
			}
		}
		return 0
	}
	albumPlays := func(ts []*model.Track) int {
		n := 0
		for _, t := range ts {
			n += a.playCounts[t.ID]
		}
		return n
	}
	switch typ {
	case "random":
		rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
	case "newest":
		sort.SliceStable(list, func(i, j int) bool {
			yi, yj := albumYear(list[i].ts), albumYear(list[j].ts)
			if yi != yj {
				return yi > yj
			}
			return songAlbum(list[i].ts[0]) < songAlbum(list[j].ts[0])
		})
	case "frequent":
		sort.SliceStable(list, func(i, j int) bool {
			pi, pj := albumPlays(list[i].ts), albumPlays(list[j].ts)
			if pi != pj {
				return pi > pj
			}
			return songAlbum(list[i].ts[0]) < songAlbum(list[j].ts[0])
		})
	default:
		sort.SliceStable(list, func(i, j int) bool {
			return songAlbum(list[i].ts[0]) < songAlbum(list[j].ts[0])
		})
	}
	if offset > len(list) {
		offset = len(list)
	}
	end := offset + size
	if end > len(list) {
		end = len(list)
	}
	var albums []sxNode
	for _, e := range list[offset:end] {
		albums = append(albums, albumNode(e.ts, stableID("al-", e.k), songAlbum(e.ts[0]), songArtist(e.ts[0]), artistID(e.ts[0])))
	}
	// v1 getAlbumList 返回 <albumList>，v2 getAlbumList2 返回 <albumList2>，节点名必须与操作名一致。
	// total 给全量专辑数（不受 size/offset 分页截断影响），供客户端统计与分页。
	name := "albumList"
	if v2 {
		name = "albumList2"
	}
	albumChildren := append([]sxNode{attr("total", strconv.Itoa(len(list)))}, albums...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(name, node(name, albumChildren...)))
}
func (a *subsonicAPI) album(id string) sxNode {
	var arr []sxNode
	var title, artist string
	for _, t := range a.all() {
		if albumID(t) == id {
			title = songAlbum(t)
			artist = songArtist(t)
			arr = append(arr, a.songNode(t))
		}
	}
	if len(arr) == 0 {
		return apiError(70, "Album not found")
	}
	albumParts := append([]sxNode{attr("id", id), attr("name", title), attr("title", title), attr("artist", artist), attr("artistId", artistIDByName(artist)), attr("songCount", strconv.Itoa(len(arr))), attr("coverArt", arr[0].Attr["id"])}, arr...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("album", node("album", albumParts...)))
}
func artistIDByName(s string) string { return stableID("ar-", s) }
func (a *subsonicAPI) artistName(id string) string {
	for _, t := range a.all() {
		if artistID(t) == id {
			return songArtist(t)
		}
	}
	return ""
}
func (a *subsonicAPI) albumCount(id string) int {
	n := 0
	for _, t := range a.all() {
		if albumID(t) == id {
			n++
		}
	}
	return n
}
func (a *subsonicAPI) artistInfo(id string) sxNode {
	var name string
	for _, t := range a.all() {
		if artistID(t) == id {
			name = songArtist(t)
			break
		}
	}
	if name == "" {
		return apiError(70, "Artist not found")
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("artistInfo2", node("artistInfo2", attr("biography", ""), attr("musicBrainzId", ""), attr("lastFmUrl", ""), attr("smallImageUrl", ""), attr("mediumImageUrl", ""), attr("largeImageUrl", ""), attr("similarArtistCount", "0"))))
}
func (a *subsonicAPI) albumInfo(id string) sxNode {
	found := false
	for _, t := range a.all() {
		if albumID(t) == id {
			found = true
			break
		}
	}
	if !found {
		return apiError(70, "Album not found")
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("albumInfo", node("albumInfo", attr("notes", ""), attr("musicBrainzId", ""), attr("lastFmUrl", ""), attr("smallImageUrl", ""), attr("mediumImageUrl", ""), attr("largeImageUrl", ""))))
}

func (a *subsonicAPI) song(id string) sxNode {
	t := a.track(id)
	if t == nil {
		return apiError(70, "Song not found")
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("song", a.songNode(t)))
}
func (a *subsonicAPI) search(r *http.Request, resultName string) sxNode {
	q := strings.ToLower(strings.TrimSpace(strings.Trim(r.URL.Query().Get("query"), `"`)))
	if q == "" {
		q = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	}
	count := func(key string, def int) int {
		v, _ := strconv.Atoi(r.URL.Query().Get(key))
		if v <= 0 || v > 500 {
			return def
		}
		return v
	}
	offset := func(key string) int {
		v, _ := strconv.Atoi(r.URL.Query().Get(key))
		if v < 0 {
			return 0
		}
		return v
	}
	songCount, albumCount, artistCount := count("songCount", 50), count("albumCount", 20), count("artistCount", 20)
	songOff, albumOff, artistOff := offset("songOffset"), offset("albumOffset"), offset("artistOffset")
	// 歌曲：标题/歌手/专辑/文件名 全字段匹配
	var songs []sxNode
	for _, t := range a.all() {
		hay := strings.ToLower(songTitle(t) + " " + songArtist(t) + " " + songAlbum(t) + " " + t.FileName)
		if q == "" || strings.Contains(hay, q) {
			songs = append(songs, a.songNode(t))
			if len(songs) >= songOff+songCount {
				break
			}
		}
	}
	if songOff > len(songs) {
		songOff = len(songs)
	}
	if end := songOff + songCount; end < len(songs) {
		songs = songs[songOff:end]
	} else {
		songs = songs[songOff:]
	}
	// 专辑：专辑名/歌手 匹配
	groups := map[string][]*model.Track{}
	for _, t := range a.all() {
		k := songArtist(t) + "\x00" + songAlbum(t)
		groups[k] = append(groups[k], t)
	}
	var albums []sxNode
	for k, ts := range groups {
		hay := strings.ToLower(songAlbum(ts[0]) + " " + songArtist(ts[0]))
		if q == "" || strings.Contains(hay, q) {
			albums = append(albums, albumNode(ts, stableID("al-", k), songAlbum(ts[0]), songArtist(ts[0]), artistID(ts[0])))
		}
	}
	sort.Slice(albums, func(i, j int) bool { return albums[i].Attr["name"] < albums[j].Attr["name"] })
	if albumOff > len(albums) {
		albumOff = len(albums)
	}
	if end := albumOff + albumCount; end < len(albums) {
		albums = albums[albumOff:end]
	} else {
		albums = albums[albumOff:]
	}
	// 艺术家：歌手名 匹配
	songCounts := map[string]int{}
	albumSets := map[string]map[string]bool{}
	for _, t := range a.all() {
		ar := songArtist(t)
		songCounts[ar]++
		if albumSets[ar] == nil {
			albumSets[ar] = map[string]bool{}
		}
		albumSets[ar][songAlbum(t)] = true
	}
	var artists []sxNode
	for ar := range albumSets {
		if q != "" && !strings.Contains(strings.ToLower(ar), q) {
			continue
		}
		artists = append(artists, node("artist", attr("id", stableID("ar-", ar)), attr("name", ar), attr("albumCount", strconv.Itoa(len(albumSets[ar]))), attr("songCount", strconv.Itoa(songCounts[ar])), attr("trackCount", strconv.Itoa(songCounts[ar]))))
	}
	sort.Slice(artists, func(i, j int) bool { return artists[i].Attr["name"] < artists[j].Attr["name"] })
	if artistOff > len(artists) {
		artistOff = len(artists)
	}
	if end := artistOff + artistCount; end < len(artists) {
		artists = artists[artistOff:end]
	} else {
		artists = artists[artistOff:]
	}
	parts := append([]sxNode{}, songs...)
	parts = append(parts, albums...)
	parts = append(parts, artists...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(resultName, node(resultName, parts...)))
}
func (a *subsonicAPI) musicDirectory(id string) sxNode {
	if id == "" {
		return apiError(10, "Missing parameter: id")
	}
	// 1) 曲目 id：返回该曲目自身（与标准行为一致）
	if t := a.track(id); t != nil {
		c := a.songNode(t)
		c.Name = "child"
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("directory", node("directory", attr("id", id), attr("name", songTitle(t)), attr("parent", "1"), c)))
	}
	// 2) 媒体库根目录（getMusicFolders 返回的 folder id）：返回全部歌曲
	if id == "1" || id == "root" || strings.EqualFold(id, "Music") {
		var children []sxNode
		for _, t := range a.all() {
			c := a.songNode(t)
			c.Name = "child"
			children = append(children, c)
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Attr["title"] < children[j].Attr["title"] })
		dirParts := append([]sxNode{attr("id", id), attr("name", "Music"), attr("parent", "")}, children...)
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("directory", node("directory", dirParts...)))
	}
	// 3) 艺术家 id：返回该艺术家的全部专辑（<child isDir=true>）
	if name := a.artistName(id); name != "" {
		seen := map[string]bool{}
		var children []sxNode
		for _, t := range a.all() {
			if songArtist(t) != name {
				continue
			}
			aid := albumID(t)
			if seen[aid] {
				continue
			}
			seen[aid] = true
			children = append(children, node("child", attr("id", aid), attr("parent", id), attr("isDir", "true"), attr("title", songAlbum(t)), attr("name", songAlbum(t)), attr("album", songAlbum(t)), attr("artist", name), attr("artistId", id), attr("coverArt", t.ID), attr("childCount", strconv.Itoa(a.albumCount(aid))), attr("type", "music")))
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Attr["name"] < children[j].Attr["name"] })
		dirParts := append([]sxNode{attr("id", id), attr("name", name), attr("parent", "1")}, children...)
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("directory", node("directory", dirParts...)))
	}
	// 3) 专辑 id：返回专辑内歌曲（<child isDir=false>）
	var name, artist string
	var children []sxNode
	for _, t := range a.all() {
		if albumID(t) == id {
			name, artist = songAlbum(t), songArtist(t)
			c := a.songNode(t)
			c.Name = "child"
			children = append(children, c)
		}
	}
	if len(children) == 0 {
		return apiError(70, "Directory not found")
	}
	dirParts := append([]sxNode{attr("id", id), attr("name", name), attr("parent", artistIDByName(artist))}, children...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("directory", node("directory", dirParts...)))
}
func (a *subsonicAPI) songCollection(r *http.Request, mode string) sxNode {
	tracks := a.all()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("genre")))
	if mode == "genre" {
		filtered := tracks[:0]
		for _, t := range tracks {
			if strings.Contains(strings.ToLower(tag(t, "GENRE")), q) {
				filtered = append(filtered, t)
			}
		}
		tracks = filtered
	}
	switch mode {
	case "random":
		rand.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	case "newest":
		mtimes := map[string]time.Time{}
		for _, t := range tracks {
			if st, err := os.Stat(t.Path); err == nil {
				mtimes[t.ID] = st.ModTime()
			}
		}
		sort.SliceStable(tracks, func(i, j int) bool { return mtimes[tracks[i].ID].After(mtimes[tracks[j].ID]) })
	case "top":
		a.mu.Lock()
		counts := map[string]int{}
		for _, t := range tracks {
			counts[t.ID] = a.playCounts[t.ID]
		}
		a.mu.Unlock()
		sort.SliceStable(tracks, func(i, j int) bool { return counts[tracks[i].ID] > counts[tracks[j].ID] })
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size, _ = strconv.Atoi(r.URL.Query().Get("count"))
	}
	if size <= 0 || size > 500 {
		size = 50
	}
	if mode == "random" && size > 100 {
		size = 100
	}
	totalCount := len(tracks)
	if len(tracks) > size {
		tracks = tracks[:size]
	}
	var songs []sxNode
	for _, t := range tracks {
		songs = append(songs, a.songNode(t))
	}
	key := "songs"
	if mode == "random" {
		key = "randomSongs"
	}
	if mode == "newest" {
		key = "newestSongs"
	}
	if mode == "genre" {
		key = "songsByGenre"
	}
	if mode == "top" {
		key = "topSongs"
	}
	// total 为截断前的全量歌曲数，供客户端统计（与 Navidrome 的 x-total-count 响应头口径一致）。
	songs = append([]sxNode{attr("total", strconv.Itoa(totalCount))}, songs...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(key, node(key, songs...)))
}
