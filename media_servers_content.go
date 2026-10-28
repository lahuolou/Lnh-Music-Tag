package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"lnh-music-tag/internal/model"
)

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
	start, limit := parseStartLimit(r, []string{"StartIndex", "startIndex"}, []string{"Limit", "limit"})
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
