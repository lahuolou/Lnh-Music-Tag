package main

import (
	"bytes"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
		case "getSongs":
			result = a.songs(r)
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
	// Navidrome 风格 apiKey 认证（箭头音乐/Stream Music 等客户端使用）：
	// apiKey 必须等于 Subsonic token（MD5(stream_pass + "lnh-navidrome")）。
	if apiKey := q.Get("apiKey"); apiKey != "" {
		sum := md5.Sum([]byte(streamPass + "lnh-navidrome"))
		want := hex.EncodeToString(sum[:])
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(apiKey)), []byte(want)) == 1
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
	// getSong 单曲详情缓存：同一首歌在播放队列/详情页被重复请求时复用序列化
	// 字节（30s TTL，实体缓存模块统一）。单用户服务器（admin），starred/
	// userRating 变更时 entityCacheClear 已全清，无陈旧风险。
	asJSON := strings.EqualFold(r.URL.Query().Get("f"), "json")
	cacheKey := ""
	if op := r.URL.Query().Get("op"); op == "getSong" {
		cacheKey = "subsonic:song:" + r.URL.Query().Get("id")
		if asJSON {
			cacheKey += ":json"
		}
	}
	// 命中缓存直接返回（序列化前查询，省掉组装+序列化）
	if cacheKey != "" {
		if b, ok := entityCacheGet(cacheKey); ok {
			if asJSON {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			}
			w.Write(b)
			return
		}
	}
	var buf bytes.Buffer
	if asJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(&buf).Encode(map[string]any{"subsonic-response": sxJSON(n)})
	} else {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		buf.WriteString(xml.Header)
		enc := xml.NewEncoder(&buf)
		_ = enc.Encode(n)
		_ = enc.Flush()
	}
	if cacheKey != "" {
		entityCachePut(cacheKey, buf.Bytes())
	}
	_, _ = w.Write(buf.Bytes())
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
func songGenre(t *model.Track) string {
	if v := tag(t, "GENRE", "genre"); v != "" {
		return v
	}
	return "Unknown"
}
func songYear(t *model.Track) int {
	for _, k := range []string{"YEAR", "DATE", "ORIGINALDATE"} {
		if v := tag(t, k); v != "" {
			if y, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return y
			}
		}
	}
	return 0
}

// songs 实现 Subsonic getSongs（歌曲列表；NASMusicTV 等客户端依赖它统计"歌曲"总数）。
// 支持规范 type：alphabetical / byArtist / byAlbum / byGenre / byYear / byFrequent /
// byNewest / byRandom；兼容客户端扩展值（如 alphabeticalByName，未知 type 兜底按标题）。
// size/offset 分页（size<=0 不限制，上限由 parseStartLimit 钳制到 maxPageSize）。
// 响应结构 <subsonic-response><songs><song .../></songs></subsonic-response>。
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
