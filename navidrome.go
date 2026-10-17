package main

// Navidrome native REST API (Music API) for third-party clients that connect
// in "Navidrome" mode (e.g. 箭头音乐 Amcfy Navidrome mode, 音流 Musiver Navidrome
// mode, Feishin, Supersonic, NSMusicS...). Navidrome clients hit these endpoints
// directly: POST /auth/login -> Bearer token, then /api/song|album|artist|playlist
// |stream|coverart|search|genres|albumlist|songlist|library/scanstatus|lyrics.
//
// The adapter intentionally shares the same in-memory library as the Subsonic and
// Jellyfin/Emby gateways; response shapes follow Navidrome's JSON schema.

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

type navidromeAPI struct {
	library *store
	cfg     *cstore.Config
}

func navidromeHandler(s *store, cfg *cstore.Config) http.Handler {
	n := &navidromeAPI{library: s, cfg: cfg}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		log.Printf("NAVIDROME %s %s from %s", r.Method, r.URL.RequestURI(), r.RemoteAddr)
		n.serve(w, r)
	})
}

func (n *navidromeAPI) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && (path == "/auth/login" || path == "/auth/token"):
		n.login(w, r)
		return
	case r.Method == http.MethodGet && path == "/ping":
		// 对齐真实 Navidrome：{"status":"ok","version":"<ver>"}；保留 ok 兼容旧客户端
		n.json(w, http.StatusOK, map[string]any{"ok": true, "status": "ok", "version": AppVersion()})
		return
	case strings.HasPrefix(path, "/api/"):
		if !n.authorized(r) {
			n.json(w, http.StatusUnauthorized, map[string]any{"error": "未登录或会话过期", "ok": false})
			return
		}
		n.api(w, r, strings.Trim(strings.TrimPrefix(path, "/api/"), "/"))
		return
	}
	http.NotFound(w, r)
}

// ---- helpers ---------------------------------------------------------------

func (n *navidromeAPI) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (n *navidromeAPI) listJSON(w http.ResponseWriter, data []any, total int) {
	// Navidrome 列表接口返回裸 JSON 数组，总数通过 x-total-count 响应头传递
	w.Header().Set("x-total-count", strconv.Itoa(total))
	_ = json.NewEncoder(w).Encode(data)
}
func (n *navidromeAPI) all() []*model.Track          { return n.library.all() }
func (n *navidromeAPI) track(id string) *model.Track { return n.library.get(id) }

func (n *navidromeAPI) wantUser() string {
	return n.cfg.GetDefault("admin_user", "admin")
}

// token base: stream_pass 优先（可派生校验），否则用 admin_pass 哈希本身。
func (n *navidromeAPI) tokenBase() string {
	if p := strings.TrimSpace(n.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS"))); p != "" {
		return p
	}
	stored, _ := n.cfg.Get("admin_pass")
	return stored
}
func (n *navidromeAPI) tokenFor(user string) string {
	h := md5.Sum([]byte(n.tokenBase() + "|" + user + "|lnh-navidrome"))
	return hex.EncodeToString(h[:])
}

