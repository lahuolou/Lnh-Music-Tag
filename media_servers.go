package main

// Jellyfin/Emby-compatible read-only media API. The adapters intentionally
// share the same in-memory library while keeping each server's URL namespace
// and response shapes separate.
import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

const gatewayUserID = "lnh-admin"

type mediaGateway struct {
	library *store
	cfg     *cstore.Config
	kind    string
	mu      sync.RWMutex
	tokens  map[string]time.Time
	plays   map[string]playState
}
type playState struct {
	UserID   string
	ItemID   string
	Position int64
	Started  time.Time
	Updated  time.Time
}

func mediaGatewayHandler(s *store, cfg *cstore.Config, kind string) http.Handler {
	g := &mediaGateway{library: s, cfg: cfg, kind: kind, tokens: map[string]time.Time{}, plays: map[string]playState{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		g.serve(w, r, kind)
	})
}
func (g *mediaGateway) serve(w http.ResponseWriter, r *http.Request, kind string) {
	log.Printf("MEDIA-GATEWAY kind=%s method=%s path=%s", kind, r.Method, r.URL.Path)
	prefix := "/" + kind
	p := strings.TrimPrefix(r.URL.Path, prefix)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "System/Info/Public"
	}
	p = strings.TrimSuffix(p, "/")
	// 兼容客户端重复前缀（如 /emby/emby/Users/... 或 /jellyfin/jellyfin/...）
	for strings.HasPrefix(p, kind+"/") {
		p = strings.TrimPrefix(p, kind+"/")
	}
	// Emby/Jellyfin 客户端路径大小写不敏感，统一小写匹配
	p = strings.ToLower(p)
	// 主取词：客户端直接从 MediaStreams 字幕流(Codec=text)的 Extradata 读完整 LRC。
	// 这里仅兜底处理个别客户端回退请求的 /Items/{id}/Subtitles/{idx}/Stream(.js)。
	if strings.Contains(p, "/subtitles/") {
		rest := p
		if j := strings.Index(rest, "items/"); j >= 0 {
			rest = rest[j+len("items/"):]
		}
		parts := strings.Split(rest, "/")
		if len(parts) >= 4 && parts[1] == "subtitles" {
			g.subtitleStream(w, r, parts[0], parts[2])
			return
		}
	}
	if kind == "emby" {
		g.serveEmby(w, r, p)
		return
	}
	g.serveJellyfin(w, r, p)
}
func (g *mediaGateway) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (g *mediaGateway) ok(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }
func (g *mediaGateway) userPass(r *http.Request) (string, string) {
	var x struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
		Password string `json:"Password"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&x)
		if x.Pw == "" {
			x.Pw = x.Password
		}
	}
	// 部分客户端以 query 方式提交认证参数。分别补齐缺失字段，兼容
	// username 在 JSON body、password 在 query 的混合请求。
	q := r.URL.Query()
	if x.Username == "" {
		x.Username = q.Get("username")
	}
	if x.Pw == "" {
		x.Pw = q.Get("pw")
	}
	if x.Pw == "" {
		x.Pw = q.Get("password")
	}
	return x.Username, x.Pw
}

// token base 仅取流媒体密码 stream_pass（唯一凭证）；未配置则为空，token 校验自然失败。
func (g *mediaGateway) tokenBase() string {
	return strings.TrimSpace(g.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
}

func (g *mediaGateway) tokenFor(user string) string {
	h := md5.Sum([]byte(g.tokenBase() + "|" + user + "|lnh-token"))
	return hex.EncodeToString(h[:])
}

// authenticate 仅以流媒体密码 stream_pass 作为唯一凭证（单用户自托管）：
// 不校验管理员密码、不接受任意空/残留密码。密码即凭证，用户名仅用于派生稳定 token。
func (g *mediaGateway) authenticate(w http.ResponseWriter, r *http.Request, kind string) {
	_, pass := g.userPass(r)
	wantUser := g.cfg.GetDefault("admin_user", "admin")
	streamPass := strings.TrimSpace(g.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if streamPass == "" {
		g.json(w, http.StatusUnauthorized, map[string]any{"Message": "流媒体密码未配置（请先在插件设置填写 stream_pass）"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(streamPass)) != 1 {
		g.json(w, http.StatusUnauthorized, map[string]any{"Message": "Invalid username or password"})
		return
	}
	// 登录来源不影响 token：token 始终使用稳定配置派生，重启后仍可校验。
	token := g.tokenFor(wantUser)
	g.mu.Lock()
	g.tokens[token] = time.Now().Add(30 * 24 * time.Hour)
	g.mu.Unlock()
	device := r.Header.Get("X-Emby-Device-Id")
	if device == "" {
		device = "lnh-client"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	g.json(w, 200, map[string]any{
		"AccessToken": token,
		"ServerId":    "lnh-music-tag",
		"User": map[string]any{
			"Name":                      wantUser,
			"Id":                        gatewayUserID,
			"ServerId":                  "lnh-music-tag",
			"HasPassword":               true,
			"HasConfiguredPassword":     true,
			"HasConfiguredEasyPassword": false,
			"EnableAutoLogin":           false,
			"LastLoginDate":             now,
			"LastActivityDate":          now,
			"Configuration": map[string]any{
				"AudioLanguagePreference":    "",
				"PlayDefaultAudioTrack":      true,
				"SubtitleLanguagePreference": "",
				"DisplayMissingEpisodes":     false,
				"GroupedFolders":             []string{},
				"SubtitleMode":               "Default",
				"DisplayCollectionsView":     false,
				"EnableLocalPassword":        false,
				"OrderedViews":               []string{},
				"LatestItemsExcludes":        []string{},
				"MyMediaExcludes":            []string{},
				"HidePlayedInLatest":         true,
				"RememberAudioSelections":    true,
				"RememberSubtitleSelections": true,
				"EnableNextEpisodeAutoPlay":  true,
			},
			"Policy": map[string]any{
				"IsAdministrator":                  true,
				"IsHidden":                         false,
				"IsHiddenRemotely":                 false,
				"IsDisabled":                       false,
				"EnableRemoteControlOfOtherUsers":  false,
				"EnableSharedDeviceControl":        true,
				"EnableRemoteAccess":               true,
				"EnableLiveTvManagement":           true,
				"EnableLiveTvAccess":               true,
				"EnableMediaPlayback":              true,
				"EnableAudioPlaybackTranscoding":   true,
				"EnableVideoPlaybackTranscoding":   true,
				"EnablePlaybackRemuxing":           true,
				"ForceRemoteSourceTranscoding":     false,
				"EnableContentDeletion":            false,
				"EnableContentDeletionFromFolders": []string{},
				"EnableContentDownloading":         true,
				"EnableSubtitleDownloading":        false,
				"EnableSubtitleManagement":         false,
				"EnableSyncTranscoding":            true,
				"EnableMediaConversion":            true,
				"EnableAllDevices":                 true,
				"EnabledDevices":                   []string{},
				"EnableAllChannels":                true,
				"EnabledChannels":                  []string{},
				"EnableAllFolders":                 true,
				"EnabledFolders":                   []string{},
				"EnableAllFeatures":                true,
				"EnabledFeatures":                  []string{},
				"EnableChannelAccess":              true,
				"BlockedChannels":                  []string{},
				"EnableAllRecordings":              true,
				"EnabledRecordings":                []string{},
				"EnableAllRemoteControlDevices":    true,
				"BlockedRemoteControlDevices":      []string{},
				"AccessSchedules":                  []string{},
				"BlockUnratedItems":                []string{},
				"BlockedTags":                      []string{},
				"EnableUserPreferenceAccess":       true,
				"RemoteClientBitrateLimit":         0,
			},
		},
		"SessionInfo": map[string]any{
			"Id":                    device + "-sess",
			"ServerId":              "lnh-music-tag",
			"UserId":                gatewayUserID,
			"UserName":              wantUser,
			"Client":                "Lnh-Music-Tag",
			"DeviceId":              device,
			"DeviceName":            "Music Client",
			"ApplicationVersion":    AppVersion(),
			"PlayableMediaTypes":    []string{"Audio"},
			"QueueableMediaTypes":   []string{"Audio"},
			"SupportedCommands":     []string{"Play", "Pause", "Stop", "NextTrack", "PreviousTrack"},
			"LastActivityDate":      now,
			"LastPlaybackCheckIn":   now,
			"SupportsRemoteControl": true,
			"IsActive":              true,
			"SupportsMediaControl":  true,
			"HasCustomDeviceName":   true,
			"PlayState": map[string]any{
				"CanSeek":             true,
				"IsPaused":            false,
				"IsMuted":             false,
				"RepeatMode":          "RepeatNone",
				"AudioStreamIndex":    0,
				"SubtitleStreamIndex": 0,
				"MediaSourceId":       "",
				"PositionTicks":       0,
				"PlaybackRate":        1.0,
				"PlayMethod":          "DirectPlay",
			},
		},
	})
}
func (g *mediaGateway) authorized(r *http.Request) bool {
	token := r.URL.Query().Get("api_key")
	if token == "" {
		token = r.URL.Query().Get("X-Emby-Token")
	}
	if token == "" {
		token = r.Header.Get("X-Emby-Token")
	}
	if token == "" {
		token = r.Header.Get("X-MediaBrowser-Token")
	}
	if token == "" {
		token = parseMediaBrowserToken(r.Header.Get("X-Emby-Authorization"))
	}
	if token == "" {
		token = parseMediaBrowserToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		h := r.Header.Get("Authorization")
		for _, part := range strings.Split(h, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "MediaBrowser ") {
				part = strings.TrimPrefix(part, "MediaBrowser ")
			}
			if strings.HasPrefix(part, "Emby ") {
				part = strings.TrimPrefix(part, "Emby ")
			}
			if strings.HasPrefix(part, "Token=\"") {
				token = strings.Trim(part[len("Token=\""):], "\"")
			}
		}
	}
	g.mu.RLock()
	expiry, ok := g.tokens[token]
	g.mu.RUnlock()
	if ok && time.Now().Before(expiry) {
		return true
	}
	// 兜底：用同一稳定派生规则恢复 token，避免服务重启后令牌失效。
	wantUser := g.cfg.GetDefault("admin_user", "admin")
	want := g.tokenFor(wantUser)
	if subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 {
		g.mu.Lock()
		g.tokens[token] = time.Now().Add(30 * 24 * time.Hour)
		g.mu.Unlock()
		return true
	}
	return false
}
func parseMediaBrowserToken(h string) string {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "MediaBrowser ") {
			part = strings.TrimPrefix(part, "MediaBrowser ")
		}
		if strings.HasPrefix(part, "Emby ") {
			part = strings.TrimPrefix(part, "Emby ")
		}
		if strings.HasPrefix(part, `Token="`) {
			return strings.Trim(strings.TrimPrefix(part, `Token="`), `"`)
		}
		if strings.HasPrefix(part, "Token=") {
			return strings.Trim(strings.TrimPrefix(part, "Token="), `"`)
		}
	}
	return ""
}

