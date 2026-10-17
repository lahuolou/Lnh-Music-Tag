package main

// Subsonic/OpenSubsonic compatibility API for third-party music clients.
// This first implementation intentionally focuses on common library and playback endpoints.

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"lnh-music-tag/internal/taglibx"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
)

const subsonicVersion = "1.16.1"

type subsonicAPI struct {
	library      *store
	cfg          *cstore.Config
	mu           sync.Mutex
	nowPlaying   map[string]time.Time
	playCounts   map[string]int
	lastStreamed string
	lastGetSong  string
}
type subsonicPlaylist struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Created  string   `json:"created"`
	Changed  string   `json:"changed"`
	TrackIDs []string `json:"trackIds"`
}

type sxNode struct {
	Name     string
	Attr     map[string]string
	Text     string
	Raw      string // JSON 原样输出（JSON 路径专用；XML 路径由 Children 兜底）
	Children []sxNode
}

func subsonicHandler(s *store, cfg *cstore.Config) http.Handler {
	inner := (&subsonicAPI{library: s, cfg: cfg, nowPlaying: map[string]time.Time{}, playCounts: map[string]int{}}).handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
func (a *subsonicAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, POST, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Subsonic clients may send parameters either in the URL or in an
		// application/x-www-form-urlencoded POST body. Merge POST fields so all
		// handlers can use the same query parsing path.
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err == nil {
				q := r.URL.Query()
				for k, values := range r.PostForm {
					if !q.Has(k) {
						for _, v := range values {
							q.Add(k, v)
						}
					}
				}
				r.URL.RawQuery = q.Encode()
			}
		}
		op := strings.TrimSuffix(filepath.Base(r.URL.Path), ".view")
		if op == "" || op == "." {
			op = "ping"
		}
		log.Printf("SUBSONIC %s ?%s", op, r.URL.RawQuery)
		if op != "ping" && op != "getLicense" && op != "getOpenSubsonicExtensions" && !a.authorized(r) {
			a.respond(w, r, apiError(40, "Wrong username or password"))
			return
		}
		var result sxNode
		switch op {
		case "ping":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
		case "getLicense":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("license", node("license", attr("valid", "true"), attr("email", ""), attr("licenseExpires", "2099-01-01T00:00:00"))))
		case "getOpenSubsonicExtensions":
			// OpenSubsonic：向客户端通告支持的扩展。音流等客户端据此处
			// 的 songLyrics 扩展决定使用 getLyricsBySongId(enhanced) 获取歌词。
			vers := func(vs ...string) sxNode {
				raw := "[" + strings.Join(vs, ",") + "]"
				cs := make([]sxNode, 0, len(vs))
				for _, v := range vs {
					cs = append(cs, node("versions", sxNode{Name: "versions", Text: v}))
				}
				return sxNode{Name: "versions", Raw: raw, Children: cs}
			}
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("openSubsonicExtensions", node("openSubsonicExtensions",
				node("openSubsonicExtension", attr("name", "songLyrics"), vers("1", "2")),
				node("openSubsonicExtension", attr("name", "transcodeOffset"), vers("1")),
			)))
		case "getMusicFolders":
			result = a.musicFolders()
		case "getIndexes":
			result = a.indexes()
		case "getArtists":
			result = a.artists()
		case "getGenres":
			result = a.genres()
		case "getArtist":
			result = a.artist(r.URL.Query().Get("id"))
		case "getArtistInfo", "getArtistInfo2":
			result = a.artistInfo(r.URL.Query().Get("id"))
		case "getAlbumInfo", "getAlbumInfo2":
			result = a.albumInfo(r.URL.Query().Get("id"))
		case "getAlbumList2", "getAlbumList":
			result = a.albumList(r, op == "getAlbumList2")
		case "getMusicDirectory":
			result = a.musicDirectory(r.URL.Query().Get("id"))
		case "getRandomSongs":
			result = a.songCollection(r, "random")
		case "getNewestSongs":
			result = a.songCollection(r, "newest")
		case "getSongsByGenre":
			result = a.songCollection(r, "genre")
		case "getTopSongs":
			result = a.songCollection(r, "top")
		case "getNowPlaying":
			result = a.nowPlayingNode()
		case "getLyrics", "getLyricsBySongId":
			result = a.lyrics(r, op == "getLyricsBySongId")
		case "scrobble":
			result = a.scrobble(r)
		case "star":
			result = a.star(r, true)
		case "unstar":
			result = a.star(r, false)
		case "setRating":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
		case "getAlbum":
			result = a.album(r.URL.Query().Get("id"))
		case "getSong":
			result = a.song(r.URL.Query().Get("id"))
			if t := a.track(r.URL.Query().Get("id")); t != nil {
				a.mu.Lock()
				a.lastGetSong = t.ID
				a.mu.Unlock()
			}
		case "search3":
			result = a.search(r, "searchResult3")
		case "search2":
			result = a.search(r, "searchResult2")
		case "search":
			result = a.search(r, "searchResult")
		case "getScanStatus":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("scanStatus", node("scanStatus", attr("scanning", "false"), attr("count", strconv.Itoa(len(a.all()))))))
		case "startScan":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
		case "stream", "download":
			a.stream(w, r)
			return
		case "getCoverArt":
			a.cover(w, r)
			return
		case "getPlaylists":
			result = a.playlists()
		case "getPlaylist":
			result = a.playlist(r.URL.Query().Get("id"))
		case "createPlaylist":
			result = a.createPlaylist(r)
		case "updatePlaylist":
			result = a.updatePlaylist(r)
		case "deletePlaylist":
			result = a.deletePlaylist(r.URL.Query().Get("id"))
		case "getStarred":
			result = a.starred(false)
		case "getStarred2":
			result = a.starred(true)
		case "getUser":
			result = a.getUser(r)
		case "getUserList":
			result = a.userList()
		case "changePassword":
			result = a.changePassword(r)
		case "getSimilarSongs", "getSimilarSongs2":
			result = a.similarSongs(r, op == "getSimilarSongs2")
		case "getInternetRadioStations":
			result = a.radioStations()
		case "getPodcasts":
			result = a.podcasts(false)
		case "getNewestPodcasts":
			result = a.podcasts(true)
		case "getBookmarks":
			result = a.bookmarks()
		case "createBookmark":
			result = a.createBookmark(r)
		case "deleteBookmark":
			result = a.deleteBookmark(r)
		case "getShares":
			result = a.shares()
		case "createShare":
			result = a.createShare(r)
		case "deleteShare":
			result = a.deleteShare(r)
		case "getPlayQueue":
			result = a.playQueue()
		case "savePlayQueue":
			result = a.savePlayQueue(r)
		case "jukeboxControl":
			result = a.jukebox(r)
		case "getDownloads":
			result = a.downloads()
		case "getAvatar":
			http.NotFound(w, r)
			return
		case "uploadAvatar", "deleteAvatar":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
		case "createUser", "updateUser", "deleteUser":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
		default:
			result = apiError(0, "Unsupported operation: "+op)
		}
		a.respond(w, r, result)
	})
}
func attr(k, v string) sxNode         { return sxNode{Name: "@" + k, Text: v} }
func child(k string, v sxNode) sxNode { v.Name = k; return v }
func node(name string, parts ...sxNode) sxNode {
	n := sxNode{Name: name}
	for _, p := range parts {
		if strings.HasPrefix(p.Name, "@") {
			if n.Attr == nil {
				n.Attr = map[string]string{}
			}
			n.Attr[strings.TrimPrefix(p.Name, "@")] = p.Text
		} else {
			n.Children = append(n.Children, p)
		}
	}
	return n
}
func leaf(name, value string) sxNode { return sxNode{Name: name, Text: value} }

