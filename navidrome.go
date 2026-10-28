package main

import (
	"bytes"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

type navidromeAPI struct {
	library *store
	cfg     *cstore.Config
	guard   *loginGuard // ND 登录认证限速（防暴力破解）；nil 时不启用（测试构造）
}

// NewNavidromeAPI 导出构造，供主路由在 /api/me 等与 Web 后台共用的路径上做 ND 令牌优先判定。
func NewNavidromeAPI(s *store, cfg *cstore.Config) *navidromeAPI {
	return &navidromeAPI{library: s, cfg: cfg, guard: newLoginGuard()}
}

// Valid 判断本次请求是否携带合法 Navidrome Bearer 令牌。
func (n *navidromeAPI) Valid(r *http.Request) bool { return n.authorized(r) }

// WriteMe 返回 Navidrome 风格的当前用户 DTO（供 /api/me 使用）。
func (n *navidromeAPI) WriteMe(w http.ResponseWriter) {
	now := time.Now().UTC().Format(time.RFC3339)
	user := n.wantUser()
	n.json(w, http.StatusOK, map[string]any{
		"id":           user,
		"username":     user,
		"name":         user,
		"email":        "",
		"isAdmin":      true,
		"isExternal":   false,
		"lastLoginAt":  now,
		"lastAccessAt": now,
		"createdAt":    now,
		"updatedAt":    now,
		"token":        n.tokenFor(user),
		"permissions": map[string]any{
			"admin":          true,
			"download":       true,
			"upload":         false,
			"share":          false,
			"lastFmScrobble": false,
		},
	})
}

func navidromeHandler(s *store, cfg *cstore.Config) http.Handler {
	n := NewNavidromeAPI(s, cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		log.Printf("NAVIDROME method=%s path=%s", r.Method, r.URL.Path)
		n.serve(w, r)
	})
}