func (g *mediaGateway) validItemID(id string) bool {
	if id == "" {
		return false
	}
	if g.library.get(id) != nil {
		return true
	}
	for _, t := range g.library.all() {
		if albumID(t) == id || artistID(t) == id {
			return true
		}
	}
	return false
}

// favorite applies the subset of Jellyfin/Emby favorite operations backed by
// the shared stored favorite list. Invalid item IDs and unsupported methods must
// not silently mutate state.
func (g *mediaGateway) favorite(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		g.json(w, http.StatusMethodNotAllowed, map[string]any{"Message": "Method not allowed"})
		return
	}
	if !g.validItemID(id) {
		g.json(w, http.StatusNotFound, map[string]any{"Message": "Item not found"})
		return
	}
	var ids []string
	if err := json.Unmarshal([]byte(g.cfg.GetDefault("streaming_starred", "[]")), &ids); err != nil || ids == nil {
		ids = []string{}
	}
	if r.Method == http.MethodPost {
		found := false
		for _, existing := range ids {
			if existing == id {
				found = true
				break
			}
		}
		if !found {
			ids = append(ids, id)
		}
	} else {
		out := ids[:0]
		for _, existing := range ids {
			if existing != id {
				out = append(out, existing)
			}
		}
		ids = out
	}
	b, err := json.Marshal(ids)
	if err != nil {
		g.json(w, http.StatusInternalServerError, map[string]any{"Message": "Could not encode favorite state"})
		return
	}
	if err := g.cfg.Set("streaming_starred", string(b)); err != nil {
		g.json(w, http.StatusInternalServerError, map[string]any{"Message": "Could not save favorite state"})
		return
	}
	entityCacheClear() // 收藏变更：实体响应（UserData.IsFavorite）缓存失效
	g.json(w, http.StatusOK, map[string]any{"ItemId": id, "IsFavorite": r.Method == http.MethodPost})
}

