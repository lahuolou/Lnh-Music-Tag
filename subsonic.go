// Subsonic API 协议输出（内置插件 protocol.subsonic）。
//
// 在现有 Web 服务上挂载 /rest/* 端点，让音流 / StreamMusic / Symfonium /
// Ultrasonic / DSub 等支持 Subsonic 协议的空壳播放器直接连接本音乐库：
// 浏览艺术家/专辑/曲目、搜索、在线播放（stream，支持 Range）、下载、
// 封面与歌词。响应支持 XML 与 JSON（format 参数，缺省 XML，符合 Subsonic
// 规范），认证支持 token(盐) / 明文密码 / HTTP Basic 三种形式。
package main

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lnh-musictag/internal/model"
	cstore "lnh-musictag/internal/store"
	"lnh-musictag/internal/taglibx"
)

const (
	ssAPIVersion = "1.16.1" // 对外声明的 Subsonic API 版本（实现兼容子集）
	ssServerType = "lnh-musictag"
)

// ---- 认证 ----

// subsonicAuth 校验 Subsonic 凭据：
//   - 插件页配置了专用密码（subsonic_pass，明文存储）时，支持
//     token=md5(password+salt) / 明文 p= / HTTP Basic 三种形式；
//   - 未配置时回退到后台管理员密码（仅支持明文 p= 与 HTTP Basic，
//     因 admin_pass 是不可逆加盐哈希，无法计算 token）。
func subsonicAuth(cfg *cstore.Config, r *http.Request) bool {
	wantUser := cfg.GetDefault("admin_user", "admin")
	u := r.FormValue("u")
	if u == "" {
		if user, _, ok := r.BasicAuth(); ok {
			u = user
		}
	}
	if subtle.ConstantTimeCompare([]byte(u), []byte(wantUser)) != 1 {
		return false
	}
	if pass := cfg.GetDefault("subsonic_pass", ""); pass != "" {
		return subsonicVerifyPassword(r, pass)
	}
	stored, _ := cfg.Get("admin_pass")
	pw, ok := subsonicPlainPassword(r)
	if !ok {
		return false
	}
	return verifyPassword(stored, pw)
}

// subsonicVerifyPassword 针对给定明文密码处理三种认证形式。
func subsonicVerifyPassword(r *http.Request, plain string) bool {
	if t, s := r.FormValue("t"), r.FormValue("s"); t != "" && s != "" {
		h := md5.Sum([]byte(plain + s))
		got := strings.ToLower(hex.EncodeToString(h[:]))
		want := strings.ToLower(strings.TrimSpace(t))
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
	pw, ok := subsonicPlainPassword(r)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pw), []byte(plain)) == 1
}

// subsonicPlainPassword 提取明文密码：p= 参数（支持 enc: 十六进制前缀）或 HTTP Basic。
func subsonicPlainPassword(r *http.Request) (string, bool) {
	if p := r.FormValue("p"); p != "" {
		if strings.HasPrefix(p, "enc:") {
			if raw, err := hex.DecodeString(strings.TrimPrefix(p, "enc:")); err == nil {
				return string(raw), true
			}
		}
		return p, true
	}
	if _, pw, ok := r.BasicAuth(); ok {
		return pw, true
	}
	return "", false
}

// ---- 响应树：XML / JSON 双格式 ----

// ssElem 是 Subsonic 响应元素树：属性（attrs）+ 文本（text）+ 子元素（children）。
// XML 输出时 attrs 全部作为属性、text 作为元素文本；JSON 输出时 attrs 作为
// 对象字段、text 映射为 "value" 字段、children 按同名合并为数组。
type ssElem struct {
	name     string
	attrs    map[string]any
	text     string
	children []*ssElem
}

func newSS(name string) *ssElem { return &ssElem{name: name, attrs: map[string]any{}} }

func ssText(name, text string) *ssElem {
	e := newSS(name)
	e.text = text
	return e
}