// emptyArrayNode：空列表在 JSON 下输出为 []（而非空对象 {}），XML 下保留空容器元素。
func emptyArrayNode(name string) sxNode {
	n := node(name)
	n.Raw = "[]"
	return n
}
func apiError(code int, msg string) sxNode {
	return node("subsonic-response", attr("status", "failed"), attr("version", subsonicVersion), child("error", node("error", attr("code", strconv.Itoa(code)), attr("message", msg))))
}

func (a *subsonicAPI) authorized(r *http.Request) bool {
	q := r.URL.Query()
	user := q.Get("u")
	supplied := q.Get("p")
	wantUser := a.cfg.GetDefault("admin_user", "admin")
	stored, _ := a.cfg.Get("admin_pass")
	if subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 {
		return false
	}
	// 流媒体明文密码（插件配置 stream_pass 优先，兼容 LNH_STREAM_PASS 环境变量）。
	// 用于 Subsonic token 认证（md5(password+salt)）与可选明文认证。
	streamPass := strings.TrimSpace(a.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	pass := supplied
	if strings.HasPrefix(pass, "enc:") {
		b, err := hex.DecodeString(strings.TrimPrefix(pass, "enc:"))
		if err != nil {
			return false
		}
		pass = string(b)
	}
	// 明文认证：管理员密码（加盐哈希）或流媒体密码
	if pass != "" && verifyPassword(stored, pass) {
		return true
	}
	if pass != "" && streamPass != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(streamPass)) == 1 {
		return true
	}
	// Subsonic token 认证是 MD5(password + salt)。管理员密码是单向哈希、
	// 无法派生 token，故 token 认证要求配置流媒体密码（插件设置页或 LNH_STREAM_PASS）。
	if streamPass == "" {
		return false
	}
	token := q.Get("t")
	salt := q.Get("s")
	if token == "" || salt == "" {
		return false
	}
	sum := md5.Sum([]byte(streamPass + salt))
	want := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(token)), []byte(want)) == 1
}
func (a *subsonicAPI) respond(w http.ResponseWriter, r *http.Request, n sxNode) {
	// OpenSubsonic 标志：告知客户端支持扩展端点（Navidrome 亦如此声明）
	if n.Name == "subsonic-response" {
		if n.Attr == nil {
			n.Attr = map[string]string{}
		}
		n.Attr["openSubsonic"] = "true"
		// OpenSubsonic 规范：serverVersion/type 为根响应必带字段；
		// 音流等 Navidrome 模式客户端依赖 serverVersion 判定服务器版本
		if n.Attr["serverVersion"] == "" {
			n.Attr["serverVersion"] = AppVersion()
		}
		if n.Attr["type"] == "" {
			n.Attr["type"] = "navidrome"
		}
	}
	if strings.EqualFold(r.URL.Query().Get("f"), "json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": sxJSON(n)})
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	_ = enc.Encode(n)
	_ = enc.Flush()
}
var sxJSONNumeric = map[string]bool{
	"track": true, "year": true, "size": true, "duration": true, "bitRate": true,
	"playCount": true, "discNumber": true, "songCount": true, "albumCount": true, "trackCount": true,
	"childCount": true, "userRating": true, "averageRating": true, "channelCount": true,
	"maxBitRate": true, "similarArtistCount": true, "artistCount": true,
	"position": true, "start": true, "offset": true, "total": true,
	"quota": true, "diskQuota": true, "diskUsed": true, "currentIndex": true, "gain": true, "count": true,
}
var sxJSONBoolean = map[string]bool{
	"isDir": true, "isVideo": true, "openSubsonic": true, "isCompilation": true,
	"public": true, "scrobblingEnabled": true, "adminRole": true, "settingsRole": true,
	"streamRole": true, "jukeboxRole": true, "downloadRole": true, "uploadRole": true,
	"playlistRole": true, "coverArtRole": true, "commentRole": true, "podcastRole": true,
	"shareRole": true, "videoConversionRole": true, "isExternal": true, "isAdmin": true, "playing": true,
}