// rateItem 处理 Jellyfin/Emby 星级评分（POST /Users/{userId}/Items/{id}/Rating?Rating=0..5），
// 与 Subsonic/Navidrome 共享 streaming_ratings；仅对真实曲目生效，Rating=0 表示清除。
func (g *mediaGateway) rateItem(w http.ResponseWriter, r *http.Request, id string) {
	if g.library.get(id) == nil {
		g.json(w, http.StatusNotFound, map[string]any{"Message": "Item not found"})
		return
	}
	rating, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("Rating")))
	if err != nil || rating < 0 || rating > 5 {
		g.json(w, http.StatusBadRequest, map[string]any{"Message": "Invalid Rating (0-5)"})
		return
	}
	m := map[string]int{}
	_ = json.Unmarshal([]byte(g.cfg.GetDefault("streaming_ratings", "{}")), &m)
	if rating == 0 {
		delete(m, id)
	} else {
		m[id] = rating
	}
	if b, err := json.Marshal(m); err == nil {
		_ = g.cfg.Set("streaming_ratings", string(b))
	}
	entityCacheClear() // 评分变更：实体响应（UserData.Rating）缓存失效
	g.json(w, http.StatusOK, map[string]any{"ItemId": id, "Rating": rating})
}

func (g *mediaGateway) serveJellyfin(w http.ResponseWriter, r *http.Request, p string) {
	if p == "system/info/public" || p == "system/info" {
		g.json(w, 200, map[string]any{"LocalAddress": "http://192.168.100.29:10248", "ServerName": "Lnh-Music-Tag", "Version": "10.9.11", "ProductName": "Jellyfin Server", "OperatingSystem": "Linux", "Id": "d4b3f6e2-8a1c-4f5e-9d2b-3c7a1e9f6b21", "StartupWizardCompleted": true})
		return
	}
	if p == "branding/configuration" {
		g.json(w, 200, map[string]any{})
		return
	}
	// 兼容标准登录路径：/Users/AuthenticateByName、/Users/Public/AuthenticateByName、/Users/{userId}/AuthenticateByName
	if strings.HasSuffix(p, "authenticatebyname") {
		g.authenticate(w, r, "jellyfin")
		return
	}
	if !g.authorized(r) {
		w.Header().Set("WWW-Authenticate", `MediaBrowser`)
		g.json(w, 401, map[string]any{"Message": "Unauthorized"})
		return
	}
	// 下载（兼容 /Users/{uid} 前缀）：附件直传原始文件，优先于对象路由。
	if strings.HasSuffix(p, "/download") && (strings.Contains(p, "/items/") || strings.Contains(p, "/audio/")) {
		var id string
		if i := strings.Index(p, "/items/"); i >= 0 {
			id = p[i+len("/items/"):]
		} else if i := strings.Index(p, "/audio/"); i >= 0 {
			id = p[i+len("/audio/"):]
		}
		id = strings.TrimSuffix(id, "/download")
		g.directDownload(w, r, id)
		return
	}
	// 歌单/艺术家对象的详情、曲目列表与加删歌（兼容 users 前缀），优先于旧的吞吃式分支。
	if g.matchObjectRoute(w, r, p) {
		return
	}
	if p == "users/me" || p == "users/"+gatewayUserID {
		g.json(w, 200, map[string]any{"Name": g.cfg.GetDefault("admin_user", "admin"), "Id": gatewayUserID, "HasPassword": true, "Policy": map[string]any{"IsAdministrator": true}})
		return
	}
	if strings.HasPrefix(p, "users/") && strings.HasSuffix(p, "/views") || p == "users/"+gatewayUserID+"/views" {
		// 返回音乐媒体库视图（id 用 gatewayUserID，Items?ParentId=lnh-admin 即全库）
		g.json(w, 200, map[string]any{"Items": []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "Type": "CollectionFolder", "CollectionType": "music", "IsFolder": true, "ServerId": "lnh-music-tag"}}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}
	if p == "library/virtualfolders" {
		// Emby 资料库列表（数组格式，含 ItemId 供 Items?ParentId=ItemId 查询）
		g.json(w, 200, []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "ItemId": gatewayUserID, "ServerId": "lnh-music-tag", "CollectionType": "music", "Type": "CollectionFolder", "Locations": []string{"/music"}}})
		return
	}
	if p == "library/mediafolders" {
		// 标准 Jellyfin /Emby /Library/MediaFolders：客户端初始化拉取可访问媒体库，返回单个 Music 库。
		g.json(w, 200, map[string]any{"Items": []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "CollectionType": "music", "Type": "CollectionFolder", "IsFolder": true, "ServerId": "lnh-music-tag", "Locations": []string{"/music"}}}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}
	if p == "sessions/playing" || p == "sessions/playing/progress" {
		g.playback(r)
		g.ok(w)
		return
	}
	if p == "sessions/playing/stopped" {
		g.stopPlayback(r)
		g.ok(w)
		return
	}
	// 收藏：POST/DELETE /Users/{userId}/FavoriteItems/{id}
	if strings.Contains(p, "/favoriteitems/") {
		id := p[strings.LastIndex(p, "/favoriteitems/")+len("/favoriteitems/"):]
		g.favorite(w, r, id)
		return
	}
	// 星级评分：POST /Users/{userId}/Items/{id}/Rating?Rating=0..5（与 Subsonic 共享 streaming_ratings）
	if strings.Contains(p, "/items/") && strings.HasSuffix(p, "/rating") {
		id := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		g.rateItem(w, r, strings.TrimSuffix(id, "/rating"))
		return
	}
	// 建歌单：POST /Users/{uid}/Playlists 或 POST /Playlists（body {"Name":..,"Ids":[..]}）
	if r.Method == http.MethodPost && (p == "playlists" || strings.HasSuffix(p, "/playlists")) {
		g.createPlaylistJf(w, r)
		return
	}
	// 加/删歌：POST/DELETE /Playlists/{id}/Items?Ids=.. / EntryIds=..
	if strings.HasPrefix(p, "playlists/") && strings.HasSuffix(p, "/items") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "playlists/"), "/items")
		g.mutatePlaylistItems(w, r, id)
		return
	}
	// 标准 Jellyfin 歌词接口：/Users/{userId}/Items/{id}/Lyrics、/Items/{id}/Lyrics（10.9+）、/Audio/{id}/Lyrics（10.8+）
	if strings.Contains(p, "/items/") && strings.HasSuffix(p, "/lyrics") {
		id := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		g.jellyfinLyrics(w, r, strings.TrimSuffix(id, "/lyrics"))
		return
	}
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/lyrics") {
		g.jellyfinLyrics(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/lyrics"))
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/lyrics") {
		g.jellyfinLyrics(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/lyrics"))
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/file") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/file")
		g.stream(w, r, id)
		return
	}
	if p == "artists" || p == "artists/albumartists" {
		g.json(w, 200, g.artistItems(r))
		return
	}
	if p == "albums" {
		// 标准 Jellyfin/Emby 顶层 /Albums：复用 itemQuery 的 MusicAlbum 分组，补充 IncludeItemTypes。
		r2 := r.Clone(r.Context())
		u := *r.URL
		q := u.Query()
		q.Set("IncludeItemTypes", "MusicAlbum")
		u.RawQuery = q.Encode()
		r2.URL = &u
		g.json(w, 200, g.itemQuery(r2))
		return
	}
	// GET /Playlists 返回歌单列表；POST /Playlists 建歌单落到写动作路由，不可在此吞掉。
	if p == "playlists" && r.Method != http.MethodPost {
		g.json(w, 200, g.playlistItems(r))
		return
	}
	if p == "genres" {
		g.json(w, 200, g.genreItems(r))
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/items") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/items")
		r2 := r.Clone(r.Context())
		u := *r.URL
		q := u.Query()
		q.Set("ParentId", id)
		u.RawQuery = q.Encode()
		r2.URL = &u
		g.json(w, 200, g.itemQuery(r2))
		return
	}
	// 兼容 Items/Artists/Audio 三类对象的 Images/Primary 封面路径（须在 audio 流与 users/items 拦截之前）
	if strings.Contains(p, "/images/") {
		rest := p
		for _, key := range []string{"items/", "artists/", "audio/", "playlists/"} {
			if i := strings.Index(rest, key); i >= 0 {
				rest = rest[i+len(key):]
				break
			}
		}
		id := strings.Split(rest, "/")[0]
		g.cover(w, r, id)
		return
	}
	if strings.HasPrefix(p, "users/") && strings.Contains(p, "/items/") {
		last := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		// 评分写动作须先于通用曲目读取拦截，否则 POST 会被 item 查询吞掉。
		if strings.HasSuffix(last, "/rating") {
			g.rateItem(w, r, strings.TrimSuffix(last, "/rating"))
			return
		}
		if t := g.library.get(last); t != nil {
			g.writeItem(w, t)
			return
		}
		if strings.HasPrefix(last, "ar-") {
			if it := g.artistItem(last); it != nil {
				g.json(w, 200, it)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(last, "al-") {
			if it := g.albumItem(last); it != nil {
				g.json(w, 200, it)
			} else {
				http.NotFound(w, r)
			}
			return
		}
	}
	if strings.HasSuffix(p, "/items/latest") {
		// 标准 Jellyfin：/Users/{userId}/Items/Latest 返回 ItemDTO 裸数组（JellyBox 等客户端要求）
		g.json(w, 200, g.latestItems(r))
		return
	}
	if p == "items" || strings.HasPrefix(p, "users/") && strings.Contains(p, "/items") {
		g.json(w, 200, g.itemQuery(r))
		return
	}
	if strings.HasPrefix(p, "artists/") && strings.HasSuffix(p, "/albums") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "artists/"), "/albums")
		g.json(w, 200, g.albumItems(r, id))
		return
	}
	if strings.HasPrefix(p, "artists/") && strings.HasSuffix(p, "/similar") {
		// 标准 Jellyfin /Artists/{id}/Similar：返回相似艺术家列表；无相似数据时返回空分页
		g.json(w, 200, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	if strings.HasPrefix(p, "items/") {
		rest := strings.TrimPrefix(p, "items/")
		parts := strings.Split(rest, "/")
		id := parts[0]
		// Emby/Jellyfin 下载：/Items/{id}/Download（路由已小写归一为 download），附件直传原始文件。
		if len(parts) >= 2 && parts[1] == "download" {
			g.directDownload(w, r, id)
			return
		}
		if len(parts) >= 3 && parts[1] == "images" {
			g.cover(w, r, id)
			return
		}
		if len(parts) == 1 {
			t := g.library.get(id)
			if t == nil {
				if pl, idx := g.playlistByID(id); idx >= 0 {
					g.json(w, 200, g.playlistDTO(pl))
					return
				}
				http.NotFound(w, r)
				return
			}
			g.writeItem(w, t)
			return
		}
	}
	if strings.HasPrefix(p, "audio/") && (strings.HasSuffix(p, "/download") || strings.HasSuffix(p, "/Download")) {
		id := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/download"), "/Download")
		g.directDownload(w, r, id)
		return
	}
	if strings.HasPrefix(p, "items/") && (strings.HasSuffix(p, "/download") || strings.HasSuffix(p, "/Download")) {
		id := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/download"), "/Download")
		g.directDownload(w, r, id)
		return
	}
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/stream") {
		g.stream(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/stream"))
		return
	}
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/universal") {
		g.stream(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/universal"))
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/playbackinfo") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/playbackinfo")
		t := g.library.get(id)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		g.json(w, 200, map[string]any{"MediaSources": g.mediaSources(t), "PlaySessionId": "lnh-session", "ErrorCode": nil})
		return
	}
	if strings.HasPrefix(p, "items/") {
		id := strings.TrimPrefix(p, "items/")
		if t := g.library.get(id); t != nil {
			g.writeItem(w, t)
			return
		}
	}
	if p == "library/refresh" && r.Method == http.MethodPost {
		g.json(w, http.StatusNotImplemented, map[string]any{"Message": "Library refresh through Jellyfin API is not supported; run a scan in Lnh-Music-Tag"})
		return
	}
	http.NotFound(w, r)
}

