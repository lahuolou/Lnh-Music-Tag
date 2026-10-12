// 插件设置接口：GET /api/plugins 列出全部插件（前端按类型分组渲染），
// POST /api/plugins 批量启停/配置并持久化、即时生效。
// 后续新增音源/下载/自动换源/Subsonic 协议等插件时，只需在启动时
// registerPlugin 注册描述与生效回调，前端与接口无需改动。
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"LNH-musictag/internal/scrape"
	cstore "LNH-musictag/internal/store"
)

// keyedSources 需要 API Key 的刮削源：源名 -> 设置库 Key 键名。
// 这些源默认关闭，需先在「设置 → 数据库配置」填写对应 Key 再开启。
var keyedSources = map[string]string{
	"lastfm":  "lastfm_key",
	"spotify": "spotify_id", // Spotify 需要 ID+Secret 两个 Key（KeyField 用 spotify_id）
	"discogs": "discogs_token",
	"jamendo": "jamendo_client_id",
}

// registerBuiltinPlugins 注册内置插件：
//  - scrape.*：现有刮削/音源（auto 内置不可停用；需 Key 的源默认关闭）
//  - tool.ffmpeg：FFmpeg 工具（可配置路径、可一键安装）
//  download / protocol 类型预留：后续插件直接在此追加注册。
func registerBuiltinPlugins(cfg *cstore.Config, ms *scrape.MultiSource) {
	for _, s := range ms.Sources() {
		name := "scrape." + s.Name
		// 内置插件（内部）：只有开关，不可删除
		builtin := true
		keyField := keyedSources[s.Name]
		// 需 Key 的源默认关闭（并且实际生效），配置 Key 前不参与自动刮削
		enabled := s.Name == "auto" || keyField == ""
		if !enabled {
			ms.SetEnabled(s.Name, false)
		}
		registerPlugin(&Plugin{
			Name:      name,
			Label:     s.Label,
			Kind:      PluginScrape,
			Enabled:   enabled,
			Builtin:   builtin,
			NeedsKey:  keyField != "",
			KeyField:  keyField,
		}, func(n string, on bool, _ map[string]string) {
			ms.SetEnabled(n[len("scrape."):], on)
		})
	}
	ff := &Plugin{
		Name:        "tool.ffmpeg",
		Label:       "FFmpeg",
		Kind:        PluginTool,
		Enabled:     true,
		Builtin:     true,
		Installable: true,
		Hint:        "ffHint",
		Fields: []PluginField{
			{Key: "path", Label: "ffPath", Type: "text", Placeholder: "ffPathPlaceholder"},
		},
	}
	registerPlugin(ff, nil)
	// 预留类型注册示例（后续实现时在此补充）：
	//   registerPlugin(&Plugin{Name:"download.example", Label:"…", Kind:PluginDownload,...})
	//   registerPlugin(&Plugin{Name:"protocol.subsonic", Label:"Subsonic", Kind:PluginProtocol,...})
	_ = cfg
}

// pluginsHandler GET 列出全部插件（含启停/配置/Key 状态），供插件页动态渲染。
func pluginsHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list := pluginsSnapshot()
		// 计算依赖 Key 的插件是否已配置（KeyConfigured）
		for i := range list {
			p := &list[i]
			if !p.NeedsKey || p.KeyField == "" || cfg == nil {
				continue
			}
			// Spotify 需要 ID + Secret 两个 Key
			if p.KeyField == "spotify_id" {
				a, _ := cfg.Get("spotify_id")
				b, _ := cfg.Get("spotify_secret")
				p.KeyConfigured = a != "" && b != ""
				continue
			}
			if v, ok := cfg.Get(p.KeyField); ok && v != "" {
				p.KeyConfigured = true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"plugins": list})
	}
}

// pluginImportDecl 第三方插件导入声明（与 Plugin 同构的 JSON 子集）。
type pluginImportDecl struct {
	Name    string        `json:"name"`
	Label   string        `json:"label"`
	Kind    string        `json:"kind"`
	Version string        `json:"version,omitempty"`
	Hint    string        `json:"hint,omitempty"`
	Fields  []PluginField `json:"fields,omitempty"`
}

