package main

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cstore "lnh-music-tag/internal/store"
)

const gatewayUserID = "lnh-admin"

type mediaGateway struct {
	library *store
	cfg     *cstore.Config
	kind    string
	mu      sync.RWMutex
	tokens  map[string]time.Time
	plays   map[string]playState
	guard   *loginGuard // 流媒体密码认证限速（防暴力破解）；nil 时不启用（测试构造）
}
type playState struct {
	UserID   string
	ItemID   string
	Position int64
	Started  time.Time
	Updated  time.Time
}

func mediaGatewayHandler(s *store, cfg *cstore.Config, kind string) http.Handler {
	g := &mediaGateway{library: s, cfg: cfg, kind: kind, tokens: map[string]time.Time{}, plays: map[string]playState{}, guard: newLoginGuard()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg != nil && cfg.GetDefault("streaming_gateway_enabled", "true") == "false" {
			http.NotFound(w, r)
			return
		}
		g.serve(w, r, kind)
	})
}
func (g *mediaGateway) serve(w http.ResponseWriter, r *http.Request, kind string) {
	metricGatewayReq.Add(1)
	log.Printf("MEDIA-GATEWAY rid=%s kind=%s method=%s path=%s", requestID(r), kind, r.Method, r.URL.Path)
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
	if g.guard != nil && !g.guard.allow(r) {
		g.json(w, http.StatusTooManyRequests, map[string]any{"Message": "认证失败次数过多，请 5 分钟后再试"})
		return
	}
	_, pass := g.userPass(r)
	wantUser := g.cfg.GetDefault("admin_user", "admin")
	streamPass := strings.TrimSpace(g.cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS")))
	if streamPass == "" {
		g.json(w, http.StatusUnauthorized, map[string]any{"Message": "流媒体密码未配置（请先在插件设置填写 stream_pass）"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(streamPass)) != 1 {
		if g.guard != nil {
			g.guard.fail(r)
		}
		g.json(w, http.StatusUnauthorized, map[string]any{"Message": "Invalid username or password"})
		return
	}
	if g.guard != nil {
		g.guard.success(r)
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