func (n *navidromeAPI) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// Stream Music / Musiver configure their Navidrome base URL with a "/rest" suffix
	// (Subsonic convention) and therefore request /rest/auth/login and /rest/api/*.
	// Normalize those paths so the internal routing below stays prefix-agnostic.
	if strings.HasPrefix(path, "/rest/") {
		path = strings.TrimPrefix(path, "/rest")
		r.URL.Path = path
	}
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
func (n *navidromeAPI) listJSON(w http.ResponseWriter, r *http.Request, data []any, total int) {
	// Navidrome 列表接口返回裸 JSON 数组，总数通过 x-total-count 与 Content-Range 响应头传递
	start, end := paginationND(r)
	start, end = sliceRange(total, start, end)
	// Public list fields must always serialize as arrays; nil slices become [].
	if data == nil {
		data = make([]any, 0)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("x-total-count", strconv.Itoa(total))
	if total == 0 || start >= end {
		w.Header().Set("Content-Range", fmt.Sprintf("items */%d", total))
	} else {
		w.Header().Set("Content-Range", fmt.Sprintf("items %d-%d/%d", start, end-1, total))
	}
	_ = json.NewEncoder(w).Encode(data)
}
func (n *navidromeAPI) all() []*model.Track          { return n.library.all() }
func (n *navidromeAPI) track(id string) *model.Track { return n.library.get(id) }

func (n *navidromeAPI) wantUser() string {
	return n.cfg.GetDefault("admin_user", "admin")
}

// token base 仅取流媒体密码 stream_pass（唯一凭证）；未配置则为空，token 校验自然失败。
func (n *navidromeAPI) tokenBase() string {
	return strings.TrimSpace(n.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
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
	// 兼容多种提交方式：JSON body（Navidrome 客户端）、x-www-form-urlencoded
	// （Subsonic 风格客户端）、以及 URL 查询参数。不依赖 Content-Type，因为
	// 部分客户端不设置该头。
	// Authentication payloads are small. Cap reads explicitly even when this handler
	// is mounted outside the browser API middleware.
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	}
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			n.json(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "请求体超过 1 MiB 限制", "ok": false})
		} else {
			n.json(w, http.StatusBadRequest, map[string]any{"error": "无法读取登录请求", "ok": false})
		}
		return
	}
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) > 0 {
		if err := json.Unmarshal(trimmedBody, &x); err != nil && (strings.HasPrefix(string(trimmedBody), "{") || strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json")) {
			n.json(w, http.StatusBadRequest, map[string]any{"error": "登录 JSON 格式错误", "ok": false})
			return
		}
	}
	// form（Subsonic 风格 u/p）兜底：JSON 未解出密码时再尝试。
	if x.Password == "" && r.Method == http.MethodPost && len(bytes.TrimSpace(body)) > 0 {
		if form, err := url.ParseQuery(string(body)); err == nil {
			if v := form.Get("u"); v != "" {
				x.Username = v
			}
			if v := form.Get("username"); v != "" {
				x.Username = v
			}
			if v := form.Get("p"); v != "" {
				x.Password = v
			}
			if v := form.Get("password"); v != "" {
				x.Password = v
			}
		}
	}
	q := r.URL.Query()
	if x.Username == "" {
		x.Username = q.Get("username")
	}
	if x.Username == "" {
		x.Username = q.Get("u")
	}
	if x.Password == "" {
		x.Password = q.Get("password")
	}
	if x.Password == "" {
		x.Password = q.Get("p")
	}
	wantUser := n.wantUser()
	if n.guard != nil && !n.guard.allow(r) {
		n.json(w, http.StatusTooManyRequests, map[string]any{"error": "认证失败次数过多，请 5 分钟后再试", "ok": false})
		return
	}
	// 仅以流媒体密码 stream_pass 为唯一凭证（单用户）：不校验管理员密码，不接受任意残留密码。
	streamPass := strings.TrimSpace(n.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if streamPass == "" {
		n.json(w, http.StatusUnauthorized, map[string]any{"error": "流媒体密码未配置（请先填写 stream_pass）", "ok": false})
		return
	}
	if subtle.ConstantTimeCompare([]byte(x.Password), []byte(streamPass)) != 1 {
		if n.guard != nil {
			n.guard.fail(r)
		}
		n.json(w, http.StatusUnauthorized, map[string]any{"error": "Invalid username or password", "ok": false})
		return
	}
	if n.guard != nil {
		n.guard.success(r)
	}
	token := n.tokenFor(wantUser)
	subTok, subSalt := n.subsonicToken()
	now := time.Now().UTC().Format(time.RFC3339)
	// 同时返回 Navidrome 顶层字段（Stream Music / Musiver 需要 token/subsonicToken）与
	// subsonic-response 嵌套（兼容按 Subsonic 语义调用 /rest/auth/login 的客户端），
	// 让同一端点同时服务两类客户端。
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
		"subsonic-response": map[string]any{
			"status":        "ok",
			"version":       "1.16.1",
			"type":          "Lnh-Music-Tag",
			"serverVersion": AppVersion(),
			"openSubsonic":  true,
		},
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
	if token != "" {
		want := n.tokenFor(n.wantUser())
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(token)), []byte(want)) == 1
	}
	// Navidrome clients (e.g. Stream Music / Musiver) authenticate their /api/* requests with
	// Subsonic-style query parameters (u/s/t) after a successful auth/login. Accept those too:
	// t must equal md5(stream_pass + salt), s must equal the published salt.
	if q := r.URL.Query(); q.Get("u") != "" {
		subTok, subSalt := n.subsonicToken()
		if subTok == "" {
			return false
		}
		if q.Get("s") == subSalt && strings.EqualFold(q.Get("t"), subTok) {
			return true
		}
	}
	return false
}