// pluginsImportHandler POST /api/plugins/import 导入第三方插件。
// 支持两种形态：
//  1. ZIP 插件包（推荐）：包内 plugin.json + 可执行入口，导入即解压即用；
//  2. JSON 声明：单对象 / {"plugins":[...]} / 裸数组。
// 校验：name 非空且不得与已注册插件冲突（防止覆盖内置/重复导入）、
// kind 必须合法。导入成功即注册并持久化（重启后仍在插件列表）。
func pluginsImportHandler(cfg *cstore.Config, configDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
			return
		}
		// ZIP 插件包：魔数 PK\x03\x04
		if len(body) >= 4 && body[0] == 0x50 && body[1] == 0x4B && body[2] == 0x03 && body[3] == 0x04 {
			zipName := "plugin.zip"
			if cd := r.Header.Get("X-File-Name"); cd != "" && len(cd) < 200 {
				zipName = cd
			}
			p, err := importPluginZip(cfg, configDir, body, zipName)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"imported": []Plugin{*p}, "plugins": pluginsSnapshot()})
			return
		}
		var decls []pluginImportDecl
		// 先探测形态：{plugins:[...]} / 裸数组 / 单对象
		var probe struct {
			Plugins []pluginImportDecl `json:"plugins"`
		}
		switch {
		case json.Unmarshal(body, &probe) == nil && len(probe.Plugins) > 0:
			decls = probe.Plugins
		case json.Unmarshal(body, &decls) == nil && len(decls) > 0:
			// 裸数组
		default:
			var one pluginImportDecl
			if json.Unmarshal(body, &one) == nil && one.Name != "" {
				decls = []pluginImportDecl{one}
			}
		}
		if len(decls) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件声明为空或格式不正确"})
			return
		}
		existing := map[string]bool{}
		for _, p := range pluginsSnapshot() {
			existing[p.Name] = true
		}
		imported := []Plugin{}
		for _, d := range decls {
			if d.Name == "" || d.Label == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件 name/label 不能为空: " + d.Name})
				return
			}
			kind := PluginKind(d.Kind)
			switch kind {
			case PluginScrape, PluginTool, PluginDownload, PluginProtocol:
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法插件类型 kind: " + d.Kind + "（可选 scrape/tool/download/protocol）"})
				return
			}
			if existing[d.Name] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件已存在（内置或已导入）: " + d.Name})
				return
			}
			registerPlugin(&Plugin{
				Name:    d.Name,
				Label:   d.Label,
				Kind:    kind,
				Enabled: true,
				Version: d.Version,
				Hint:    d.Hint,
				Fields:  d.Fields,
			}, nil)
			existing[d.Name] = true
			imported = append(imported, Plugin{Name: d.Name, Label: d.Label, Kind: kind, Enabled: true, Version: d.Version, Hint: d.Hint, Fields: d.Fields, Config: map[string]string{}})
		}
		savePluginsToDB(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "plugins": pluginsSnapshot()})
	}
}

// pluginsDeleteHandler DELETE /api/plugins/{name} 删除上传导入的第三方插件。
// 内置插件不可删除（返回 400）；删除后立即持久化。
func pluginsDeleteHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing plugin name"})
			return
		}
		reg.mu.RLock()
		p := reg.plugins[name]
		reg.mu.RUnlock()
		if p == nil || p.Builtin {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件不存在或是内置插件，无法删除: " + name})
			return
		}
		if !unregisterPlugin(name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件不存在或是内置插件，无法删除: " + name})
			return
		}
		savePluginsToDB(cfg)
		deletePluginZip(p)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": name, "plugins": pluginsSnapshot()})
	}
}

// pluginsRunHandler POST /api/plugins/{name}/run 调用第三方 ZIP 插件：
// body: {"op":"...", ...args} → 宿主以固定 JSON 协议 exec 插件入口，返回插件输出。
// 仅 ZIP 插件（已解压）支持调用；内置插件无入口则返回明确错误。
func pluginsRunHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		reg.mu.RLock()
		p := reg.plugins[name]
		reg.mu.RUnlock()
		if p == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "插件不存在: " + name})
			return
		}
		if p.ZipDir == "" || len(p.Entries) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "插件无可执行入口（内置插件不支持直接调用）: " + name})
			return
		}
		var args map[string]any
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &args); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
				return
			}
		}
		op, _ := args["op"].(string)
		if op == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 op 字段"})
			return
		}
		delete(args, "op")
		res, err := runPluginEntry(p, op, args)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "detail": res})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// pluginsUpdateHandler POST 批量更新插件状态：
// body: { "scrape.qq": {"enabled": false}, "tool.ffmpeg": {"config": {"path": "/usr/bin/ffmpeg"}} }
// 只更新传了 enabled / config 的字段；未知插件名直接报错。
func pluginsUpdateHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]struct {
			Enabled *bool             `json:"enabled"`
			Config  map[string]string `json:"config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
			return
		}
		byName := map[string]*Plugin{}
		for _, p := range pluginsSnapshot() {
			pp := p
			byName[pp.Name] = &pp
		}
		for name, upd := range req {
			p, ok := byName[name]
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown plugin: " + name})
				return
			}
			enabled := p.Enabled
			if upd.Enabled != nil {
				enabled = *upd.Enabled
			}
			if p.Builtin {
				enabled = true // 内置不可停用
			}
			applyPluginState(name, enabled, upd.Config)
		}
		savePluginsToDB(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"plugins": pluginsSnapshot()})
	}
}