// trackLyricsText 返回曲目的歌词文本：优先嵌入 LYRICS 标签，其次同名 .lrc 文件。
// 复用全局短 TTL 歌词缓存（cachedLyrics），避免播放会话内反复读盘。
func (g *mediaGateway) trackLyricsText(t *model.Track) string {
	return cachedLyrics(t)
}

// plainLyrics 返回剥离 LRC 时间戳与元信息行后的纯文本歌词，供 Emby/JF item 对象内嵌 Lyrics 字段（Emby 客户端不另发 /Lyrics，直接读该字段）。
func (g *mediaGateway) plainLyrics(t *model.Track) string {
	text := g.trackLyricsText(t)
	if text == "" {
		return ""
	}
	out := make([]string, 0)
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if lrcMetaLineRe.MatchString(ln) {
			continue
		}
		if m := lrcLineRe.FindStringSubmatch(ln); m != nil {
			if txt := strings.TrimSpace(m[3]); txt != "" {
				out = append(out, txt)
			}
		} else if strings.TrimSpace(ln) != "" {
			out = append(out, strings.TrimSpace(ln))
		}
	}
	return strings.Join(out, "\n")
}

// lrcMetaLineRe 匹配 LRC 元信息标签行：[ti:]/[ar:]/[al:]/[by:]/[offset:]/[length:] 等。
var lrcMetaLineRe = regexp.MustCompile(`^\[[a-zA-Z]+:[^]]*\]\s*$`)

