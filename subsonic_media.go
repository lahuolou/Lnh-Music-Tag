package main

import (
	"encoding/json"
	"lnh-music-tag/internal/taglibx"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
)

func (a *subsonicAPI) lyrics(r *http.Request, byID bool) sxNode {
	q := r.URL.Query()
	artist := strings.TrimSpace(q.Get("artist"))
	title := strings.TrimSpace(q.Get("title"))
	var t *model.Track
	if byID {
		t = a.track(q.Get("id"))
	} else {
		// 部分客户端会在 artist/title 之外附带 id，优先用 id 精确定位
		if id := q.Get("id"); id != "" {
			t = a.track(id)
		}
		if t == nil && artist == "" && title == "" {
			a.recMu.Lock()
			ls := a.lastGetSong
			if ls == "" {
				ls = a.lastStreamed
			}
			a.recMu.Unlock()
			if ls != "" {
				t = a.track(ls)
			}
		}
		if t == nil {
			for _, x := range a.all() {
				if strings.EqualFold(songArtist(x), artist) && strings.EqualFold(songTitle(x), title) {
					t = x
					break
				}
			}
		}
	}
	if t == nil {
		if byID {
			return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("lyricsList", node("lyricsList")))
		}
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("lyrics", sxNode{Name: "lyrics", Attr: map[string]string{"artist": artist, "title": title}, Text: ""}), child("lyricsList", node("lyricsList")))
	}
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
	if byID {
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("lyricsList", structuredLyricsNode(t, text)))
	}
	// getLyrics 同时返回 lyrics（原始文本，保留 LRC 时间戳，Airsonic/Subsonic 惯例）
	// 与 lyricsList（结构化同步/非同步歌词），兼容只解析其中一种的客户端；
	// 无时间戳的纯文本歌词原样返回。
	raw := strings.TrimSpace(text)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion),
		child("lyrics", sxNode{Name: "lyrics", Attr: map[string]string{"artist": songArtist(t), "title": songTitle(t)}, Text: raw}),
		child("lyricsList", structuredLyricsNode(t, text)),
	)
}

// structuredLyricsNode 将 LRC/纯文本歌词解析为 OpenSubsonic structuredLyrics 节点。
// 支持多行（每行一个 line，start 毫秒）；非 LRC 文本作为单行 unsynced 歌词返回。
func structuredLyricsNode(t *model.Track, text string) sxNode {
	lines := []sxNode{}
	started := false
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if lrcMetaLineRe.MatchString(ln) {
			continue // 跳过 [ti:]/[ar:]/[al:]/[by:]/[offset:] 等元信息行
		}
		if m := lrcLineRe.FindStringSubmatch(ln); m != nil {
			min, _ := strconv.Atoi(m[1])
			sec, _ := strconv.Atoi(m[2])
			var milli int
			if dot := strings.Index(m[0], "."); dot >= 0 {
				frac := m[0][dot+1:]
				frac = strings.TrimRight(frac, "]")
				if len(frac) > 0 {
					milli, _ = strconv.Atoi(frac)
				}
			}
			start := (min*60 + sec) * 1000
			if milli > 0 {
				switch {
				case milli < 100:
					start += milli * 10
				case milli < 1000:
					start += milli
				default:
					start += milli / 10
				}
			}
			started = true
			if txt := strings.TrimSpace(m[3]); txt != "" {
				lines = append(lines, node("line", attr("start", strconv.Itoa(start)), attr("value", txt)))
			}
		} else if !started && strings.TrimSpace(ln) != "" {
			// 非 LRC：整段作为单行 unsynced 歌词
			lines = append(lines, node("line", attr("start", "0"), attr("value", strings.TrimSpace(ln))))
		}
	}
	if len(lines) == 0 {
		if txt := strings.TrimSpace(text); txt != "" {
			lines = append(lines, node("line", attr("start", "0"), attr("value", txt)))
		}
	}
	sl := node("structuredLyrics", attr("displayArtist", songArtist(t)), attr("displayTitle", songTitle(t)), attr("lang", "und"))
	sl.Children = append(sl.Children, lines...)
	return node("lyricsList", node("lyricsList", child("structuredLyrics", sl)))
}