func sxJSON(n sxNode) any {
	if n.Raw != "" {
		return json.RawMessage(n.Raw)
	}
	m := map[string]any{}
	for k, v := range n.Attr {
		if sxJSONBoolean[k] && (v == "true" || v == "false") {
			m[k] = v == "true"
		} else if sxJSONNumeric[k] {
			// OpenSubsonic JSON：数值字段必须是 JSON 数字（Navidrome 同此），
			// 音流等严格解析器对字符串数字会解析失败
			if iv, err := strconv.Atoi(v); err == nil {
				m[k] = iv
			} else if fv, err := strconv.ParseFloat(v, 64); err == nil {
				m[k] = fv
			} else {
				m[k] = v
			}
		} else {
			m[k] = v
		}
	}
	if n.Text != "" {
		if len(m) == 0 && len(n.Children) == 0 {
			return n.Text
		}
		m["value"] = n.Text
	}
	counts := map[string]int{}
	for _, c := range n.Children {
		counts[c.Name]++
	}
	for _, c := range n.Children {
		v := sxJSON(c)
		if counts[c.Name] > 1 || sxForceArray(n.Name, c.Name) {
			if old, ok := m[c.Name]; ok {
				if arr, ok := old.([]any); ok {
					m[c.Name] = append(arr, v)
				} else {
					m[c.Name] = []any{old, v}
				}
			} else {
				m[c.Name] = []any{v}
			}
		} else {
			m[c.Name] = v
		}
	}
	// searchResult2/3：song/album/artist 三键必须存在（即使为空数组）
	if n.Name == "searchResult2" || n.Name == "searchResult3" {
		for _, k := range []string{"song", "album", "artist"} {
			if _, ok := m[k]; !ok {
				m[k] = []any{}
			}
		}
	}
	return m
}
func sxForceArray(parent, child string) bool {
	switch parent {
	case "openSubsonicExtensions":
		return child == "openSubsonicExtension"
	case "indexes":
		return child == "index"
	case "index":
		return child == "artist"
	case "artists":
		return child == "index"
	case "albums":
		return child == "album"
	case "albumList", "albumList2", "randomSongs", "newestSongs", "songsByGenre", "topSongs", "starred", "starred2":
		return child == "song" || child == "album" || child == "artist"
	case "searchResult2", "searchResult3":
		return child == "song" || child == "album" || child == "artist"
	case "playlists":
		return child == "playlist"
	case "musicFolders":
		return child == "musicFolder"
	case "playlist", "album", "directory":
		return child == "song" || child == "child"
	case "artist":
		return child == "album"
	case "nowPlaying":
		return child == "entry"
	case "genres":
		return child == "genre"
	case "lyricsList":
		return child == "structuredLyrics"
	case "structuredLyrics":
		return child == "line"
	case "users":
		return child == "user"
	case "bookmarks":
		return child == "bookmark"
	case "similarSongs", "similarSongs2":
		return child == "song" || child == "child"
	case "jukeboxPlaylist":
		return child == "song" || child == "child"
	case "playQueue":
		return child == "song" || child == "child"
	case "shares":
		return child == "share"
	case "internetRadioStations":
		return child == "station"
	case "podcasts":
		return child == "channel"
	case "newestPodcasts":
		return child == "episode"
	case "downloads":
		return child == "download"
	default:
		return false
	}
}