// Navidrome auth/login 返回的 subsonicToken/subsonicSalt 供客户端回退到
// Subsonic 认证（如 getCoverArt 带 t/s 参数）。Subsonic token = md5(password+salt)，
// 只能由流媒体明文密码派生（管理员密码是单向哈希）。
func (n *navidromeAPI) subsonicToken() (string, string) {
	const salt = "lnh-navidrome"
	p := strings.TrimSpace(n.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if p == "" {
		return "", salt
	}
	h := md5.Sum([]byte(p + salt))
	return hex.EncodeToString(h[:]), salt
}

func (n *navidromeAPI) login(w http.ResponseWriter, r *http.Request) {
	var x struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&x)
	}
	if x.Username == "" {
		q := r.URL.Query()
		x.Username, x.Password = q.Get("username"), q.Get("password")
	}
	wantUser := n.wantUser()
	stored, _ := n.cfg.Get("admin_pass")
	ok := subtle.ConstantTimeCompare([]byte(x.Username), []byte(wantUser)) == 1 && verifyPassword(stored, x.Password)
	if !ok {
		streamPass := strings.TrimSpace(n.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
		ok = subtle.ConstantTimeCompare([]byte(x.Username), []byte(wantUser)) == 1 && streamPass != "" && subtle.ConstantTimeCompare([]byte(x.Password), []byte(streamPass)) == 1
	}
	if !ok {
		n.json(w, http.StatusUnauthorized, map[string]any{"error": "Invalid username or password", "ok": false})
		return
	}
	token := n.tokenFor(wantUser)
	subTok, subSalt := n.subsonicToken()
	now := time.Now().UTC().Format(time.RFC3339)
	n.json(w, http.StatusOK, map[string]any{
		"token":         token,
		"id":            wantUser,
		"username":      wantUser,
		"name":          wantUser,
		"email":         "",
		"isAdmin":       true,
		"isExternal":    false,
		"lastLoginAt":   now,
		"lastAccessAt":  now,
		"subsonicToken": subTok,
		"subsonicSalt":  subSalt,
		"createdAt":     now,
		"updatedAt":     now,
	})
}
func (n *navidromeAPI) authorized(r *http.Request) bool {
	token := ""
	for _, h := range []string{r.Header.Get("Authorization"), r.Header.Get("X-ND-Authorization")} {
		if strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
			break
		}
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return false
	}
	want := n.tokenFor(n.wantUser())
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(token)), []byte(want)) == 1
}

func paginationND(r *http.Request) (start, end int) {
	start, _ = strconv.Atoi(r.URL.Query().Get("_start"))
	if start == 0 {
		if s := r.URL.Query().Get("start"); s != "" {
			start, _ = strconv.Atoi(s)
		}
	}
	if start == 0 {
		if s := r.URL.Query().Get("offset"); s != "" {
			start, _ = strconv.Atoi(s)
		}
	}
	end, _ = strconv.Atoi(r.URL.Query().Get("_end"))
	if end == 0 {
		if v := r.URL.Query().Get("_limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				end = start + n
			}
		}
	}
	if end == 0 {
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				end = start + n
			}
		}
	}
	if end == 0 {
		if s := r.URL.Query().Get("size"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				end = start + v
			}
		}
	}
	if end <= start {
		end = start + 100
	}
	return
}
func sliceRange(total, start, end int) (int, int) {
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	return start, end
}

// ---- data model ------------------------------------------------------------

type ndAlbum struct {
	id, name, artist, artistID, genre string
	songs                             []*model.Track
	year                              int
}
type ndArtist struct {
	id, name string
	albums   []ndAlbum
	songs    int
}