func (a *subsonicAPI) getUser(r *http.Request) sxNode {
	user := r.URL.Query().Get("username")
	if user == "" {
		user = a.cfg.GetDefault("admin_user", "admin")
	}
	u := node("user",
		attr("username", user),
		attr("email", user+"@localhost"),
		attr("scrobblingEnabled", "true"),
		attr("adminRole", "true"),
		attr("settingsRole", "true"),
		attr("streamRole", "true"),
		attr("downloadRole", "true"),
		attr("uploadRole", "false"),
		attr("commentRole", "false"),
		attr("podcastRole", "false"),
		attr("shareRole", "false"),
		attr("coverArtRole", "true"),
		attr("playlistRole", "true"),
		attr("jukeboxRole", "false"),
		attr("folder", "0"),
		attr("quota", "0"),
		attr("diskQuota", "0"),
		attr("diskUsed", "0"),
	)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("user", u))
}

// getUserList：单用户服务器，仅返回内置管理员。
func (a *subsonicAPI) userList() sxNode {
	user := a.cfg.GetDefault("admin_user", "admin")
	u := node("user",
		attr("username", user), attr("email", user+"@localhost"),
		attr("scrobblingEnabled", "true"), attr("adminRole", "true"),
		attr("settingsRole", "true"), attr("streamRole", "true"),
		attr("downloadRole", "true"), attr("uploadRole", "false"),
		attr("commentRole", "false"), attr("podcastRole", "false"),
		attr("shareRole", "false"), attr("coverArtRole", "true"),
		attr("playlistRole", "true"), attr("jukeboxRole", "false"), attr("folder", "0"),
	)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("users", node("users", u)))
}

// changePassword is intentionally not a no-op: credentials are managed by the
// Lnh-Music-Tag Settings UI, so clients must not be told that a password changed.
func (a *subsonicAPI) changePassword(r *http.Request) sxNode {
	return apiError(0, "changePassword is not supported; change the password in Lnh-Music-Tag Settings")
}

// similarSongs：按同歌手(权重3)、同专辑(2)、同流派(1)打分推荐，排除自身。
func (a *subsonicAPI) similarSongs(r *http.Request, v2 bool) sxNode {
	q := r.URL.Query()
	src := a.track(q.Get("id"))
	count, _ := strconv.Atoi(q.Get("count"))
	if count <= 0 || count > 100 {
		count = 50
	}
	type cand struct {
		t     *model.Track
		score int
	}
	seen := map[string]bool{}
	var list []cand
	if src != nil {
		sArtist, sAlbum, sGenre := songArtist(src), songAlbum(src), tag(src, "GENRE")
		for _, t := range a.all() {
			if t.ID == src.ID || seen[t.ID] {
				continue
			}
			score := 0
			if songArtist(t) == sArtist {
				score += 3
			}
			if songAlbum(t) == sAlbum {
				score += 2
			}
			if sGenre != "" && tag(t, "GENRE") == sGenre {
				score += 1
			}
			if score > 0 {
				seen[t.ID] = true
				list = append(list, cand{t, score})
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	}
	if len(list) > count {
		list = list[:count]
	}
	children := make([]sxNode, 0, len(list))
	for _, c := range list {
		children = append(children, a.songNode(c.t))
	}
	name := "similarSongs"
	if v2 {
		name = "similarSongs2"
	}
	inner := node(name, children...)
	if len(children) == 0 {
		inner = emptyArrayNode(name)
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(name, inner))
}

// radioStations：无外部电台源，返回结构合法的空电台列表。
func (a *subsonicAPI) radioStations() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("internetRadioStations", emptyArrayNode("internetRadioStations")))
}

// podcasts：本地库不订阅播客，返回合法空频道结构；newest 仅取最新播客集（同样为空）。
func (a *subsonicAPI) podcasts(newest bool) sxNode {
	if newest {
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("newestPodcasts", emptyArrayNode("newestPodcasts")))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("podcasts", emptyArrayNode("podcasts")))
}

// ---- 书签（createBookmark / deleteBookmark / getBookmarks），持久化到配置 ----

type subsonicBookmark struct {
	EntryID  string `json:"entryId"`
	Position int64  `json:"position"`
	Username string `json:"username"`
	Created  string `json:"created"`
	Changed  string `json:"changed"`
	Comment  string `json:"comment"`
}

