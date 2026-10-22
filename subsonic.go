package main

// Subsonic/OpenSubsonic compatibility API for third-party music clients.
// This first implementation intentionally focuses on common library and playback endpoints.

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
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
	library *store
	cfg     *cstore.Config
	// mu 保护播放状态（nowPlaying / playCounts）：scrobble 提交与
	// getNowPlaying/getTopSongs 读取共享。流媒体/歌曲读取不走此锁。
	mu         sync.Mutex
	nowPlaying map[string]time.Time
	playCounts map[string]int
	// recMu 独立保护"最近访问"记录（lastStreamed / lastGetSong）：stream 与
	// getSong 每次请求都要写，拆开后不再与播放计数/nowPlaying 争用同一把锁，
	// 降低高并发流媒体下的首字节延迟。
	recMu        sync.Mutex
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
		log.Printf("SUBSONIC method=%s operation=%s", r.Method, op)
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
			// OpenSubsonic：向客户端通告支持的扩展。部分客户端据此
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
			result = a.setRating(r)
		case "getAlbum":
			result = a.album(r.URL.Query().Get("id"))
		case "getSong":
			result = a.song(r.URL.Query().Get("id"))
			if t := a.track(r.URL.Query().Get("id")); t != nil {
				a.recMu.Lock()
				a.lastGetSong = t.ID
				a.recMu.Unlock()
			}
		case "search3":
			result = a.search(r, "searchResult3")
		case "search2":
			result = a.search(r, "searchResult2")
		case "search":
			result = a.search(r, "searchResult")
		case "getStatistics":
			result = a.statistics()
		case "getScanStatus":
			result = node("subsonic-response", attr("status", "ok"), attr("version", subsonicVersion), child("scanStatus", node("scanStatus", attr("scanning", "false"), attr("count", strconv.Itoa(len(a.all()))))))
		case "startScan":
			result = apiError(0, "startScan is not supported through the streaming gateway")
		case "stream", "download":
			a.stream(w, r, op)
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
			result = apiError(0, "getAvatar is not supported by this server")
		case "uploadAvatar", "deleteAvatar":
			result = apiError(0, op+" is not supported by this server")
		case "createUser", "updateUser", "deleteUser":
			result = apiError(0, op+" is not supported by the single-user server")
		default:
			result = apiError(0, "Unsupported operation: "+op)
		}
		// x-total-count：对齐 Navidrome Subsonic API 行为。音流（SoundFlow/Musiver）等客户端
		// 依赖该响应头获取音乐库统计（专辑/歌手/歌单总数），缺失时概览页显示"未知"。
		if tot := listTotal(result); tot != "" {
			w.Header().Set("x-total-count", tot)
		}
		a.respond(w, r, result)
	})
}
// listTotal 从列表响应（albumList/albumList2/artists/playlists 等）的 total 属性提取总数，
// 用于 x-total-count 响应头；无 total 属性的响应（如 randomSongs）返回空串、不设头。
func listTotal(n sxNode) string {
	for _, c := range n.Children {
		if v, ok := c.Attr["total"]; ok {
			return v
		}
	}
	return ""
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

// authorized 仅以流媒体密码 stream_pass 为唯一凭证（单用户自托管）：
// 明文 p 精确匹配 stream_pass；token 认证为 MD5(stream_pass + salt)。
// 不校验管理员密码，不接受空参/残留密码握手。
func (a *subsonicAPI) authorized(r *http.Request) bool {
	q := r.URL.Query()
	streamPass := strings.TrimSpace(a.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if streamPass == "" {
		return false
	}
	// 明文密码（兼容客户端 enc:hex 编码）。
	supplied := q.Get("p")
	pass := supplied
	if strings.HasPrefix(pass, "enc:") {
		b, err := hex.DecodeString(strings.TrimPrefix(pass, "enc:"))
		if err != nil {
			return false
		}
		pass = string(b)
	}
	if pass != "" {
		return subtle.ConstantTimeCompare([]byte(pass), []byte(streamPass)) == 1
	}
	// Subsonic token 认证：MD5(stream_pass + salt)。
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
		// 部分 Navidrome 模式客户端依赖 serverVersion 判定服务器版本
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
			// 部分严格解析器对字符串数字会解析失败
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
		return child == "song" || child == "child" || child == "entry"
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
	if a.isStarred(t.ID) {
		attrs = append(attrs, attr("starred", time.Now().UTC().Format(time.RFC3339)))
	}
	if rv, ok := a.loadRatings()[t.ID]; ok && rv > 0 {
		attrs = append(attrs, attr("userRating", strconv.Itoa(rv)))
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