// jellyfinLyrics 标准 Jellyfin 10.9+ 歌词响应（LyricDto）：
// {"Lyrics":[{"Start":ticks,"Text":...}],"Metadata":{...}}
// Jellyfin 时间单位是 100ns ticks（1 毫秒 = 10000 ticks）。
// 无歌词返回 404（Jellyfin 规范）。
func (g *mediaGateway) jellyfinLyrics(w http.ResponseWriter, r *http.Request, id string) {
	t := g.library.get(id)
	text := g.trackLyricsText(t)
	if text == "" {
		http.NotFound(w, r)
		return
	}
	type lyricLine struct {
		Start int64  `json:"Start"`
		Text  string `json:"Text"`
	}
	type lyricMeta struct {
		Artist string `json:"Artist,omitempty"`
		Album  string `json:"Album,omitempty"`
		Title  string `json:"Title,omitempty"`
		Author string `json:"Author,omitempty"`
	}
	lines := []lyricLine{}
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
				lines = append(lines, lyricLine{Start: int64(start) * 10000, Text: txt})
			}
		} else if !started && strings.TrimSpace(ln) != "" {
			lines = append(lines, lyricLine{Start: 0, Text: strings.TrimSpace(ln)})
		}
	}
	if len(lines) == 0 {
		if txt := strings.TrimSpace(text); txt != "" {
			lines = append(lines, lyricLine{Start: 0, Text: txt})
		}
	}
	var meta *lyricMeta
	if t != nil {
		meta = &lyricMeta{Artist: songArtist(t), Album: songAlbum(t), Title: songTitle(t)}
	}
	// 兼容多种客户端取词约定：lrclib 风格 lrclist[{time(秒),lyric}]、纯文本 lyrics/value/text。
	lrcList := make([]map[string]any, 0, len(lines))
	for _, ln := range lines {
		sec := float64(ln.Start) / 1e7
		lrcList = append(lrcList, map[string]any{
			"time":  sec,
			"start": sec,
			"lyric": ln.Text,
			"text":  ln.Text,
		})
	}
	plain := g.plainLyrics(t)
	// Emby 原生结构：TrackEvents[{Text,StartPositionTicks,EndPositionTicks}]（100ns ticks），
	// 与 Jellyfin 的 Lyrics[{Start,Text}] 并存以兼容两类客户端。
	totalTicks := int64(0)
	if t != nil && t.Duration > 0 {
		totalTicks = int64(t.Duration * 10000000)
	}
	trackEvents := make([]map[string]any, 0, len(lines))
	for i, ln := range lines {
		s := ln.Start
		e := int64(0)
		if i+1 < len(lines) {
			e = lines[i+1].Start
		} else if totalTicks > 0 {
			e = totalTicks
		} else {
			e = s + 30000000
		}
		if e <= s {
			e = s + 30000000
		}
		trackEvents = append(trackEvents, map[string]any{
			"Text":               ln.Text,
			"StartPositionTicks": s,
			"EndPositionTicks":   e,
		})
	}
	// Emby 原生 /Audio/{id}/Lyrics 部分客户端接受纯文本 LRC；按 query/Accept 降级返回。
	if qv(r, "format", "") == "lrc" || qv(r, "format", "") == "plain" || strings.HasPrefix(r.Header.Get("Accept"), "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte(text))
		return
	}
	dataObj := map[string]any{
		"Lyrics":       lines,
		"TrackEvents":  trackEvents,
		"lrclist":      lrcList,
		"lyrics":       plain,
		"value":        plain,
		"text":         plain,
		"plainLyrics":  plain,
		"syncedLyrics": text,
		"plainlyrics":  plain,
		"syncedlyrics": text,
		"Title":        "Lyrics",
		"Extradata":    text,
	}
	body, _ := json.Marshal(map[string]any{
		"Lyrics":       lines,
		"TrackEvents":  trackEvents,
		"Metadata":     meta,
		"lrclist":      lrcList,
		"lyrics":       plain,
		"value":        plain,
		"text":         plain,
		"plainLyrics":  plain,
		"syncedLyrics": text,
		"plainlyrics":  plain,
		"syncedlyrics": text,
		"Title":        "Lyrics",
		"Extradata":    text,
		"data":         dataObj,
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	w.Write(body)
}

// subtitleStream 兜底处理个别客户端回退请求的
// /Items/{id}/Subtitles/{index}/Stream(.js)。客户端 Accept=application/json，
// 返回 Emby 原生 TrackEvents JSON（与 /Lyrics 同源，含 TrackEvents/Lyrics/lrclist）；
// Accept:text/plain 时由 jellyfinLyrics 自动降级为纯 LRC。
func (g *mediaGateway) subtitleStream(w http.ResponseWriter, r *http.Request, id, index string) {
	g.jellyfinLyrics(w, r, id)
}

func (g *mediaGateway) serveEmby(w http.ResponseWriter, r *http.Request, p string) {
	if p == "system/info/public" || p == "system/info" {
		g.json(w, 200, map[string]any{"ServerName": "Lnh-Music-Tag", "Version": "4.8.0.0", "ProductName": "Lnh-Music-Tag", "Id": "lnh-music-tag", "OperatingSystem": "Linux", "StartupWizardCompleted": true, "SystemArchitecture": "x64", "LocalNetworkAddresses": []string{}, "CanSelfRestart": false, "CanLaunchWebBrowser": false})
		return
	}
	if p == "system/ping" {
		g.ok(w)
		return
	}
	// 兼容标准登录路径：/Users/AuthenticateByName、/Users/Public/AuthenticateByName、/Users/{userId}/AuthenticateByName
	if strings.HasSuffix(p, "authenticatebyname") {
		g.authenticate(w, r, "emby")
		return
	}
	if !g.authorized(r) {
		g.json(w, 401, map[string]any{"Message": "Unauthorized"})
		return
	}
	// 下载（兼容 /Users/{uid} 前缀）：附件直传原始文件，优先于对象路由。
	if strings.HasSuffix(p, "/download") && (strings.Contains(p, "/items/") || strings.Contains(p, "/audio/")) {
		var id string
		if i := strings.Index(p, "/items/"); i >= 0 {
			id = p[i+len("/items/"):]
		} else if i := strings.Index(p, "/audio/"); i >= 0 {
			id = p[i+len("/audio/"):]
		}
		id = strings.TrimSuffix(id, "/download")
		g.directDownload(w, r, id)
		return
	}
	// 歌单/艺术家对象的详情、曲目列表与加删歌（兼容 users 前缀），优先于旧的吞吃式分支。
	if g.matchObjectRoute(w, r, p) {
		return
	}
	// 歌词端点统一返回结构化 JSON（客户端解析 data.lrclist / data.lyrics）；
	// jellyfinLyrics 内部已按 Accept:text/plain 或 format=lrc/plain 降级为纯文本。
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/lyrics") {
		g.jellyfinLyrics(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/lyrics"))
		return
	}
	if (strings.HasPrefix(p, "items/") || strings.Contains(p, "/items/")) && strings.HasSuffix(p, "/lyrics") {
		rest := p
		if i := strings.LastIndex(rest, "items/"); i >= 0 {
			rest = rest[i+len("items/"):]
		}
		g.jellyfinLyrics(w, r, strings.TrimSuffix(rest, "/lyrics"))
		return
	}
	if p == "users/me" || p == "users/"+gatewayUserID {
		g.json(w, 200, map[string]any{"Name": g.cfg.GetDefault("admin_user", "admin"), "Id": gatewayUserID, "HasPassword": true})
		return
	}
	if strings.HasPrefix(p, "users/") && strings.HasSuffix(p, "/views") || p == "users/"+gatewayUserID+"/views" {
		// 返回音乐媒体库视图（id 用 gatewayUserID，Items?ParentId=lnh-admin 即全库）
		g.json(w, 200, map[string]any{"Items": []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "Type": "CollectionFolder", "CollectionType": "music", "IsFolder": true, "ServerId": "lnh-music-tag"}}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}
	if p == "library/virtualfolders" {
		// Emby 资料库列表（数组格式，含 ItemId 供 Items?ParentId=ItemId 查询）
		g.json(w, 200, []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "ItemId": gatewayUserID, "ServerId": "lnh-music-tag", "CollectionType": "music", "Type": "CollectionFolder", "Locations": []string{"/music"}}})
		return
	}
	if p == "library/mediafolders" {
		// 标准 Jellyfin /Emby /Library/MediaFolders：客户端初始化拉取可访问媒体库，返回单个 Music 库。
		g.json(w, 200, map[string]any{"Items": []any{map[string]any{"Name": "Music", "Id": gatewayUserID, "CollectionType": "music", "Type": "CollectionFolder", "IsFolder": true, "ServerId": "lnh-music-tag", "Locations": []string{"/music"}}}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/file") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/file")
		g.stream(w, r, id)
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/items") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/items")
		r2 := r.Clone(r.Context())
		u := *r.URL
		q := u.Query()
		q.Set("ParentId", id)
		u.RawQuery = q.Encode()
		r2.URL = &u
		g.json(w, 200, g.itemQuery(r2))
		return
	}
	// 兼容 Items/Artists/Audio 三类对象的 Images/Primary 封面路径（须在 audio 流与 users/items 拦截之前）
	if strings.Contains(p, "/images/") {
		rest := p
		for _, key := range []string{"items/", "artists/", "audio/", "playlists/"} {
			if i := strings.Index(rest, key); i >= 0 {
				rest = rest[i+len(key):]
				break
			}
		}
		id := strings.Split(rest, "/")[0]
		g.cover(w, r, id)
		return
	}
	if strings.HasPrefix(p, "users/") && strings.Contains(p, "/items/") {
		last := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		// 评分写动作须先于通用曲目读取拦截，否则 POST 会被 item 查询吞掉。
		if strings.HasSuffix(last, "/rating") {
			g.rateItem(w, r, strings.TrimSuffix(last, "/rating"))
			return
		}
		if t := g.library.get(last); t != nil {
			g.writeItem(w, t)
			return
		}
		if strings.HasPrefix(last, "ar-") {
			if it := g.artistItem(last); it != nil {
				g.json(w, 200, it)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(last, "al-") {
			if it := g.albumItem(last); it != nil {
				g.json(w, 200, it)
			} else {
				http.NotFound(w, r)
			}
			return
		}
	}
	if strings.HasSuffix(p, "/items/latest") {
		// 标准 Jellyfin：/Users/{userId}/Items/Latest 返回 ItemDTO 裸数组（JellyBox 等客户端要求）
		g.json(w, 200, g.latestItems(r))
		return
	}
	if p == "items" || strings.HasPrefix(p, "users/") && strings.Contains(p, "/items") {
		g.json(w, 200, g.itemQuery(r))
		return
	}
	if p == "artists" || p == "artists/albumartists" {
		g.json(w, 200, g.artistItems(r))
		return
	}
	if p == "albums" {
		// 标准 Jellyfin/Emby 顶层 /Albums：复用 itemQuery 的 MusicAlbum 分组，补充 IncludeItemTypes。
		r2 := r.Clone(r.Context())
		u := *r.URL
		q := u.Query()
		q.Set("IncludeItemTypes", "MusicAlbum")
		u.RawQuery = q.Encode()
		r2.URL = &u
		g.json(w, 200, g.itemQuery(r2))
		return
	}
	// GET /Playlists 返回歌单列表；POST /Playlists 建歌单落到写动作路由，不可在此吞掉。
	if p == "playlists" && r.Method != http.MethodPost {
		g.json(w, 200, g.playlistItems(r))
		return
	}
	if p == "genres" {
		g.json(w, 200, g.genreItems(r))
		return
	}
	if strings.HasPrefix(p, "artists/") && strings.HasSuffix(p, "/albums") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "artists/"), "/albums")
		g.json(w, 200, g.albumItems(r, id))
		return
	}
	if strings.HasPrefix(p, "items/") && strings.HasSuffix(p, "/playbackinfo") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "items/"), "/playbackinfo")
		t := g.library.get(id)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		g.json(w, 200, map[string]any{"MediaSources": g.mediaSources(t), "PlaySessionId": "lnh-session", "ErrorCode": nil})
		return
	}
	// 兼容 Items/Artists/Audio 三类对象的 Images/Primary 封面路径
	if strings.Contains(p, "/images/") {
		rest := p
		for _, key := range []string{"items/", "artists/", "audio/", "playlists/"} {
			if i := strings.Index(rest, key); i >= 0 {
				rest = rest[i+len(key):]
				break
			}
		}
		id := strings.Split(rest, "/")[0]
		g.cover(w, r, id)
		return
	}
	if strings.HasPrefix(p, "items/") || strings.Contains(p, "/items/") {
		raw := p
		if i := strings.Index(raw, "items/"); i >= 0 {
			raw = raw[i:]
		}
		parts := strings.Split(strings.TrimPrefix(raw, "items/"), "/")
		// Emby 下载：/Items/{id}/Download（路由已小写），附件直传原始文件。
		if len(parts) >= 2 && parts[1] == "download" {
			g.directDownload(w, r, parts[0])
			return
		}
		id := parts[0]
		if t := g.library.get(id); t != nil {
			g.writeItem(w, t)
			return
		}
		if pl, idx := g.playlistByID(id); idx >= 0 {
			g.json(w, 200, g.playlistDTO(pl))
			return
		}
	}
	if strings.HasPrefix(p, "audio/") {
		rest := strings.TrimPrefix(p, "audio/")
		segs := strings.Split(rest, "/")
		id := segs[0]
		t := g.library.get(id)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		second := ""
		if len(segs) >= 2 {
			second = segs[1]
		}
		// Emby 下载：/Audio/{id}/download 附件直传原始文件。
		if second == "download" {
			g.directDownload(w, r, id)
			return
		}
		// Emby HLS：/Audio/{id}/main.m3u8 返回单分片播放列表，hls0.ts 走 mpegts 实时转码
		if second == "main.m3u8" {
			g.embyHLSPlaylist(w, r, t)
			return
		}
		if strings.HasSuffix(second, ".ts") {
			g.embyHLSSegment(w, r, t)
			return
		}
		g.stream(w, r, id)
		return
	}
	if p == "sessions/playing" || p == "sessions/playing/progress" {
		g.playback(r)
		g.ok(w)
		return
	}
	if p == "sessions/playing/stopped" {
		g.stopPlayback(r)
		g.ok(w)
		return
	}
	// 收藏：POST/DELETE /Users/{userId}/FavoriteItems/{id}
	if strings.Contains(p, "/favoriteitems/") {
		id := p[strings.LastIndex(p, "/favoriteitems/")+len("/favoriteitems/"):]
		g.favorite(w, r, id)
		return
	}
	// 星级评分：POST /Users/{userId}/Items/{id}/Rating?Rating=0..5（与 Subsonic 共享 streaming_ratings）
	if strings.Contains(p, "/items/") && strings.HasSuffix(p, "/rating") {
		id := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		g.rateItem(w, r, strings.TrimSuffix(id, "/rating"))
		return
	}
	// 建歌单：POST /Users/{uid}/Playlists 或 POST /Playlists（body {"Name":..,"Ids":[..]}）
	if r.Method == http.MethodPost && (p == "playlists" || strings.HasSuffix(p, "/playlists")) {
		g.createPlaylistJf(w, r)
		return
	}
	// 加/删歌：POST/DELETE /Playlists/{id}/Items?Ids=.. / EntryIds=..
	if strings.HasPrefix(p, "playlists/") && strings.HasSuffix(p, "/items") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "playlists/"), "/items")
		g.mutatePlaylistItems(w, r, id)
		return
	}
	http.NotFound(w, r)
}
func (g *mediaGateway) mediaSources(t *model.Track) []any {
	var streamURL, transURL string
	if g.kind == "emby" {
		streamURL = "/Items/" + t.ID + "/File"
		transURL = "/Items/" + t.ID + "/File?format=mp3&maxBitrate=128"
	} else {
		streamURL = "/Audio/" + t.ID + "/stream"
		transURL = "/Audio/" + t.ID + "/universal?format=mp3&maxBitrate=128"
	}
	return []any{map[string]any{
		"Id":                    t.ID,
		"Path":                  "",
		"Protocol":              "File",
		"Type":                  "Default",
		"CanDownload":           true,
		"Container":             strings.TrimPrefix(strings.ToLower(t.Ext), "."),
		"Size":                  t.Size,
		"IsRemote":              false,
		"SupportsDirectPlay":    false,
		"SupportsDirectStream":  true,
		"SupportsTranscoding":   true,
		"DirectStreamUrl":       streamURL,
		"TranscodingUrl":        transURL,
		"TranscodingContainer":  "mp3",
		"TranscodingProtocol":   "http",
		"IsInfiniteStream":      false,
		"RequiresOpening":       false,
		"RequiresClosing":       false,
		"RequiresLooping":       false,
		"SupportsProbing":       true,
		"ReadAtNativeFramerate": false,
		"IgnoreDts":             false,
		"IgnoreIndex":           false,
		"GenPtsInput":           false,
		"RunTimeTicks":          int64(t.Duration * 10000000),
		"Bitrate":               t.Bitrate * 1000,
		"MediaStreams":          g.mediaStreams(t),
	}}
}