func (a *subsonicAPI) loadBookmarks() []subsonicBookmark {
	var bs []subsonicBookmark
	_ = json.Unmarshal([]byte(a.cfg.GetDefault("streaming_bookmarks", "[]")), &bs)
	if bs == nil {
		bs = []subsonicBookmark{}
	}
	return bs
}
func (a *subsonicAPI) saveBookmarks(bs []subsonicBookmark) {
	if b, err := json.Marshal(bs); err == nil {
		_ = a.cfg.Set("streaming_bookmarks", string(b))
	}
}
func (a *subsonicAPI) bookmarks() sxNode {
	var items []sxNode
	for _, bm := range a.loadBookmarks() {
		t := a.track(bm.EntryID)
		if t == nil {
			continue
		}
		entry := node("entry", a.songNode(t))
		bmNode := node("bookmark",
			attr("position", strconv.FormatInt(bm.Position, 10)),
			attr("username", bm.Username),
			attr("created", bm.Created),
			attr("changed", bm.Changed),
			attr("comment", bm.Comment),
			entry,
		)
		items = append(items, bmNode)
	}
	inner := node("bookmarks", items...)
	if len(items) == 0 {
		inner = emptyArrayNode("bookmarks")
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("bookmarks", inner))
}
func (a *subsonicAPI) createBookmark(r *http.Request) sxNode {
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" || q.Get("position") == "" {
		return apiError(10, "Missing required parameter: id or position")
	}
	if a.track(id) == nil {
		return apiError(70, "Song not found")
	}
	pos, err := strconv.ParseInt(q.Get("position"), 10, 64)
	if err != nil || pos < 0 {
		return apiError(10, "Invalid bookmark position")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	user := a.cfg.GetDefault("admin_user", "admin")
	bs := a.loadBookmarks()
	found := false
	for i := range bs {
		if bs[i].EntryID == id {
			bs[i].Position, bs[i].Changed, bs[i].Username, bs[i].Comment = pos, now, user, q.Get("comment")
			found = true
			break
		}
	}
	if !found {
		bs = append(bs, subsonicBookmark{EntryID: id, Position: pos, Username: user, Created: now, Changed: now, Comment: q.Get("comment")})
	}
	a.saveBookmarks(bs)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}
func (a *subsonicAPI) deleteBookmark(r *http.Request) sxNode {
	id := r.URL.Query().Get("id")
	if id == "" {
		return apiError(10, "Missing required parameter: id")
	}
	bs := a.loadBookmarks()
	out := bs[:0]
	found := false
	for _, bm := range bs {
		if bm.EntryID == id {
			found = true
			continue
		}
		out = append(out, bm)
	}
	if !found {
		return apiError(70, "Bookmark not found")
	}
	a.saveBookmarks(out)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}

// ---- 分享（本地单用户，不生成外部分享链接，返回合法空结构） ----

func (a *subsonicAPI) shares() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("shares", emptyArrayNode("shares")))
}
func (a *subsonicAPI) createShare(r *http.Request) sxNode {
	return apiError(0, "createShare is not supported; this server does not create public shares")
}
func (a *subsonicAPI) deleteShare(r *http.Request) sxNode {
	return apiError(0, "deleteShare is not supported; this server does not create public shares")
}

// ---- 播放队列（getPlayQueue / savePlayQueue），持久化到配置 ----

type subsonicPlayQueue struct {
	Current  string   `json:"current"`
	Position int      `json:"position"`
	Entries  []string `json:"entries"`
	Changed  string   `json:"changed"`
	Username string   `json:"username"`
}

