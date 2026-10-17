package main

// Jellyfin/Emby-compatible read-only media API. The adapters intentionally
// share the same in-memory library while keeping each server's URL namespace
// and response shapes separate.
import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	"lnh-music-tag/internal/taglibx"
)

const gatewayUserID = "lnh-admin"

type mediaGateway struct {
	library *store
	cfg     *cstore.Config
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
	g := &mediaGateway{library: s, cfg: cfg, tokens: map[string]time.Time{}, plays: map[string]playState{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		g.serve(w, r, kind)
	})
}
func (g *mediaGateway) serve(w http.ResponseWriter, r *http.Request, kind string) {
	fmt.Fprintf(os.Stderr, "stream:%s %s %s from %s\n", kind, r.Method, r.URL.RequestURI(), r.RemoteAddr)
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
	if x.Username == "" {
		// 部分客户端（如箭头音乐）以 GET query 方式提交认证参数
		q := r.URL.Query()
		x.Username = q.Get("username")
		if x.Pw == "" {
			x.Pw = q.Get("pw")
		}
		if x.Pw == "" {
			x.Pw = q.Get("password")
		}
	}
	return x.Username, x.Pw
}
func (g *mediaGateway) authenticate(w http.ResponseWriter, r *http.Request, kind string) {
	user, pass := g.userPass(r)
	wantUser := g.cfg.GetDefault("admin_user", "admin")
	stored, _ := g.cfg.Get("admin_pass")
	ok := subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1 && verifyPassword(stored, pass)
	if !ok {
		// 兼容流媒体密码（与 Subsonic 网关一致；token 认证类客户端要求）
		streamPass := strings.TrimSpace(g.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
		ok = subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) == 1 && streamPass != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(streamPass)) == 1
	}
	if !ok {
		g.json(w, 401, map[string]any{"Message": "Invalid username or password"})
		return
	}
	// 确定性 token：基于认证密码派生，服务重启后已登录客户端仍可继续使用
	h := md5.Sum([]byte(pass + "|" + wantUser + "|lnh-token"))
	token := hex.EncodeToString(h[:])
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
				"AudioLanguagePreference":      "",
				"PlayDefaultAudioTrack":        true,
				"SubtitleLanguagePreference":   "",
				"DisplayMissingEpisodes":       false,
				"GroupedFolders":               []string{},
				"SubtitleMode":                 "Default",
				"DisplayCollectionsView":       false,
				"EnableLocalPassword":          false,
				"OrderedViews":                 []string{},
				"LatestItemsExcludes":          []string{},
				"MyMediaExcludes":              []string{},
				"HidePlayedInLatest":           true,
				"RememberAudioSelections":      true,
				"RememberSubtitleSelections":   true,
				"EnableNextEpisodeAutoPlay":    true,
			},
			"Policy": map[string]any{
				"IsAdministrator":               true,
				"IsHidden":                      false,
				"IsHiddenRemotely":              false,
				"IsDisabled":                    false,
				"EnableRemoteControlOfOtherUsers": false,
				"EnableSharedDeviceControl":     true,
				"EnableRemoteAccess":            true,
				"EnableLiveTvManagement":        true,
				"EnableLiveTvAccess":            true,
				"EnableMediaPlayback":           true,
				"EnableAudioPlaybackTranscoding": true,
				"EnableVideoPlaybackTranscoding": true,
				"EnablePlaybackRemuxing":        true,
				"ForceRemoteSourceTranscoding":  false,
				"EnableContentDeletion":         false,
				"EnableContentDeletionFromFolders": []string{},
				"EnableContentDownloading":      true,
				"EnableSubtitleDownloading":     false,
				"EnableSubtitleManagement":      false,
				"EnableSyncTranscoding":         true,
				"EnableMediaConversion":         true,
				"EnableAllDevices":              true,
				"EnabledDevices":                []string{},
				"EnableAllChannels":             true,
				"EnabledChannels":               []string{},
				"EnableAllFolders":              true,
				"EnabledFolders":                []string{},
				"EnableAllFeatures":             true,
				"EnabledFeatures":               []string{},
				"EnableChannelAccess":           true,
				"BlockedChannels":               []string{},
				"EnableAllRecordings":           true,
				"EnabledRecordings":             []string{},
				"EnableAllRemoteControlDevices": true,
				"BlockedRemoteControlDevices":   []string{},
				"AccessSchedules":               []string{},
				"BlockUnratedItems":             []string{},
				"BlockedTags":                   []string{},
				"EnableUserPreferenceAccess":    true,
				"RemoteClientBitrateLimit":      0,
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
				"CanSeek":           true,
				"IsPaused":          false,
				"IsMuted":           false,
				"RepeatMode":        "RepeatNone",
				"AudioStreamIndex":  0,
				"SubtitleStreamIndex": 0,
				"MediaSourceId":     "",
				"PositionTicks":     0,
				"PlaybackRate":      1.0,
				"PlayMethod":        "DirectPlay",
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
	// 兜底：确定性 token（服务重启后已登录客户端仍可用）
	wantUser := g.cfg.GetDefault("admin_user", "admin")
	streamPass := strings.TrimSpace(g.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if streamPass != "" {
		h := md5.Sum([]byte(streamPass + "|" + wantUser + "|lnh-token"))
		if subtle.ConstantTimeCompare([]byte(token), []byte(hex.EncodeToString(h[:]))) == 1 {
			g.mu.Lock()
			g.tokens[token] = time.Now().Add(30 * 24 * time.Hour)
			g.mu.Unlock()
			return true
		}
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

func (g *mediaGateway) serveJellyfin(w http.ResponseWriter, r *http.Request, p string) {
	if p == "system/info/public" || p == "system/info" {
		g.json(w, 200, map[string]any{"ServerName": "Lnh-Music-Tag", "Version": "10.8.13", "ProductName": "Lnh-Music-Tag", "Id": "lnh-music-tag", "OperatingSystem": "Linux", "CanSelfRestart": false, "CanLaunchWebBrowser": false, "StartupWizardCompleted": true, "SystemArchitecture": "x64", "LocalNetworkAddresses": []string{}})
		return
	}
	if p == "branding/configuration" {
		g.json(w, 200, map[string]any{})
		return
	}
	if p == "users/authenticatebyname" {
		g.authenticate(w, r, "jellyfin")
		return
	}
	if !g.authorized(r) {
		w.Header().Set("WWW-Authenticate", `MediaBrowser`)
		g.json(w, 401, map[string]any{"Message": "Unauthorized"})
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
		var ids []string
		_ = json.Unmarshal([]byte(g.cfg.GetDefault("streaming_starred", "[]")), &ids)
		if r.Method == http.MethodPost {
			ids = append(ids, id)
		} else {
			out := ids[:0]
			for _, x := range ids {
				if x != id {
					out = append(out, x)
				}
			}
			ids = out
		}
		if b, err := json.Marshal(ids); err == nil {
			_ = g.cfg.Set("streaming_starred", string(b))
		}
		g.json(w, 200, map[string]any{"ItemId": id, "IsFavorite": r.Method == http.MethodPost})
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
	if strings.HasPrefix(p, "users/") && strings.Contains(p, "/items/") {
		last := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		if t := g.library.get(last); t != nil {
			g.json(w, 200, g.item(t))
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
		if len(parts) >= 3 && parts[1] == "images" {
			g.cover(w, r, id)
			return
		}
		if len(parts) == 1 {
			t := g.library.get(id)
			if t == nil {
				http.NotFound(w, r)
				return
			}
			g.json(w, 200, g.item(t))
			return
		}
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
			g.json(w, 200, g.item(t))
			return
		}
	}
	if p == "library/refresh" && r.Method == http.MethodPost {
		g.ok(w)
		return
	}
	http.NotFound(w, r)
}

// trackLyricsText 返回曲目的歌词文本：优先嵌入 LYRICS 标签，其次同名 .lrc 文件。
func (g *mediaGateway) trackLyricsText(t *model.Track) string {
	if t == nil {
		return ""
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
	return strings.TrimSpace(text)
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
	log.Printf("jellyfinLyrics id=%s url=%s headers=%v", id, r.URL.String(), r.Header)
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
	body, _ := json.Marshal(map[string]any{"Lyrics": lines, "Metadata": meta})
	mb := 160
	if len(body) < mb {
		mb = len(body)
	}
	log.Printf("jellyfinLyrics RESP %s CT=application/json body[:160]=%q", id, body[:mb])
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	w.Write(body)
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
	if p == "users/authenticatebyname" {
		g.authenticate(w, r, "emby")
		return
	}
	if !g.authorized(r) {
		g.json(w, 401, map[string]any{"Message": "Unauthorized"})
		return
	}
	// 标准歌词接口（与 Jellyfin 共用 LyricDto）：/Audio/{id}/Lyrics、/Items/{id}/Lyrics、/Users/{uid}/Items/{id}/Lyrics
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/lyrics") {
		g.jellyfinLyrics(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/lyrics"))
		return
	}
	if strings.Contains(p, "/items/") && strings.HasSuffix(p, "/lyrics") {
		id := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		g.jellyfinLyrics(w, r, strings.TrimSuffix(id, "/lyrics"))
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
	if strings.HasPrefix(p, "users/") && strings.Contains(p, "/items/") {
		last := p[strings.LastIndex(p, "/items/")+len("/items/"):]
		if t := g.library.get(last); t != nil {
			g.json(w, 200, g.item(t))
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
	if strings.HasPrefix(p, "items/") && strings.Contains(p, "/images/") {
		id := strings.TrimPrefix(p, "items/")
		id = strings.Split(id, "/")[0]
		g.cover(w, r, id)
		return
	}
	if strings.HasPrefix(p, "items/") {
		id := strings.TrimPrefix(p, "items/")
		if t := g.library.get(id); t != nil {
			g.json(w, 200, g.item(t))
			return
		}
	}
	if strings.HasPrefix(p, "audio/") && strings.HasSuffix(p, "/main.m3u8") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "audio/"), "/main.m3u8")
		t := g.library.get(id)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		dur := 30
		if int(t.Duration) > 0 {
			dur = int(t.Duration)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:%d,\nAudio/%s/stream?static=true&id=%s\n#EXT-X-ENDLIST\n", dur, dur, id, id)
		return
	}
	if strings.HasPrefix(p, "audio/") {
		id := strings.TrimPrefix(p, "audio/")
		id = strings.TrimSuffix(id, "/stream")
		id = strings.TrimSuffix(id, "/universal")
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
		var ids []string
		_ = json.Unmarshal([]byte(g.cfg.GetDefault("streaming_starred", "[]")), &ids)
		if r.Method == http.MethodPost {
			ids = append(ids, id)
		} else {
			out := ids[:0]
			for _, x := range ids {
				if x != id {
					out = append(out, x)
				}
			}
			ids = out
		}
		if b, err := json.Marshal(ids); err == nil {
			_ = g.cfg.Set("streaming_starred", string(b))
		}
		g.json(w, 200, map[string]any{"ItemId": id, "IsFavorite": r.Method == http.MethodPost})
		return
	}
	http.NotFound(w, r)
}
func (g *mediaGateway) mediaSources(t *model.Track) []any {
	return []any{map[string]any{
		"Id":                     t.ID,
		"Path":                   "",
		"Protocol":               "File",
		"Type":                   "Default",
		"Container":              strings.TrimPrefix(strings.ToLower(t.Ext), "."),
		"Size":                   t.Size,
		"IsRemote":               false,
		"SupportsDirectPlay":     false,
		"SupportsDirectStream":   true,
		"SupportsTranscoding":    false,
		"IsInfiniteStream":       false,
		"RequiresOpening":        false,
		"RequiresClosing":        false,
		"RequiresLooping":        false,
		"SupportsProbing":        true,
		"ReadAtNativeFramerate":  false,
		"IgnoreDts":              false,
		"IgnoreIndex":            false,
		"GenPtsInput":            false,
		"RunTimeTicks":           int64(t.Duration * 10000000),
		"Bitrate":                t.Bitrate * 1000,
		"MediaStreams":           g.mediaStreams(t),
	}}
}

func (g *mediaGateway) mediaStreams(t *model.Track) []any {
	return []any{map[string]any{
		"Type":                     "Audio",
		"Codec":                    strings.TrimPrefix(strings.ToLower(t.Ext), "."),
		"BitRate":                  t.Bitrate * 1000,
		"SampleRate":               t.SampleRate,
		"Channels":                 t.Channels,
		"IsInterlaced":             false,
		"IsDefault":                true,
		"IsForced":                 false,
		"Index":                    0,
		"IsExternal":               false,
		"IsTextSubtitleStream":     false,
		"SupportsExternalStream":   false,
	}}
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
	return map[string]any{"Name": songTitle(t), "ServerId": "lnh-music-tag", "Id": t.ID, "Type": "Audio", "MediaType": "Audio", "IsFolder": false, "Album": songAlbum(t), "AlbumId": albumID(t), "AlbumArtist": songArtist(t), "Artists": []string{songArtist(t)}, "ArtistItems": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "AlbumArtists": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "RunTimeTicks": int64(t.Duration * 10000000), "ProductionYear": year, "IndexNumber": intValue(tag(t, "TRACKNUMBER", "TRACK")), "ParentIndexNumber": intValue(tag(t, "DISCNUMBER", "DISC")), "Container": strings.TrimPrefix(strings.ToLower(t.Ext), "."), "Path": t.Path, "Size": t.Size, "Bitrate": t.Bitrate * 1000, "HasPrimaryImage": t.HasCover, "ImageTags": imageTags, "Genres": genres, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": g.isStarred(t.ID), "Played": false}, "MediaSources": g.mediaSources(t)}
}
func intValue(s string) int { n, _ := strconv.Atoi(firstNumber(s)); return n }
// qv 兼容大小写参数读取（旧版音流用全小写 query 参数）
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
	filters := qv(r, "Filters", "filters")
	if qv(r, "isFavorite") == "true" && !strings.Contains(filters, "IsFavorite") {
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
	var items []any
	for _, t := range tracks {
		if strings.Contains(filters, "IsFavorite") && !starred[t.ID] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(songTitle(t)+" "+songArtist(t)+" "+songAlbum(t)), q) {
			continue
		}
		if artistIds != "" && !strings.Contains(artistIds, artistID(t)) {
			continue
		}
		if parent != "" && parent != gatewayUserID && parent != albumID(t) && parent != artistID(t) {
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
func (g *mediaGateway) latestItems(r *http.Request) []any {
	// 标准 Jellyfin /Users/{userId}/Items/Latest：返回 ItemDTO 裸数组（JellyBox 期望 MusicAlbum 等文件夹类型）
	seen := map[string]bool{}
	var items []any
	for _, t := range g.library.all() {
		id := albumID(t)
		if seen[id] {
			continue
		}
		seen[id] = true
		cover := map[string]string{}
		has := false
		if g.coverForAlbum(id) {
			cover["Primary"] = "primary"
			has = true
		}
		py := 0
		for _, s := range g.library.all() {
			if albumID(s) == id {
				if y := tag(s, "YEAR", "DATE"); len(y) >= 4 {
					py, _ = strconv.Atoi(y[:4])
				}
				if py > 0 {
					break
				}
			}
		}
		items = append(items, map[string]any{"Name": songAlbum(t), "Id": id, "Type": "MusicAlbum", "IsFolder": true, "AlbumArtist": songArtist(t), "AlbumArtists": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ArtistItems": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ChildCount": g.albumCount(id), "ServerId": "lnh-music-tag", "HasPrimaryImage": has, "ImageTags": cover, "ProductionYear": py, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}})
	}
	limit, _ := strconv.Atoi(qv(r, "Limit", "limit"))
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	if items == nil {
		items = []any{}
	}
	return items
}
func (g *mediaGateway) artistItems(r *http.Request) map[string]any {
	seen := map[string]bool{}
	albumsOf := map[string]map[string]bool{}
	songsOf := map[string]int{}
	names := map[string]string{}
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
	}
	var items []any
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("SearchTerm")))
	for id := range seen {
		if q != "" && !strings.Contains(strings.ToLower(names[id]), q) {
			continue
		}
		items = append(items, map[string]any{"Name": names[id], "Id": id, "Type": "MusicArtist", "IsFolder": true, "ServerId": "lnh-music-tag", "AlbumCount": len(albumsOf[id]), "SongCount": songsOf[id], "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}})
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
	for _, t := range g.library.all() {
		if artistID(t) == id {
			name = songArtist(t)
			albums[albumID(t)] = true
			songs++
		}
	}
	if name == "" {
		return nil
	}
	return map[string]any{"Name": name, "Id": id, "Type": "MusicArtist", "IsFolder": true, "ServerId": "lnh-music-tag", "AlbumCount": len(albums), "SongCount": songs, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}}
}
func (g *mediaGateway) albumItem(id string) map[string]any {
	for _, t := range g.library.all() {
		if albumID(t) == id {
			cover := map[string]string{}
			has := false
			if g.coverForAlbum(id) {
				cover["Primary"] = "primary"
				has = true
			}
			return map[string]any{"Name": songAlbum(t), "Id": id, "Type": "MusicAlbum", "IsFolder": true, "AlbumArtist": songArtist(t), "AlbumArtists": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ArtistItems": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ChildCount": g.albumCount(id), "ServerId": "lnh-music-tag", "HasPrimaryImage": has, "ImageTags": cover, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}}
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
func (g *mediaGateway) playlistItems(r *http.Request) map[string]any {
	var p []subsonicPlaylist
	raw := g.cfg.GetDefault("streaming_playlists", "[]")
	if json.Unmarshal([]byte(raw), &p) != nil {
		p = []subsonicPlaylist{}
	}
	var items []any
	for _, pl := range p {
		items = append(items, map[string]any{"Name": pl.Name, "Id": "pl-" + pl.ID, "Type": "Playlist", "IsFolder": true, "ChildCount": len(pl.TrackIDs), "ServerId": "lnh-music-tag", "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}})
	}
	return pageItems(items, r)
}
func (g *mediaGateway) albumItems(r *http.Request, artists string) map[string]any {
	seen := map[string]bool{}
	var items []any
	for _, t := range g.library.all() {
		if artists != "" && !strings.Contains(artists, artistID(t)) {
			continue
		}
		id := albumID(t)
		if seen[id] {
			continue
		}
		seen[id] = true
		cover := map[string]string{}
		has := false
		if g.coverForAlbum(id) {
			cover["Primary"] = "primary"
			has = true
		}
		py := 0
		for _, s := range g.library.all() {
			if albumID(s) == id {
				if y := tag(s, "YEAR", "DATE"); len(y) >= 4 {
					py, _ = strconv.Atoi(y[:4])
				}
				if py > 0 {
					break
				}
			}
		}
		items = append(items, map[string]any{"Name": songAlbum(t), "Id": id, "Type": "MusicAlbum", "IsFolder": true, "AlbumArtist": songArtist(t), "AlbumArtists": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ArtistItems": []any{map[string]any{"Name": songArtist(t), "Id": artistID(t)}}, "ChildCount": g.albumCount(id), "ServerId": "lnh-music-tag", "HasPrimaryImage": has, "ImageTags": cover, "ProductionYear": py, "UserData": map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}})
	}
	return pageItems(items, r)
}
func (g *mediaGateway) albumCount(id string) int {
	n := 0
	for _, t := range g.library.all() {
		if albumID(t) == id {
			n++
		}
	}
	return n
}
func (g *mediaGateway) coverForAlbum(id string) bool {
	for _, x := range g.library.all() {
		if albumID(x) == id && x.HasCover {
			return true
		}
	}
	return false
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
	f, err := os.Open(t.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "media unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", contentType(t.Ext))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(t.FileName)))
	http.ServeContent(w, r, t.FileName, st.ModTime(), f)
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
		for _, x := range g.library.all() {
			if artistID(x) == id && x.HasCover {
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