func (g *mediaGateway) mediaStreams(t *model.Track) []any {
	ms := []any{map[string]any{
		"Type":                   "Audio",
		"Codec":                  strings.TrimPrefix(strings.ToLower(t.Ext), "."),
		"BitRate":                t.Bitrate * 1000,
		"SampleRate":             t.SampleRate,
		"Channels":               t.Channels,
		"IsInterlaced":           false,
		"IsDefault":              true,
		"IsForced":               false,
		"Index":                  0,
		"IsExternal":             false,
		"IsTextSubtitleStream":   false,
		"SupportsExternalStream": false,
	}}
	// Emby：把内嵌歌词作为文本字幕流挂出。参考音流实现：客户端直接从
	// MediaStreams 字幕流的 Extradata 读取完整 LRC（Codec=text），无需再请求 Stream.js。
	if g.kind == "emby" {
		if lrc := g.trackLyricsText(t); lrc != "" {
			ms = append(ms, map[string]any{
				"Index":                  1,
				"Type":                   "Subtitle",
				"Codec":                  "text",
				"Title":                  "Lyrics",
				"DisplayTitle":           "Und (TEXT)",
				"Extradata":              lrc,
				"Language":               "",
				"IsDefault":              true,
				"IsForced":               false,
				"IsExternal":             false,
				"IsTextSubtitleStream":   true,
				"SupportsExternalStream": true,
				"Protocol":               "File",
				"SubtitleLocationType":   "InternalStream",
				"Size":                   int64(len(lrc)),
			})
		}
	}
	return ms
}
func (g *mediaGateway) item(t *model.Track) map[string]any {
	year := 0
	if y := tag(t, "YEAR", "DATE"); len(y) >= 4 {
		year, _ = strconv.Atoi(y[:4])
	}
	imageTags := map[string]string{}
	if t.HasCover {
		imageTags["Primary"] = "primary"
	}
	genres := []string{}
	if gv := tag(t, "GENRE"); gv != "" {
		genres = []string{gv}
	}
	// 歌词纯文本只解析一次供 Lyrics 字段使用；Overview/Comment 不再重复塞歌词
	//（原实现把歌词全文写进 3 个字段，列表响应体积为歌词的 3 倍，且 Overview/Comment
	// 语义错误——Jellyfin/Emby 中它们是"简介/注释"。歌词仍经 Lyrics 字段与
	// Emby 字幕流（Extradata）两条标准路径输出，客户端兼容性不受影响）。
	plain := g.plainLyrics(t)
	m := map[string]any{"Name": songTitle(t), "ServerId": "lnh-music-tag", "Id": t.ID, "Type": "Audio", "MediaType": "Audio", "IsFolder": false, "Album": songAlbum(t), "AlbumId": albumID(t), "AlbumArtist": songArtist(t), "Artists": []string{songArtist(t)}, "ArtistItems": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "AlbumArtists": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "RunTimeTicks": int64(t.Duration * 10000000), "ProductionYear": year, "IndexNumber": intValue(tag(t, "TRACKNUMBER", "TRACK")), "ParentIndexNumber": intValue(tag(t, "DISCNUMBER", "DISC")), "Container": strings.TrimPrefix(strings.ToLower(t.Ext), "."), "Path": t.Path, "Size": t.Size, "Bitrate": t.Bitrate * 1000, "HasPrimaryImage": t.HasCover, "CanDownload": true, "ImageTags": imageTags, "Lyrics": plain, "Overview": "", "Comment": "", "Genres": genres, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": g.isStarred(t.ID), "Rating": ratingFor(g.cfg, t.ID), "Played": false}, "MediaSources": g.mediaSources(t)}
	if g.kind == "emby" && plain != "" {
		m["SubtitleStreamIndex"] = 1
	}
	return m
}

// writeItem 输出单曲 item JSON（带实体缓存）：同一客户端会话中重复请求
// /Items/{id} 等单曲详情时复用序列化字节，跳过重复组装+序列化。
// 曲库/收藏/评分变更时由 entityCacheClear 统一失效，无陈旧风险。
func (g *mediaGateway) writeItem(w http.ResponseWriter, t *model.Track) {
	if t == nil {
		g.json(w, http.StatusNotFound, map[string]any{"Message": "Item not found"})
		return
	}
	key := "media:" + g.kind + ":item:" + t.ID
	if b, ok := entityCacheGet(key); ok {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}
	b, _ := json.Marshal(g.item(t))
	entityCachePut(key, b)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}
func intValue(s string) int { n, _ := strconv.Atoi(firstNumber(s)); return n }

// qv 兼容大小写参数读取（部分客户端使用全小写 query 参数）
func qv(r *http.Request, names ...string) string {
	for _, n := range names {
		if v := r.URL.Query().Get(n); v != "" {
			return v
		}
	}
	return ""
}
func (g *mediaGateway) itemQuery(r *http.Request) map[string]any {
	tracks := g.library.all()
	q := strings.ToLower(strings.TrimSpace(qv(r, "SearchTerm", "searchTerm")))
	parent := qv(r, "ParentId", "parentId")
	types := strings.ToLower(qv(r, "IncludeItemTypes", "includeItemTypes"))
	artistIds := strings.Trim(qv(r, "ArtistIds", "artistIds"), ",")
	artistFilter := make(map[string]struct{})
	for _, id := range strings.Split(artistIds, ",") {
		if id = strings.TrimSpace(id); id != "" {
			artistFilter[id] = struct{}{}
		}
	}
	filters := qv(r, "Filters", "filters")
	if strings.EqualFold(qv(r, "isFavorite", "IsFavorite"), "true") && !strings.Contains(filters, "IsFavorite") {
		filters += ",IsFavorite"
	}
	start, _ := strconv.Atoi(qv(r, "StartIndex", "startIndex"))
	limit, _ := strconv.Atoi(qv(r, "Limit", "limit"))
	if start < 0 {
		start = 0
	}
	starred := map[string]bool{}
	if strings.Contains(filters, "IsFavorite") {
		var ids []string
		_ = json.Unmarshal([]byte(g.cfg.GetDefault("streaming_starred", "[]")), &ids)
		for _, id := range ids {
			starred[id] = true
		}
	}
	// ParentId 指向歌单（pl- 前缀或裸歌单 id）时，仅返回该歌单内曲目
	playlistSet := map[string]bool{}
	if parent != "" {
		if pl, idx := g.playlistByID(parent); idx >= 0 {
			for _, sid := range pl.TrackIDs {
				playlistSet[sid] = true
			}
		}
	}
	var items []any
	for _, t := range tracks {
		if len(playlistSet) > 0 && !playlistSet[t.ID] {
			continue
		}
		if strings.Contains(filters, "IsFavorite") && !starred[t.ID] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(songTitle(t)+" "+songArtist(t)+" "+songAlbum(t)), q) {
			continue
		}
		if len(artistFilter) > 0 {
			if _, ok := artistFilter[artistID(t)]; !ok {
				continue
			}
		}
		if len(playlistSet) == 0 && parent != "" && parent != gatewayUserID && parent != albumID(t) && parent != artistID(t) {
			// ParentId 可能是歌曲 id：客户端用它查询该歌曲所属专辑的全部歌曲
			parentTrack := g.library.get(parent)
			if parentTrack == nil || albumID(parentTrack) != albumID(t) {
				continue
			}
		}
		if strings.Contains(types, "artist") {
			continue
		}
		if strings.Contains(types, "album") {
			continue
		}
		items = append(items, g.item(t))
	}
	if strings.Contains(types, "artist") {
		return g.artistItems(r)
	}
	if strings.Contains(types, "playlist") {
		return g.playlistItems(r)
	}
	if strings.Contains(types, "album") {
		art := strings.Trim(qv(r, "AlbumArtistIds", "albumArtistIds")+","+qv(r, "ContributingArtistIds", "contributingArtistIds")+","+qv(r, "ArtistIds", "artistIds"), ",")
		return g.albumItems(r, art)
	}
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

type mediaAlbumGroup struct {
	id       string
	first    *model.Track
	count    int
	hasCover bool
	year     int
}

// albumGroups performs one pass over the scanned library. The previous listing
// code repeatedly called library.all() and resorted the full track set for each
// album, making album/latest queries approach O(albums * tracks * log(tracks)).
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
	limit, _ := strconv.Atoi(qv(r, "Limit", "limit"))
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
	start, _ := strconv.Atoi(qv(r, "StartIndex", "startIndex"))
	limit, _ := strconv.Atoi(qv(r, "Limit", "limit"))
	if start < 0 {
		start = 0
	}
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