func (a *subsonicAPI) loadPlayQueue() subsonicPlayQueue {
	var pq subsonicPlayQueue
	_ = json.Unmarshal([]byte(a.cfg.GetDefault("streaming_playqueue", "{}")), &pq)
	if pq.Entries == nil {
		pq.Entries = []string{}
	}
	return pq
}
func (a *subsonicAPI) savePlayQueue(r *http.Request) sxNode {
	q := r.URL.Query()
	entries := q["id"]
	valid := make([]string, 0, len(entries))
	for _, id := range entries {
		if a.track(id) == nil {
			return apiError(70, "Song not found: "+id)
		}
		valid = append(valid, id)
	}
	current := q.Get("current")
	if current != "" && a.track(current) == nil {
		return apiError(70, "Current song not found: "+current)
	}
	pos, err := strconv.Atoi(q.Get("position"))
	if q.Get("position") != "" && (err != nil || pos < 0) {
		return apiError(10, "Invalid play queue position")
	}
	pq := subsonicPlayQueue{
		Current:  current,
		Position: pos,
		Entries:  valid,
		Changed:  time.Now().UTC().Format(time.RFC3339),
		Username: a.cfg.GetDefault("admin_user", "admin"),
	}
	if b, err := json.Marshal(pq); err == nil {
		_ = a.cfg.Set("streaming_playqueue", string(b))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}
func (a *subsonicAPI) playQueue() sxNode {
	pq := a.loadPlayQueue()
	children := make([]sxNode, 0, len(pq.Entries))
	for _, id := range pq.Entries {
		if t := a.track(id); t != nil {
			children = append(children, a.songNode(t))
		}
	}
	n := node("playQueue",
		attr("current", pq.Current),
		attr("position", strconv.Itoa(pq.Position)),
		attr("username", pq.Username),
		attr("changed", pq.Changed),
		attr("savedBy", pq.Username),
	)
	n.Children = children
	inner := sxNode(n)
	if len(children) == 0 {
		inner = emptyArrayNode("playQueue")
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("playQueue", inner))
}

// jukeboxControl：本地无硬件点唱机，只读取队列/状态；播放控制动作明确返回不支持。
func (a *subsonicAPI) jukebox(r *http.Request) sxNode {
	action := r.URL.Query().Get("action")
	status := node("jukeboxStatus",
		attr("currentIndex", "0"), attr("position", "0"),
		attr("playing", "false"), attr("gain", "1.0"),
	)
	if action == "status" {
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("jukeboxStatus", status))
	}
	if action == "get" {
		var items []sxNode
		for _, id := range a.loadPlayQueue().Entries {
			if t := a.track(id); t != nil {
				items = append(items, a.songNode(t))
			}
		}
		inner := node("jukeboxPlaylist", append([]sxNode{status}, items...)...)
		if len(items) == 0 {
			inner = emptyArrayNode("jukeboxPlaylist")
		}
		return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion),
			child("jukeboxPlaylist", inner))
	}
	return apiError(0, "jukeboxControl action is not supported; this server has no attached audio output")
}

// downloads：本地下载目录概念不适用，返回合法空下载列表。
func (a *subsonicAPI) downloads() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("downloads", emptyArrayNode("downloads")))
}
func (a *subsonicAPI) starred(v2 bool) sxNode {
	var songs, albums, artists []sxNode
	all := a.all()
	for _, id := range a.loadStarred() {
		if t := a.track(id); t != nil {
			songs = append(songs, a.songNode(t))
			continue
		}
		var albumTracks []*model.Track
		for _, t := range all {
			if albumID(t) == id {
				albumTracks = append(albumTracks, t)
			}
		}
		if len(albumTracks) > 0 {
			// The same deterministic grouping/id logic is used by getAlbum.
			albums = append(albums, albumNode(albumTracks, id, songAlbum(albumTracks[0]), songArtist(albumTracks[0]), artistID(albumTracks[0])))
			continue
		}
		var artistName string
		var songCount int
		albumSet := map[string]bool{}
		for _, t := range all {
			if artistID(t) == id {
				artistName = songArtist(t)
				songCount++
				albumSet[albumID(t)] = true
			}
		}
		if artistName != "" {
			artists = append(artists, node("artist", attr("id", id), attr("name", artistName), attr("albumCount", strconv.Itoa(len(albumSet))), attr("songCount", strconv.Itoa(songCount)), attr("trackCount", strconv.Itoa(songCount))))
		}
	}
	sort.Slice(albums, func(i, j int) bool { return albums[i].Attr["name"] < albums[j].Attr["name"] })
	sort.Slice(artists, func(i, j int) bool { return artists[i].Attr["name"] < artists[j].Attr["name"] })
	name := "starred"
	if v2 {
		name = "starred2"
	}
	children := append(songs, albums...)
	children = append(children, artists...)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(name, node(name, children...)))
}
func (a *subsonicAPI) scrobble(r *http.Request) sxNode {
	q := r.URL.Query()
	ids := q["id"]
	submission := q.Get("submission") != "false"
	a.mu.Lock()
	for _, id := range ids {
		if a.track(id) == nil {
			continue
		}
		if submission {
			delete(a.nowPlaying, id)
			a.playCounts[id]++
		} else {
			a.nowPlaying[id] = time.Now()
		}
	}
	a.mu.Unlock()
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}
func (a *subsonicAPI) nowPlayingNode() sxNode {
	a.mu.Lock()
	defer a.mu.Unlock()
	var entries []sxNode
	for id, started := range a.nowPlaying {
		if t := a.track(id); t != nil {
			n := a.songNode(t)
			n.Name = "entry"
			n.Attr["username"] = a.cfg.GetDefault("admin_user", "admin")
			n.Attr["minutesAgo"] = strconv.Itoa(int(time.Since(started).Minutes()))
			entries = append(entries, n)
		}
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("nowPlaying", node("nowPlaying", entries...)))
}