func paginationND(r *http.Request) (start, end int) {
	parse := func(key string) int {
		v, err := strconv.Atoi(r.URL.Query().Get(key))
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	start = parse("_start")
	if start == 0 {
		start = parse("start")
	}
	if start == 0 {
		start = parse("offset")
	}
	// Prevent overflow and pathological page sizes from reaching slice arithmetic.
	// （单页上限 maxPageSize 统一见 pagination.go）
	end = parse("_end")
	if end == 0 {
		for _, key := range []string{"_limit", "limit", "size"} {
			if raw := r.URL.Query().Get(key); raw != "" {
				limit := parse(key)
				if limit > 0 {
					if limit > maxPageSize {
						limit = maxPageSize
					}
					end = start + limit
					break
				}
			}
		}
	}
	if end <= start {
		if start > int(^uint(0)>>1)-100 {
			end = int(^uint(0) >> 1)
		} else {
			end = start + 100
		}
	}
	if end-start > maxPageSize {
		end = start + maxPageSize
	}
	return
}
func sliceRange(total, start, end int) (int, int) {
	// Defense in depth: callers should use paginationND, but this helper is also
	// safe when called directly with invalid values.
	if total < 0 {
		total = 0
	}
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	if end < start {
		end = start
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
		"playCount": 0, "starred": starred, "rating": ratingFor(n.cfg, t.ID), "starredAt": starredAt,
		"size": t.Size, "suffix": strings.TrimPrefix(t.Ext, "."), "format": format,
		"bitRate": int(t.Bitrate), "channels": t.Channels,
		"path": t.FileName, "coverArtId": albumID(t),
		"date": ndDate(year), "fullText": ar + " " + al + " " + songTitle(t),
		"orderTitle": songTitle(t), "orderAlbumName": al,
		"orderArtistName": ar, "orderAlbumArtistName": ar,
		"compilation": false,
		"createdAt":   now, "updatedAt": now,
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
	resolved := 0
	for _, id := range p.TrackIDs {
		if t := n.track(id); t != nil {
			resolved++
			dur += t.Duration
			size += t.Size
		}
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "comment": "",
		"owner": n.wantUser(), "public": true,
		"songCount": resolved, "duration": int(dur), "size": int(size),
		"coverArtId": "", "createdAt": p.Created, "updatedAt": p.Changed,
	}
}

// ---- API dispatch ----------------------------------------------------------

func (n *navidromeAPI) api(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(p, "/")
	// 路由段统一小写匹配，兼容 Navidrome 官方驼峰（coverArt）与复数别名（tracks）；
	// 库内 id 本身为小写 hex/前缀，不影响取值。
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	switch {
	case len(parts) == 1 && parts[0] == "me":
		n.WriteMe(w)
	case len(parts) == 1 && parts[0] == "song":
		n.listSongs(w, r)
	case len(parts) == 2 && parts[0] == "song":
		n.songOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "song" && parts[2] == "stream":
		n.stream(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "song" && parts[2] == "download":
		n.songDownload(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "album":
		n.listAlbums(w, r)
	case len(parts) == 2 && parts[0] == "album":
		n.albumOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "album" && (parts[2] == "songs" || parts[2] == "tracks"):
		n.albumSongs(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "album" && (parts[2] == "coverart" || parts[2] == "image"):
		n.cover(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "artist":
		n.listArtists(w, r)
	case len(parts) == 2 && parts[0] == "artist":
		n.artistOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "artist" && parts[2] == "albums":
		n.artistAlbums(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "artist" && (parts[2] == "coverart" || parts[2] == "image"):
		n.cover(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "artist" && parts[2] == "tracks":
		n.artistTracks(w, r, parts[1])
	case parts[0] == "playlist" && len(parts) == 1 && r.Method == http.MethodPost:
		n.createPlaylist(w, r)
	case parts[0] == "playlist" && len(parts) == 2 && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		n.playlistRenameDelete(w, r, parts[1])
	case parts[0] == "playlist" && len(parts) == 3 && (parts[2] == "songs" || parts[2] == "tracks") && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		n.playlistSongsWrite(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "playlist":
		n.listPlaylists(w, r)
	case len(parts) == 2 && parts[0] == "playlist":
		n.playlistOne(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "playlist" && (parts[2] == "songs" || parts[2] == "tracks"):
		n.playlistSongs(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "stream":
		n.stream(w, r, parts[1])
	// 封面：/api/coverart/{id}、官方驼峰 /api/coverArt/{id}（段已小写归一），以及 /api/coverArt?id=/artistId= 查询形式
	case len(parts) == 1 && parts[0] == "coverart":
		n.coverQuery(w, r)
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
	case parts[0] == "starred" && len(parts) == 2 && (r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPost):
		n.starWrite(w, r, parts[1])
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
	case (len(parts) == 1 && parts[0] == "statistics") || (len(parts) == 2 && parts[0] == "library" && parts[1] == "statistics"):
		n.statistics(w, r)
	case len(parts) == 1 && parts[0] == "nowplaying":
		n.nowPlaying(w, r)
	case len(parts) == 2 && parts[0] == "lyrics":
		n.lyrics(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "keepalive", len(parts) == 2 && parts[0] == "keepalive" && parts[1] == "keepalive":
		// 音流等客户端登录后通过 keepalive 保活会话；真实 Navidrome 返回 200 {"status":"ok"}。
		n.json(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		// 记录未识别的 ND 请求，便于按真实客户端调用补齐缺失路由（临时诊断日志）。
		log.Printf("[ND] unmatched %s %s", r.Method, r.URL.Path)
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