func (n *navidromeAPI) albumGroups() []ndAlbum {
	groups := map[string][]*model.Track{}
	for _, t := range n.all() {
		k := songArtist(t) + "\x00" + songAlbum(t)
		groups[k] = append(groups[k], t)
	}
	out := make([]ndAlbum, 0, len(groups))
	for k, ts := range groups {
		year := 0
		if y := tag(ts[0], "YEAR", "DATE"); y != "" {
			year, _ = strconv.Atoi(firstYear(y))
		}
		out = append(out, ndAlbum{
			id: stableID("al-", k), name: songAlbum(ts[0]), artist: songArtist(ts[0]),
			artistID: artistID(ts[0]), genre: tag(ts[0], "GENRE"), songs: ts, year: year,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
func (n *navidromeAPI) artistList() []ndArtist {
	byArtist := map[string][]*model.Track{}
	for _, t := range n.all() {
		byArtist[songArtist(t)] = append(byArtist[songArtist(t)], t)
	}
	out := make([]ndArtist, 0, len(byArtist))
	for name, ts := range byArtist {
		albums := map[string][]*model.Track{}
		for _, t := range ts {
			albums[songAlbum(t)] = append(albums[songAlbum(t)], t)
		}
		var aList []ndAlbum
		for _, tt := range albums {
			year := 0
			if y := tag(tt[0], "YEAR", "DATE"); y != "" {
				year, _ = strconv.Atoi(firstYear(y))
			}
			aList = append(aList, ndAlbum{
				id: stableID("al-", songArtist(tt[0])+"\x00"+songAlbum(tt[0])), name: songAlbum(tt[0]),
				artist: name, artistID: artistID(ts[0]), genre: tag(tt[0], "GENRE"), songs: tt, year: year,
			})
		}
		sort.Slice(aList, func(i, j int) bool { return aList[i].name < aList[j].name })
		out = append(out, ndArtist{id: artistID(ts[0]), name: name, albums: aList, songs: len(ts)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (n *navidromeAPI) songJSON(t *model.Track) map[string]any {
	year, trackNo := 0, 0
	if y := tag(t, "YEAR", "DATE"); y != "" {
		year, _ = strconv.Atoi(firstYear(y))
	}
	if v := tag(t, "TRACKNUMBER", "TRACK"); v != "" {
		trackNo, _ = strconv.Atoi(firstNumber(v))
	}
	format := strings.ToUpper(strings.TrimPrefix(t.Ext, "."))
	if format == "" {
		format = "MP3"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	yest := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	al := songAlbum(t)
	ar := songArtist(t)
	starred := false
	for _, id := range n.loadStarred() {
		if id == t.ID {
			starred = true
			break
		}
	}
	var starredAt any
	if starred {
		starredAt = now
	}
	return map[string]any{
		"id": t.ID, "title": songTitle(t), "album": al,
		"artist": ar, "artistId": artistID(t),
		"albumArtist": ar, "albumArtistId": artistID(t),
		"albumId": albumID(t), "hasCoverArt": t.HasCover,
		"genre": tag(t, "GENRE"), "year": year,
		"duration": t.Duration, "trackNumber": trackNo, "discNumber": 1,
		"playCount": 0, "starred": starred, "rating": 0, "starredAt": starredAt,
		"size": t.Size, "suffix": strings.TrimPrefix(t.Ext, "."), "format": format,
		"bitRate": int(t.Bitrate), "channels": t.Channels,
		"path": t.FileName, "coverArtId": albumID(t),
		"date": ndDate(year), "fullText": ar + " " + al + " " + songTitle(t),
		"orderTitle": songTitle(t), "orderAlbumName": al,
		"orderArtistName": ar, "orderAlbumArtistName": ar,
		"compilation": false, "lyrics": tag(t, "LYRICS", "UNSYNCEDLYRICS", "UNSYNCED LYRICS"),
		"createdAt": now, "updatedAt": now,
		"playDate": yest, "play_date": yest,
	}
}
func ndDate(y int) string {
	if y > 0 {
		return strconv.Itoa(y)
	}
	return ""
}
func (n *navidromeAPI) albumJSON(a ndAlbum) map[string]any {
	var dur float64
	var size int64
	for _, t := range a.songs {
		dur += t.Duration
		size += t.Size
	}
	now := time.Now().UTC().Format(time.RFC3339)
	yest := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	return map[string]any{
		"id": a.id, "name": a.name, "coverArtId": a.id,
		"songCount": len(a.songs), "duration": dur,
		"playCount": 0, "starred": false, "rating": 0, "starredAt": nil,
		"artist": a.artist, "artistId": a.artistID,
		"albumArtist": a.artist, "albumArtistId": a.artistID,
		"allArtistIds": a.artistID,
		"maxYear":      a.year, "minYear": a.year, "date": ndDate(a.year),
		"maxOriginalYear": 0, "minOriginalYear": 0,
		"releases": 1, "compilation": false,
		"year": a.year, "genre": a.genre, "size": int(size),
		"fullText":       a.artist + " " + a.name,
		"orderAlbumName": a.name, "orderAlbumArtistName": a.artist,
		"paths":     albumPath(a),
		"createdAt": now, "updatedAt": now,
		"playDate": yest, "play_date": yest,
	}
}
func albumPath(a ndAlbum) string {
	if len(a.songs) == 0 {
		return ""
	}
	f := a.songs[0].FileName
	i := strings.LastIndexAny(f, "/\\")
	if i < 0 {
		return f
	}
	return f[:i]
}
func (n *navidromeAPI) artistJSON(a ndArtist) map[string]any {
	cover := ""
	var size int64
	for _, al := range a.albums {
		for _, t := range al.songs {
			size += t.Size
		}
	}
	if len(a.albums) > 0 {
		cover = a.albums[0].id
	}
	now := time.Now().UTC().Format(time.RFC3339)
	yest := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	return map[string]any{
		"id": a.id, "name": a.name, "albumCount": len(a.albums),
		"songCount": a.songs, "playCount": 0, "starred": false,
		"rating": 0, "starredAt": nil,
		"coverArtId": cover, "createdAt": now, "updatedAt": now,
		"playDate": yest, "play_date": yest,
		"fullText": a.name, "orderArtistName": a.name,
		"size": int(size), "genres": nil,
	}
}
func (n *navidromeAPI) playlistJSON(p subsonicPlaylist) map[string]any {
	var dur float64
	var size int64
	for _, id := range p.TrackIDs {
		if t := n.track(id); t != nil {
			dur += t.Duration
			size += t.Size
		}
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "comment": "",
		"owner": n.wantUser(), "public": true,
		"songCount": len(p.TrackIDs), "duration": int(dur), "size": int(size),
		"coverArtId": "", "createdAt": p.Created, "updatedAt": p.Changed,
	}
}

// ---- API dispatch ----------------------------------------------------------

func (n *navidromeAPI) api(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(p, "/")
	switch {
	case len(parts) == 1 && parts[0] == "song":
		n.listSongs(w, r)
	case len(parts) == 2 && parts[0] == "song":
		n.songOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "song" && parts[2] == "stream":
		n.stream(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "album":
		n.listAlbums(w, r)
	case len(parts) == 2 && parts[0] == "album":
		n.albumOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "album" && parts[2] == "songs":
		n.albumSongs(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "album" && parts[2] == "coverart":
		n.cover(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "artist":
		n.listArtists(w, r)
	case len(parts) == 2 && parts[0] == "artist":
		n.artistOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "artist" && parts[2] == "albums":
		n.artistAlbums(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "playlist":
		n.listPlaylists(w, r)
	case len(parts) == 2 && parts[0] == "playlist":
		n.playlistOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "playlist" && parts[2] == "songs":
		n.playlistSongs(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "stream":
		n.stream(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "coverart":
		n.cover(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "search" && parts[1] == "songs":
		n.searchSongs(w, r)
	case len(parts) == 2 && parts[0] == "search" && parts[1] == "albums":
		n.searchAlbums(w, r)
	case len(parts) == 2 && parts[0] == "search" && parts[1] == "artists":
		n.searchArtists(w, r)
	case len(parts) == 1 && parts[0] == "search":
		n.searchAll(w, r)
	case len(parts) == 1 && parts[0] == "starred":
		n.starred(w, r)
	case len(parts) == 1 && parts[0] == "genres":
		n.genres(w, r)
	case len(parts) == 2 && parts[0] == "albumlist":
		n.albumList(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "songlist":
		n.songList(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "library" && parts[1] == "scanstatus":
		n.scanStatus(w, r)
	case len(parts) == 1 && parts[0] == "nowplaying":
		n.nowPlaying(w, r)
	case len(parts) == 2 && parts[0] == "lyrics":
		n.lyrics(w, r, parts[1])
	default:
		n.json(w, http.StatusNotFound, map[string]any{"error": "not found", "ok": false})
	}
}

// ---- song ------------------------------------------------------------------

func ndSortField(r *http.Request) (field string, desc bool) {
	field = strings.ToLower(r.URL.Query().Get("_sort"))
	if field == "" {
		field = strings.ToLower(r.URL.Query().Get("sort"))
	}
	desc = strings.EqualFold(r.URL.Query().Get("_order"), "DESC")
	if !desc {
		desc = strings.EqualFold(r.URL.Query().Get("order"), "DESC")
	}
	return
}
func ndSortTracks(ts []*model.Track, field string) {
	switch field {
	case "title", "name":
		sort.SliceStable(ts, func(i, j int) bool { return songTitle(ts[i]) < songTitle(ts[j]) })
	case "album":
		sort.SliceStable(ts, func(i, j int) bool { return songAlbum(ts[i]) < songAlbum(ts[j]) })
	case "artist":
		sort.SliceStable(ts, func(i, j int) bool { return songArtist(ts[i]) < songArtist(ts[j]) })
	case "year":
		sort.SliceStable(ts, func(i, j int) bool { return ndYear(ts[i]) < ndYear(ts[j]) })
	case "random":
		rand.Shuffle(len(ts), func(i, j int) { ts[i], ts[j] = ts[j], ts[i] })
	}
}
func ndYear(t *model.Track) int {
	if y := tag(t, "YEAR", "DATE"); y != "" {
		if v, err := strconv.Atoi(firstYear(y)); err == nil {
			return v
		}
	}
	return 0
}

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
	// Navidrome 过滤参数：album_id/albumId 仅返回该专辑歌曲
	if aid := r.URL.Query().Get("album_id"); aid != "" {
		filtered := all[:0]
		for _, t := range all {
			if albumID(t) == aid {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	} else if aid := r.URL.Query().Get("albumId"); aid != "" {
		filtered := all[:0]
		for _, t := range all {
			if albumID(t) == aid {
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
	n.listJSON(w, data, len(all))
}
func (n *navidromeAPI) songOne(w http.ResponseWriter, r *http.Request, id string) {
	t := n.track(id)
	if t == nil {
		n.json(w, http.StatusNotFound, map[string]any{"error": "song not found", "ok": false})
		return
	}
	n.json(w, http.StatusOK, n.songJSON(t))
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
	n.listJSON(w, data, len(albums))
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
			n.listJSON(w, data, len(a.songs))
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
	n.listJSON(w, data, len(albums))
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
	n.listJSON(w, data, len(artists))
}
func (n *navidromeAPI) artistOne(w http.ResponseWriter, r *http.Request, id string) {
	for _, a := range n.artistList() {
		if a.id == id {
			n.json(w, http.StatusOK, n.artistJSON(a))
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
			n.listJSON(w, data, len(a.albums))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "artist not found", "ok": false})
}

// ---- playlist -------------------------------------------------------------

func (n *navidromeAPI) listPlaylists(w http.ResponseWriter, r *http.Request) {
	ps := n.loadPlaylists()
	data := make([]any, 0, len(ps))
	for _, p := range ps {
		data = append(data, n.playlistJSON(p))
	}
	n.listJSON(w, data, len(ps))
}
func (n *navidromeAPI) playlistOne(w http.ResponseWriter, r *http.Request, id string) {
	for _, p := range n.loadPlaylists() {
		if p.ID == id {
			n.json(w, http.StatusOK, n.playlistJSON(p))
			return
		}
	}
	n.json(w, http.StatusNotFound, map[string]any{"error": "playlist not found", "ok": false})
}
func (n *navidromeAPI) playlistSongs(w http.ResponseWriter, r *http.Request, id string) {
	for _, p := range n.loadPlaylists() {
		if p.ID == id {
			data := make([]any, 0, len(p.TrackIDs))
			for _, sid := range p.TrackIDs {
				if t := n.track(sid); t != nil {
					data = append(data, n.songJSON(t))
				}
			}
			n.listJSON(w, data, len(p.TrackIDs))
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
	n.listJSON(w, data, len(matched))
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
	n.listJSON(w, data, len(matched))
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
	n.listJSON(w, data, len(matched))
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
	n.listJSON(w, data, len(data))
}
func (n *navidromeAPI) loadStarred() []string {
	var ids []string
	_ = json.Unmarshal([]byte(n.cfg.GetDefault("streaming_starred", "[]")), &ids)
	return ids
}
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
	n.listJSON(w, data, len(data))
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
	n.listJSON(w, data, len(all))
}

// ---- misc ------------------------------------------------------------------

func (n *navidromeAPI) scanStatus(w http.ResponseWriter, r *http.Request) {
	n.json(w, http.StatusOK, map[string]any{"done": true, "progress": 100})
}
func (n *navidromeAPI) nowPlaying(w http.ResponseWriter, r *http.Request) {
	n.listJSON(w, []any{}, 0)
}
func (n *navidromeAPI) lyrics(w http.ResponseWriter, r *http.Request, songID string) {
	t := n.track(songID)
	if t == nil {
		n.json(w, http.StatusNotFound, map[string]any{"error": "song not found", "ok": false})
		return
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
	f, err := os.Open(t.Path)
	if err != nil {
		http.Error(w, "media unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "media unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType(t.Ext))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename*=UTF-8''%s", strings.ReplaceAll(urlEscape(t.FileName), "+", "%20")))
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
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