func (a *subsonicAPI) stream(w http.ResponseWriter, r *http.Request, op string) {
	t := a.track(r.URL.Query().Get("id"))
	if t == nil {
		http.Error(w, "song not found", http.StatusNotFound)
		return
	}
	a.recMu.Lock()
	a.lastStreamed = t.ID
	a.recMu.Unlock()
	q := r.URL.Query()
	// download 标准语义是返回原始文件（附件），不按 format/maxBitrate 转码；
	// 只有 stream 才按请求实时转码。若让下载也走转码，非 mp3 源在转码失败时
	// 会表现为“下载失败”，故 download 始终直传原始文件。
	download := op == "download"
	if download {
		streamTrack(w, r, a.cfg, t, "", "", true)
	} else {
		streamTrack(w, r, a.cfg, t, q.Get("format"), bitRateParam(q), false)
	}
}
func urlEscape(s string) string { return url.PathEscape(s) }
func (a *subsonicAPI) cover(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	t := a.track(id)
	// 专辑 id(al-) 兜底：取该专辑任一曲目封面。
	if t == nil {
		for _, x := range a.all() {
			if albumID(x) == id {
				t = x
				break
			}
		}
	}
	// 艺术家(ar-)或 artist_name：取该艺人任一曲目的内嵌封面作为头像。
	if t == nil && (strings.HasPrefix(id, "ar-") || q.Get("artist_name") != "") {
		want := id
		if name := q.Get("artist_name"); name != "" {
			want = artistIDByName(name)
		}
		for _, x := range a.all() {
			if artistID(x) == want {
				t = x
				break
			}
		}
	}
	// 歌单(pl-)：取歌单第一首有效曲目的封面。
	if t == nil && strings.HasPrefix(id, "pl-") {
		for _, pl := range a.loadPlaylists() {
			if pl.ID == id {
				for _, sid := range pl.TrackIDs {
					if x := a.track(sid); x != nil {
						t = x
						break
					}
				}
				break
			}
		}
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(t.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// 共享 store 封面缓存（与 /api/cover 同一缓存，mtime 变化自动失效）：
	// 避免每次 getCoverArt 都走 WASM 读文件+解码，封面是网关中体积最大、
	// 重复率最高的响应，缓存收益明确。
	a.library.coverMu.RLock()
	if e, ok := a.library.coverCache[t.ID]; ok && e.mod.Equal(st.ModTime()) {
		img := e.img
		a.library.coverMu.RUnlock()
		writeCover(w, img)
		return
	}
	a.library.coverMu.RUnlock()
	img, err := taglibx.ReadCover(t.Path)
	// 兜底：当前曲目无内嵌封面时，取同专辑任一曲目的封面（专辑级封面），
	// 避免播放器（如 Archer/音流 的 CarPlay 界面）请求歌曲封面时 404 导致无图。
	if err != nil || len(img) == 0 {
		aid := albumID(t)
		for _, x := range a.all() {
			if x == nil || albumID(x) != aid {
				continue
			}
			if img2, err2 := taglibx.ReadCover(x.Path); err2 == nil && len(img2) > 0 {
				img = img2
				break
			}
		}
	}
	if len(img) == 0 {
		http.NotFound(w, r)
		return
	}
	a.library.coverMu.Lock()
	a.library.coverCache[t.ID] = coverEntry{mod: st.ModTime(), img: img}
	a.library.coverMu.Unlock()
	writeCover(w, img)
}

// writeCover 统一写封面响应头与字节（Subsonic getCoverArt 专用出口）。
func writeCover(w http.ResponseWriter, img []byte) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", http.DetectContentType(img))
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	_, _ = w.Write(img)
}