// ssListElems 是 Subsonic JSON 中"列表型"元素：即使只有一个也序列化为数组
// 字段（如 musicFolder、index、artist、album、song、child 等）。
var ssListElems = map[string]bool{
	"musicFolder": true, "folder": true, "index": true, "artist": true,
	"album": true, "song": true, "child": true, "genre": true,
	"playlist": true, "error": true, "avatar": true, "entry": true,
	"similarSong": true, "topSong": true, "nowPlayingEntry": true,
}

// toJSON 递归转 JSON（对齐 OpenSubsonic JSON 结构）：
// 列表型或同名多元素 → 字段为数组；其余单元素 → 字段为对象。
// root 为 true 表示根响应层（subsonic-response 的直接子元素恒为单对象）。
func (e *ssElem) toJSON(root bool) any {
	if e == nil {
		return nil
	}
	obj := map[string]any{}
	for k, v := range e.attrs {
		obj[k] = v
	}
	if e.text != "" {
		obj["value"] = e.text
	}
	groups := map[string][]any{}
	order := []string{}
	for _, c := range e.children {
		if _, ok := groups[c.name]; !ok {
			order = append(order, c.name)
		}
		groups[c.name] = append(groups[c.name], c.toJSON(false))
	}
	for _, n := range order {
		g := groups[n]
		if !root && (ssListElems[n] || len(g) > 1) {
			obj[n] = g
		} else {
			obj[n] = g[0]
		}
	}
	return obj
}

// toXML 递归转 XML 字符串。
func (e *ssElem) toXML() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(e.name)
	for k, v := range e.attrs {
		b.WriteString(" ")
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(xmlEsc(fmtValue(v)))
		b.WriteString(`"`)
	}
	if len(e.children) == 0 && e.text == "" {
		b.WriteString("/>")
		return b.String()
	}
	b.WriteString(">")
	if e.text != "" {
		b.WriteString(xmlEsc(e.text))
	}
	for _, c := range e.children {
		b.WriteString(c.toXML())
	}
	b.WriteString("</")
	b.WriteString(e.name)
	b.WriteString(">")
	return b.String()
}

func fmtValue(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return strings.TrimSpace(x.(string))
	}
}

func xmlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ssSend 输出 Subsonic 响应（XML 或 JSON，由 format 参数决定；缺省 XML）。
func ssSend(w http.ResponseWriter, r *http.Request, data *ssElem, errCode int, errMsg string) {
	status := "ok"
	top := newSS("subsonic-response")
	top.attrs["status"] = status
	top.attrs["version"] = ssAPIVersion
	top.attrs["type"] = ssServerType
	top.attrs["serverVersion"] = AppVersion()
	top.attrs["openSubsonic"] = true
	if errCode != 0 {
		top.attrs["status"] = "failed"
		top.children = append(top.children, &ssElem{
			name:  "error",
			attrs: map[string]any{"code": errCode, "message": errMsg},
		})
	} else if data != nil {
		top.children = append(top.children, data)
	}
	format := strings.ToLower(r.FormValue("format"))
	if format == "" {
		format = "xml" // Subsonic 规范缺省 XML；大多数客户端显式传 json
	}
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": top.toJSON(true)})
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>`))
	w.Write([]byte(top.toXML()))
}

// ---- 曲目/艺术家/专辑索引 ----

type ssArtist struct {
	id      string
	name    string
	albums  []*ssAlbum
	coverID string // 任一专辑封面（无则空）
}

type ssAlbum struct {
	id       string
	name     string
	artist   string
	artistID string
	year     int
	genre    string
	coverID  string // 任一曲目封面
	seq      int    // 库内首次出现序（newest/recent 排序依据）
	tracks   []*model.Track
}

type ssIndex struct {
	artists []*ssArtist
	albums  map[string]*ssAlbum
	byID    map[string]any // 专辑或艺术家 id -> 对象，getMusicDirectory 使用
}

// buildSSIndex 从当前内存曲目库构建艺术家/专辑索引（每次请求实时构建，
// 千级曲目开销可忽略，保证与扫描/编辑结果实时一致）。
func buildSSIndex(s *store) *ssIndex {
	idx := &ssIndex{albums: map[string]*ssAlbum{}, byID: map[string]any{}}
	artistByName := map[string]*ssArtist{}
	artistSeq := map[string]int{}
	albumKey := map[string]int{}

	seq := 0
	for _, t := range s.all() {
		seq++
		artist := ssTag(t, "artist")
		if artist == "" {
			artist = "未知艺术家"
		}
		albumName := ssTag(t, "album")
		if albumName == "" {
			albumName = "未知专辑"
		}
		a, ok := artistByName[artist]
		if !ok {
			h := sha256.Sum256([]byte(artist))
			a = &ssArtist{id: "r-" + hex.EncodeToString(h[:])[:12], name: artist}
			artistByName[artist] = a
			artistSeq[artist] = seq
		}
		ak := artist + "\x00" + albumName
		al, ok := idx.albums[ak]
		if !ok {
			h := sha256.Sum256([]byte(ak))
			al = &ssAlbum{
				id:       "a-" + hex.EncodeToString(h[:])[:12],
				name:     albumName,
				artist:   artist,
				artistID: a.id,
				year:     atoiSafe(ssTag(t, "year")),
				genre:    ssTag(t, "genre"),
			}
			idx.albums[ak] = al
			albumKey[ak] = seq
		}
		al.tracks = append(al.tracks, t)
		al.seq = albumKey[ak]
		if t.HasCover && al.coverID == "" {
			al.coverID = "t-" + t.ID
		}
	}

	// 组装艺术家（按首次出现序），专辑按首次出现序
	names := make([]string, 0, len(artistByName))
	for name := range artistByName {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return artistSeq[names[i]] < artistSeq[names[j]] })
	for _, name := range names {
		a := artistByName[name]
		albs := make([]*ssAlbum, 0, len(a.albums))
		// 收集该艺术家的专辑（保持专辑首次出现序）
		for _, al := range idx.albums {
			if al.artistID == a.id {
				albs = append(albs, al)
			}
		}
		sort.Slice(albs, func(i, j int) bool { return albumKey[albs[i].artist+"\x00"+albs[i].name] < albumKey[albs[j].artist+"\x00"+albs[j].name] })
		a.albums = albs
		for _, al := range albs {
			if a.coverID == "" && al.coverID != "" {
				a.coverID = al.coverID
			}
		}
		idx.artists = append(idx.artists, a)
		idx.byID[a.id] = a
	}
	for _, al := range idx.albums {
		idx.byID[al.id] = al
	}
	return idx
}

// findSSAlbum 按曲目定位其专辑（用于 song.parent 等）。
func albumForTrack(idx *ssIndex, t *model.Track) *ssAlbum {
	if idx == nil || t == nil {
		return nil
	}
	artist := ssTag(t, "artist")
	if artist == "" {
		artist = "未知艺术家"
	}
	album := ssTag(t, "album")
	if album == "" {
		album = "未知专辑"
	}
	return idx.albums[artist+"\x00"+album]
}

func cleanTag(v string) string { return strings.TrimSpace(v) }

// ssTag 大小写不敏感读取曲目标签（库内统一为大写键，兼容小写来源）。
func ssTag(t *model.Track, key string) string {
	if v, ok := t.Tags[key]; ok {
		return strings.TrimSpace(v)
	}
	if v, ok := t.Tags[strings.ToUpper(key)]; ok {
		return strings.TrimSpace(v)
	}
	if v, ok := t.Tags[strings.ToLower(key)]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func atoiSafe(v string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(v))
	return n
}

// ---- 元素构造 ----

// ssSong 构造 Subsonic song 元素（getSong / 搜索结果）。
func ssSong(idx *ssIndex, t *model.Track) *ssElem {
	return ssSongFill(newSS("song"), albumForTrack(idx, t), t)
}

// ssSongOf 用已知专辑构造 song 元素（getAlbum / 目录子元素场景，避免重复索引查找）。
func ssSongOf(a *ssAlbum, t *model.Track) *ssElem {
	return ssSongFill(newSS("song"), a, t)
}

func ssSongFill(song *ssElem, al *ssAlbum, t *model.Track) *ssElem {
	song.attrs["id"] = "t-" + t.ID
	if al != nil {
		song.attrs["parent"] = al.id
	}
	song.attrs["title"] = trackTitle(t)
	if al != nil {
		song.attrs["album"] = al.name
		song.attrs["artist"] = al.artist
		song.attrs["artistId"] = al.artistID
		song.attrs["year"] = al.year
		song.attrs["genre"] = al.genre
	}
	if n := atoiSafe(ssTag(t, "tracknumber")); n > 0 {
		song.attrs["track"] = n
	}
	if t.Duration > 0 {
		song.attrs["duration"] = int(t.Duration)
	}
	song.attrs["size"] = t.Size
	song.attrs["suffix"] = strings.TrimPrefix(t.Ext, ".")
	song.attrs["contentType"] = ssMime(t.Ext)
	song.attrs["path"] = t.Path
	if t.HasCover {
		song.attrs["coverArt"] = "t-" + t.ID
	}
	if t.Bitrate > 0 {
		song.attrs["bitRate"] = int(t.Bitrate)
	}
	if t.SampleRate > 0 {
		song.attrs["sampleRate"] = int(t.SampleRate)
	}
	return song
}

func trackTitle(t *model.Track) string {
	if title := ssTag(t, "title"); title != "" {
		return title
	}
	return strings.TrimSuffix(t.FileName, filepath.Ext(t.FileName))
}

func ssAlbumElem(a *ssAlbum, withSongs bool) *ssElem {
	al := newSS("album")
	al.attrs["id"] = a.id
	al.attrs["name"] = a.name
	al.attrs["artist"] = a.artist
	al.attrs["artistId"] = a.artistID
	if a.coverID != "" {
		al.attrs["coverArt"] = a.coverID
	}
	al.attrs["songCount"] = len(a.tracks)
	var dur int64
	for _, t := range a.tracks {
		dur += int64(t.Duration)
	}
	al.attrs["duration"] = dur
	if a.year > 0 {
		al.attrs["year"] = a.year
	}
	if a.genre != "" {
		al.attrs["genre"] = a.genre
	}
	if withSongs {
		for _, t := range a.tracks {
			al.children = append(al.children, ssSongOf(a, t))
		}
	}
	return al
}

func ssArtistElem(a *ssArtist, withAlbums bool) *ssElem {
	ar := newSS("artist")
	ar.attrs["id"] = a.id
	ar.attrs["name"] = a.name
	if a.coverID != "" {
		ar.attrs["coverArt"] = a.coverID
	}
	if withAlbums {
		for _, al := range a.albums {
			ar.children = append(ar.children, ssAlbumElem(al, false))
		}
	}
	return ar
}

func ssMime(ext string) string {
	if m := mime.TypeByExtension(strings.ToLower(ext)); m != "" {
		return m
	}
	return "application/octet-stream"
}

// ---- 封面 / 歌词 ----

// trackCoverBytes 返回曲目封面字节（复用内存缓存，与 /api/tracks/{id}/cover 一致）。
func trackCoverBytes(s *store, t *model.Track) ([]byte, error) {
	st, err := os.Stat(t.Path)
	if err != nil {
		return nil, err
	}
	s.coverMu.RLock()
	if e, ok := s.coverCache[t.ID]; ok && e.mod.Equal(st.ModTime()) {
		img := e.img
		s.coverMu.RUnlock()
		return img, nil
	}
	s.coverMu.RUnlock()
	img, err := taglibx.ReadCover(t.Path)
	if err != nil || len(img) == 0 {
		return nil, err
	}
	s.coverMu.Lock()
	s.coverCache[t.ID] = coverEntry{mod: st.ModTime(), img: img}
	s.coverMu.Unlock()
	return img, nil
}

// trackLyrics 返回曲目歌词文本：优先外挂 .lrc，其次内嵌歌词标签。
func trackLyrics(t *model.Track) (string, bool) {
	if lp := taglibx.LrcPath(t.Path); lp != "" {
		if b, err := os.ReadFile(lp); err == nil && len(b) > 0 {
			return string(b), true
		}
	}
	if l := ssTag(t, "lyrics"); l != "" {
		return l, true
	}
	return "", false
}

// ---- 方法实现 ----

func ssGetMusicFolders() *ssElem {
	d := newSS("musicFolders")
	d.children = append(d.children, &ssElem{
		name:  "musicFolder",
		attrs: map[string]any{"id": 1, "name": "Music"},
	})
	return d
}

// ssGetIndexes 生成艺术家索引（getIndexes / getArtists / getArtist 共用）。
func ssGetIndexes(idx *ssIndex) *ssElem {
	d := newSS("indexes")
	groups := map[string][]*ssArtist{}
	order := []string{}
	for _, a := range idx.artists {
		key := ssIndexKey(a.name)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], a)
	}
	sort.Strings(order)
	for _, key := range order {
		g := newSS("index")
		g.attrs["name"] = key
		for _, a := range groups[key] {
			g.children = append(g.children, ssArtistElem(a, false))
		}
		d.children = append(d.children, g)
	}
	return d
}

// ssIndexKey 艺术家索引分组键：ASCII 字母取大写首字母，其余归 "#"。
func ssIndexKey(name string) string {
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			return strings.ToUpper(string(r))
		}
		if r >= 'A' && r <= 'Z' {
			return string(r)
		}
		return "#"
	}
	return "#"
}

func ssGetAlbumList(idx *ssIndex, typ string, size int, genre string, fromYear, toYear int) *ssElem {
	all := make([]*ssAlbum, 0, len(idx.albums))
	for _, al := range idx.albums {
		all = append(all, al)
	}
	switch typ {
	case "alphabetical":
		sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	case "newest", "recent":
		sort.Slice(all, func(i, j int) bool { return all[i].seq > all[j].seq })
	case "byGenre":
		filtered := all[:0]
		for _, al := range all {
			if genre == "" || strings.EqualFold(al.genre, genre) {
				filtered = append(filtered, al)
			}
		}
		all = filtered
		sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	case "byYear":
		filtered := all[:0]
		for _, al := range all {
			if al.year == 0 {
				continue
			}
			if fromYear > 0 && al.year < fromYear {
				continue
			}
			if toYear > 0 && al.year > toYear {
				continue
			}
			filtered = append(filtered, al)
		}
		all = filtered
		sort.Slice(all, func(i, j int) bool { return all[i].year > all[j].year })
	case "starred", "frequent", "highest":
		// 无播放/收藏数据：返回空列表（客户端自动显示空态）
		all = nil
	default: // random（缺省）
		for i := len(all) - 1; i > 0; i-- {
			j := int(time.Now().UnixNano() % int64(i+1))
			all[i], all[j] = all[j], all[i]
		}
	}
	if len(all) > size {
		all = all[:size]
	}
	d := newSS(ssAlbumListName(typ))
	for _, al := range all {
		d.children = append(d.children, ssAlbumElem(al, false))
	}
	return d
}

func ssAlbumListName(typ string) string {
	if typ == "" {
		return "albumList"
	}
	return "albumList2"
}

// ssSearch 生成 search2 / search3 结果。
func ssSearch(idx *ssIndex, query string, artistCount, albumCount, songCount int, tag string) *ssElem {
	d := newSS(tag)
	if artistCount <= 0 {
		artistCount = 20
	}
	if albumCount <= 0 {
		albumCount = 20
	}
	if songCount <= 0 {
		songCount = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return d
	}
	seenAlbum := map[string]bool{}
	artistN, albumN, songN := 0, 0, 0
	for _, a := range idx.artists {
		if artistN >= artistCount {
			break
		}
		if strings.Contains(strings.ToLower(a.name), q) {
			d.children = append(d.children, ssArtistElem(a, false))
			artistN++
		}
	}
	for _, al := range idx.albums {
		if albumN >= albumCount {
			break
		}
		if strings.Contains(strings.ToLower(al.name), q) || strings.Contains(strings.ToLower(al.artist), q) {
			if !seenAlbum[al.id] {
				d.children = append(d.children, ssAlbumElem(al, false))
				seenAlbum[al.id] = true
				albumN++
			}
		}
	}
	for _, al := range idx.albums {
		for _, t := range al.tracks {
			if songN >= songCount {
				return d
			}
			if strings.Contains(strings.ToLower(trackTitle(t)), q) {
				d.children = append(d.children, ssSong(nil, t))
				songN++
			}
		}
	}
	return d
}

// subsonicHandler 是 /rest/* 入口：解析方法名并分发。
func subsonicHandler(cfg *cstore.Config, s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.PathValue("rest"), "/")
		method := rest
		if i := strings.IndexAny(method, "."); i > 0 {
			method = method[:i]
		}
		if method == "" {
			ssSend(w, r, nil, 0, "Method not found")
			return
		}
		if !subsonicAuth(cfg, r) {
			ssSend(w, r, nil, 40, "Wrong username or password")
			return
		}
		switch method {
		case "ping":
			ssSend(w, r, nil, 0, "")
		case "getLicense":
			ssSend(w, r, newSS("license"), 0, "")
		case "getMusicFolders":
			ssSend(w, r, ssGetMusicFolders(), 0, "")
		case "getIndexes", "getArtists":
			ssSend(w, r, ssGetIndexes(buildSSIndex(s)), 0, "")
		case "getArtist":
			id := r.FormValue("id")
			idx := buildSSIndex(s)
			if a, ok := idx.byID[id].(*ssArtist); ok {
				ssSend(w, r, ssArtistElem(a, true), 0, "")
			} else {
				ssSend(w, r, nil, 70, "Artist not found")
			}
		case "getAlbum":
			id := r.FormValue("id")
			idx := buildSSIndex(s)
			if al, ok := idx.byID[id].(*ssAlbum); ok {
				ssSend(w, r, ssAlbumElem(al, true), 0, "")
			} else {
				ssSend(w, r, nil, 70, "Album not found")
			}
		case "getSong":
			id := strings.TrimPrefix(r.FormValue("id"), "t-")
			if t := s.get(id); t != nil {
				ssSend(w, r, ssSong(buildSSIndex(s), t), 0, "")
			} else {
				ssSend(w, r, nil, 70, "Song not found")
			}
		case "getAlbumList", "getAlbumList2":
			typ := r.FormValue("type")
			if typ == "" {
				typ = "random"
			}
			size := atoiSafe(r.FormValue("size"))
			if size <= 0 {
				size = 10
			}
			if size > 500 {
				size = 500
			}
			ssSend(w, r, ssGetAlbumList(buildSSIndex(s), typ, size, r.FormValue("genre"), atoiSafe(r.FormValue("fromYear")), atoiSafe(r.FormValue("toYear"))), 0, "")
		case "getMusicDirectory":
			id := r.FormValue("id")
			idx := buildSSIndex(s)
			if a, ok := idx.byID[id].(*ssArtist); ok {
				d := newSS("directory")
				d.attrs["id"] = a.id
				d.attrs["name"] = a.name
				for _, al := range a.albums {
					ch := newSS("child")
					ch.attrs["id"] = al.id
					ch.attrs["parent"] = a.id
					ch.attrs["title"] = al.name
					ch.attrs["album"] = al.name
					ch.attrs["artist"] = al.artist
					ch.attrs["artistId"] = al.artistID
					ch.attrs["isDir"] = true
					if al.coverID != "" {
						ch.attrs["coverArt"] = al.coverID
					}
					ch.attrs["songCount"] = len(al.tracks)
					d.children = append(d.children, ch)
				}
				ssSend(w, r, d, 0, "")
			} else if al, ok := idx.byID[id].(*ssAlbum); ok {
				d := newSS("directory")
				d.attrs["id"] = al.id
				d.attrs["name"] = al.name
				d.attrs["parent"] = al.artistID
				for _, t := range al.tracks {
					c := ssSongOf(al, t)
					c.name = "child" // getMusicDirectory 的曲目子元素为 child
					d.children = append(d.children, c)
				}
				ssSend(w, r, d, 0, "")
			} else {
				ssSend(w, r, nil, 70, "Directory not found")
			}
		case "getGenres":
			idx := buildSSIndex(s)
			d := newSS("genres")
			genreAgg := map[string]int{}
			for _, al := range idx.albums {
				if al.genre != "" {
					genreAgg[al.genre] += len(al.tracks)
				}
			}
			names := make([]string, 0, len(genreAgg))
			for name := range genreAgg {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				g := newSS("genre")
				g.text = name
				g.attrs["songCount"] = genreAgg[name]
				g.attrs["albumCount"] = 0
				d.children = append(d.children, g)
			}
			ssSend(w, r, d, 0, "")
		case "getSongsByGenre":
			genre := r.FormValue("genre")
			idx := buildSSIndex(s)
			d := newSS("songsByGenre")
			for _, al := range idx.albums {
				if !strings.EqualFold(al.genre, genre) {
					continue
				}
				for _, t := range al.tracks {
					d.children = append(d.children, ssSong(nil, t))
				}
			}
			ssSend(w, r, d, 0, "")
		case "search", "search2", "search3":
			query := r.FormValue("query")
			if query == "" {
				query = r.FormValue("q") // 兼容部分客户端用 q 参数
			}
			idx := buildSSIndex(s)
			tag := "searchResult3"
			if method == "search2" {
				tag = "searchResult2"
			} else if method == "search" {
				tag = "searchResult"
			}
			ssSend(w, r, ssSearch(idx, query, atoiSafe(r.FormValue("artistCount")), atoiSafe(r.FormValue("albumCount")), atoiSafe(r.FormValue("songCount")), tag), 0, "")
		case "stream", "download":
			t := s.get(strings.TrimPrefix(r.FormValue("id"), "t-"))
			if t == nil {
				ssSend(w, r, nil, 70, "Song not found")
				return
			}
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", ssMime(t.Ext))
			http.ServeFile(w, r, t.Path) // 自带 Range 支持
		case "getCoverArt":
			t := s.get(strings.TrimPrefix(r.FormValue("id"), "t-"))
			if t == nil {
				ssSend(w, r, nil, 70, "Song not found")
				return
			}
			img, err := trackCoverBytes(s, t)
			if err != nil || len(img) == 0 {
				ssSend(w, r, nil, 70, "No cover art")
				return
			}
			w.Header().Set("Content-Type", http.DetectContentType(img))
			w.Header().Set("Cache-Control", "private, max-age=3600")
			_, _ = w.Write(img)
		case "getLyrics":
			artist := r.FormValue("artist")
			title := r.FormValue("title")
			d := newSS("lyrics")
			for _, t := range s.all() {
				if artist != "" && !strings.EqualFold(ssTag(t, "artist"), artist) {
					continue
				}
				if title != "" && !strings.EqualFold(trackTitle(t), title) {
					continue
				}
				if lyr, ok := trackLyrics(t); ok {
					d.attrs["artist"] = ssTag(t, "artist")
					d.attrs["title"] = trackTitle(t)
					d.text = lyr
					break
				}
			}
			ssSend(w, r, d, 0, "")
		case "getUser":
			d := newSS("user")
			d.attrs["username"] = cfg.GetDefault("admin_user", "admin")
			d.attrs["admin"] = true
			d.children = append(d.children, &ssElem{name: "folder", attrs: map[string]any{"id": 1}})
			ssSend(w, r, d, 0, "")
		case "getPlaylists":
			ssSend(w, r, newSS("playlists"), 0, "")
		case "getPlaylist":
			ssSend(w, r, newSS("playlist"), 0, "")
		case "getStarred", "getStarred2":
			name := "starred2"
			if method == "getStarred" {
				name = "starred"
			}
			ssSend(w, r, newSS(name), 0, "")
		case "getNowPlaying":
			ssSend(w, r, newSS("nowPlaying"), 0, "")
		case "scrobble", "nowPlaying", "star", "unstar", "setRating":
			ssSend(w, r, nil, 0, "")
		case "getTopSongs":
			ssSend(w, r, newSS("topSongs"), 0, "")
		case "getSimilarSongs", "getSimilarSongs2":
			ssSend(w, r, newSS("similarSongs"), 0, "")
		case "getArtistInfo", "getArtistInfo2":
			ssSend(w, r, newSS("artistInfo"), 0, "")
		case "getAvatar":
			ssSend(w, r, nil, 70, "Not found")
		default:
			ssSend(w, r, nil, 0, "Method not found")
		}
	}
}