func (n sxNode) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Name.Local = n.Name
	for k, v := range n.Attr {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: k}, Value: v})
	}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if n.Text != "" {
		if err := e.EncodeToken(xml.CharData([]byte(n.Text))); err != nil {
			return err
		}
	}
	for _, c := range n.Children {
		if err := e.Encode(c); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}
var lrcLineRe = regexp.MustCompile(`^\s*\[(\d{1,3}):(\d{2})(?:\.\d{1,3})?\]\s*(.*)$`)

func tag(t *model.Track, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(t.Tags[k]); v != "" {
			return v
		}
	}
	return ""
}
func songTitle(t *model.Track) string {
	if v := tag(t, "TITLE", "title"); v != "" {
		return v
	}
	return strings.TrimSuffix(t.FileName, filepath.Ext(t.FileName))
}
func songArtist(t *model.Track) string {
	if v := tag(t, "ARTIST", "artist", "ALBUMARTIST"); v != "" {
		return v
	}
	return "Unknown Artist"
}
func songAlbum(t *model.Track) string {
	if v := tag(t, "ALBUM", "album"); v != "" {
		return v
	}
	return "Unknown Album"
}
func stableID(prefix, s string) string {
	h := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(s))))
	return prefix + hex.EncodeToString(h[:8])
}
func artistID(t *model.Track) string                { return stableID("ar-", songArtist(t)) }
func albumID(t *model.Track) string                 { return stableID("al-", songArtist(t)+"\x00"+songAlbum(t)) }
func (a *subsonicAPI) track(id string) *model.Track { return a.library.get(id) }
func (a *subsonicAPI) all() []*model.Track          { return a.library.all() }
func (a *subsonicAPI) songNode(t *model.Track) sxNode {
	attrs := []sxNode{attr("id", t.ID), attr("parent", albumID(t)), attr("isDir", "false"), attr("title", songTitle(t)), attr("album", songAlbum(t)), attr("artist", songArtist(t)), attr("albumId", albumID(t)), attr("artistId", artistID(t)), attr("coverArt", t.ID), attr("contentType", contentType(t.Ext)), attr("suffix", strings.TrimPrefix(strings.ToLower(t.Ext), ".")), attr("duration", strconv.Itoa(int(t.Duration))), attr("bitRate", strconv.FormatUint(uint64(t.Bitrate), 10)), attr("size", strconv.FormatInt(t.Size, 10)), attr("path", t.FileName), attr("type", "music"), attr("isVideo", "false"), attr("created", time.Now().UTC().Format(time.RFC3339))}
	if y := tag(t, "YEAR", "DATE"); y != "" {
		attrs = append(attrs, attr("year", firstYear(y)))
	}
	if g := tag(t, "GENRE"); g != "" {
		attrs = append(attrs, attr("genre", g))
	}
	if n := tag(t, "TRACKNUMBER", "TRACK"); n != "" {
		attrs = append(attrs, attr("track", firstNumber(n)))
	}
	return node("song", attrs...)
}
func firstYear(s string) string {
	if len(s) >= 4 {
		return s[:4]
	}
	return s
}
func firstNumber(s string) string { s = strings.SplitN(s, "/", 2)[0]; return s }
func contentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".wma":
		return "audio/x-ms-wma"
	default:
		return "application/octet-stream"
	}
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
	counts := map[string]int{}
	for _, t := range tracks {
		counts[songArtist(t)]++
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
		groups[key] = append(groups[key], node("artist", attr("id", artistID(t)), attr("name", name), attr("coverArt", albumID(t)), attr("childCount", strconv.Itoa(counts[name]))))
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
	for _, t := range a.all() {
		ar := songArtist(t)
		songCounts[ar]++
		if albumSets[ar] == nil {
			albumSets[ar] = map[string]bool{}
		}
		albumSets[ar][songAlbum(t)] = true
	}
	for ar, albums := range albumSets {
		groups[artistIndexName(ar)] = append(groups[artistIndexName(ar)], node("artist", attr("id", stableID("ar-", ar)), attr("name", ar), attr("albumCount", strconv.Itoa(len(albums))), attr("songCount", strconv.Itoa(songCounts[ar])), attr("trackCount", strconv.Itoa(songCounts[ar]))))
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
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("artists", node("artists", indices...)))
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
	attrs := []sxNode{attr("id", id), attr("name", name), attr("isDir", "true"), attr("artist", artist), attr("artistId", artistId), attr("songCount", strconv.Itoa(len(ts))), attr("coverArt", ts[0].ID)}
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
	for _, t := range a.all() {
		if artistID(t) == id {
			ar = songArtist(t)
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
		}
	}
	var arr []sxNode
	var songTotal int
	for name, ts := range albums {
		songTotal += len(ts)
		arr = append(arr, albumNode(ts, stableID("al-", ar+"\x00"+name), name, ar, id))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("artist", node("artist", append([]sxNode{attr("id", id), attr("name", ar), attr("albumCount", strconv.Itoa(len(albums))), attr("songCount", strconv.Itoa(songTotal)), attr("trackCount", strconv.Itoa(songTotal))}, arr...)...)))
}
func (a *subsonicAPI) albumList(r *http.Request, v2 bool) sxNode {
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 || size > 500 {
		size = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	groups := map[string][]*model.Track{}
	for _, t := range a.all() {
		k := songArtist(t) + "\x00" + songAlbum(t)
		groups[k] = append(groups[k], t)
	}
	var albums []sxNode
	for k, ts := range groups {
		albums = append(albums, albumNode(ts, stableID("al-", k), songAlbum(ts[0]), songArtist(ts[0]), artistID(ts[0])))
	}
	sort.Slice(albums, func(i, j int) bool { return albums[i].Attr["name"] < albums[j].Attr["name"] })
	if offset > len(albums) {
		offset = len(albums)
	}
	end := offset + size
	if end > len(albums) {
		end = len(albums)
	}
	// v1 getAlbumList 返回 <albumList>，v2 getAlbumList2 返回 <albumList2>，节点名必须与操作名一致
	name := "albumList"
	if v2 {
		name = "albumList2"
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(name, node(name, albums[offset:end]...)))
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
	albumParts := append([]sxNode{attr("id", id), attr("name", title), attr("artist", artist), attr("artistId", artistIDByName(artist)), attr("songCount", strconv.Itoa(len(arr)))}, arr...)
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
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(key, node(key, songs...)))
}
func (a *subsonicAPI) lyrics(r *http.Request, byID bool) sxNode {
	q := r.URL.Query()
	artist := strings.TrimSpace(q.Get("artist"))
	title := strings.TrimSpace(q.Get("title"))
	var t *model.Track
	if byID {
		t = a.track(q.Get("id"))
	} else {
		// 部分客户端（如音流旧版）会在 artist/title 之外附带 id，优先用 id 精确定位
		if id := q.Get("id"); id != "" {
			t = a.track(id)
		}
		if t == nil && artist == "" && title == "" {
			a.mu.Lock()
			ls := a.lastGetSong
			if ls == "" {
				ls = a.lastStreamed
			}
			a.mu.Unlock()
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
	// 旧版 getLyrics 端点：同时返回 lyrics（纯文本）与 lyricsList（结构化），
	// 兼容只解析其中一种的客户端；文本采用 Navidrome 风格（去时间戳纯文本）。
	// 旧版 getLyrics 端点：同时返回 lyrics（原始文本）与 lyricsList（结构化）。
	// 原始文本保留 LRC 时间戳（Airsonic/Subsonic 对 LRC 的惯例，音流旧版依赖时间戳渲染时间轴歌词）；
	// 对纯文本歌词（无时间戳）则原样返回。
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
func (a *subsonicAPI) playlistNode(p subsonicPlaylist, includeSongs bool) sxNode {
	parts := []sxNode{attr("id", p.ID), attr("name", p.Name), attr("owner", a.cfg.GetDefault("admin_user", "admin")), attr("public", "false"), attr("songCount", strconv.Itoa(len(p.TrackIDs))), attr("created", p.Created), attr("changed", p.Changed)}
	if includeSongs {
		for _, id := range p.TrackIDs {
			if t := a.track(id); t != nil {
				parts = append(parts, a.songNode(t))
			}
		}
	}
	return node("playlist", parts...)
}
func (a *subsonicAPI) playlists() sxNode {
	var items []sxNode
	for _, p := range a.loadPlaylists() {
		items = append(items, a.playlistNode(p, false))
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("playlists", node("playlists", items...)))
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
	for _, songID := range q["songId"] {
		if a.track(songID) != nil {
			ps[found].TrackIDs = append(ps[found].TrackIDs, songID)
		}
	}
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
		if q.Get("name") != "" {
			ps[i].Name = q.Get("name")
		}
		for _, sid := range q["songIdToAdd"] {
			if a.track(sid) != nil {
				ps[i].TrackIDs = append(ps[i].TrackIDs, sid)
			}
		}
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
}
func (a *subsonicAPI) star(r *http.Request, add bool) sxNode {
	q := r.URL.Query()
	ids := q["id"]
	stars := a.loadStarred()
	set := map[string]bool{}
	for _, id := range stars {
		set[id] = true
	}
	for _, id := range ids {
		if a.track(id) == nil {
			continue
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
// getUser 返回单一内置用户（管理员），字段对齐 Subsonic API 的 user DTO。
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

// changePassword：单用户本地库不暴露管理员改密接口，返回 ok 避免客户端报错。
func (a *subsonicAPI) changePassword(r *http.Request) sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
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
	if a.track(id) == nil {
		return apiError(70, "Song not found")
	}
	pos, _ := strconv.ParseInt(q.Get("position"), 10, 64)
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
	bs := a.loadBookmarks()
	out := bs[:0]
	for _, bm := range bs {
		if bm.EntryID != id {
			out = append(out, bm)
		}
	}
	a.saveBookmarks(out)
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
}

// ---- 分享（本地单用户，不生成外部分享链接，返回合法空结构） ----

func (a *subsonicAPI) shares() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("shares", emptyArrayNode("shares")))
}
func (a *subsonicAPI) createShare(r *http.Request) sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("shares", emptyArrayNode("shares")))
}
func (a *subsonicAPI) deleteShare(r *http.Request) sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion))
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
	valid := entries[:0]
	for _, id := range entries {
		if a.track(id) != nil {
			valid = append(valid, id)
		}
	}
	pos, _ := strconv.Atoi(q.Get("position"))
	pq := subsonicPlayQueue{
		Current:  q.Get("current"),
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

// jukeboxControl：本地无硬件点唱机，status 返回合法空状态，其余动作返回 ok。
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
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("jukeboxStatus", status))
}

// downloads：本地下载目录概念不适用，返回合法空下载列表。
func (a *subsonicAPI) downloads() sxNode {
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("downloads", emptyArrayNode("downloads")))
}
func (a *subsonicAPI) starred(v2 bool) sxNode {
	var songs []sxNode
	for _, id := range a.loadStarred() {
		if t := a.track(id); t != nil {
			songs = append(songs, a.songNode(t))
		}
	}
	name := "starred"
	if v2 {
		name = "starred2"
	}
	return node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child(name, node(name, songs...)))
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

func (a *subsonicAPI) stream(w http.ResponseWriter, r *http.Request) {
	t := a.track(r.URL.Query().Get("id"))
	if t == nil {
		http.Error(w, "song not found", http.StatusNotFound)
		return
	}
	a.mu.Lock()
	a.lastStreamed = t.ID
	a.mu.Unlock()
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
func urlEscape(s string) string { return url.PathEscape(s) }
func (a *subsonicAPI) cover(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	t := a.track(id)
	if t == nil {
		for _, x := range a.all() {
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
