// 插件设置接口：GET /api/plugins 列出全部插件（前端按类型分组渲染），
// POST /api/plugins 批量启停/配置并持久化、即时生效。
// 后续新增音源/下载/自动换源/协议类插件时，只需在启动时
// registerPlugin 注册描述与生效回调，前端与接口无需改动。
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
)

// keyedSources 需要 API Key 的刮削源：源名 -> 设置库 Key 键名。
// 这些源默认关闭，需先在「插件页」填写对应 Key 再开启。
var keyedSources = map[string]string{
	"lastfm":  "lastfm_key",
	"spotify": "spotify_id", // Spotify 需要 ID+Secret 两个 Key（KeyField 用 spotify_id）
	"discogs": "discogs_token",
	"jamendo": "jamendo_client_id",
}

// keyedSourceFields 需 Key 源在插件页内嵌的 Key 输入字段（渲染为密码/文本框）。
var keyedSourceFields = map[string][]PluginField{
	"lastfm":  {{Key: "lastfm_key", Label: "fLastfm", Type: "password"}},
	"spotify": {{Key: "spotify_id", Label: "fSpotifyId", Type: "text"}, {Key: "spotify_secret", Label: "fSpotifySecret", Type: "password"}},
	"discogs": {{Key: "discogs_token", Label: "fDiscogs", Type: "password"}},
	"jamendo": {{Key: "jamendo_client_id", Label: "fJamendo", Type: "password"}},
}

// pluginKeyFields 返回某插件配置中属于"API Key"的键列表：
// 这些键在保存/加载时会同步到设置库（cstore），供 AcoustID/刮削源
// 内部使用点（cfg.Get 读取）直接生效，无需额外改造。
func pluginKeyFields(p *Plugin) []string {
	switch p.Name {
	case "tool.acoustid":
		return []string{"acoustid_key"}
	case "scrape.spotify":
		return []string{"spotify_id", "spotify_secret"}
	case "tool.streaming_gateway":
		return []string{"stream_pass"}
	}
	if p.KeyField != "" {
		return []string{p.KeyField}
	}
	return nil
}

// keyReady 判断需 Key 插件在合并配置后是否已配好（spotify 需 ID+Secret 两项，
// 其余按 KeyField 判空）。p.Config 须为合并后的最新配置；非 Key 插件恒为 true。
func keyReady(p *Plugin) bool {
	if p.Name == "scrape.spotify" {
		return strings.TrimSpace(p.Config["spotify_id"]) != "" && strings.TrimSpace(p.Config["spotify_secret"]) != ""
	}
	if p.KeyField != "" {
		return strings.TrimSpace(p.Config[p.KeyField]) != ""
	}
	return true
}

// acoustidActive 判定在线音频识别是否真正可用：插件开关已开启且配了 Key。
// 未开启或无 Key 时，批量/一键刮削跳过在线 AcoustID 查询（库内回退另行处理）。
func acoustidActive(cfg *cstore.Config) bool {
	return pluginOn("tool.acoustid") && strings.TrimSpace(cfg.GetDefault("acoustid_key", "")) != ""
}

// syncClientKeys 把设置库中的 API Key 同步到内存刮削客户端（ms.Client()），
// 与设置页保存行为一致。启动恢复（loadPluginsFromDB）与插件页保存
// （pluginsUpdateHandler）共用此入口，保证两条路径行为统一。
func syncClientKeys(cfg *cstore.Config, ms *scrape.MultiSource) {
	if cfg == nil || ms == nil {
		return
	}
	// AcoustID 在线查询受插件开关门控：关闭或未配 Key 时不下发 Key，
	// 客户端直接走库内回退/跳过在线查询。
	if pluginOn("tool.acoustid") {
		ms.Client().AcoustIDAPIKey = cfg.GetDefault("acoustid_key", "")
	} else {
		ms.Client().AcoustIDAPIKey = ""
	}
	ms.Client().LastFMKey = cfg.GetDefault("lastfm_key", "")
	ms.Client().DiscogsToken = cfg.GetDefault("discogs_token", "")
	ms.Client().JamendoClientID = cfg.GetDefault("jamendo_client_id", "")
	ms.Client().SpotifyID = cfg.GetDefault("spotify_id", "")
	ms.Client().SpotifySecret = cfg.GetDefault("spotify_secret", "")
}

// registerBuiltinPlugins 注册内置插件：
//   - scrape.*：现有刮削/音源（auto 内置不可停用；需 Key 的源默认关闭）
//   - tool.ffmpeg：FFmpeg 工具（可配置路径、可一键安装）
//   - tool.acoustid：AcoustID 录音识别 Key 配置
func registerBuiltinPlugins(ms *scrape.MultiSource, cfg *cstore.Config) {
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
			Name:     name,
			Label:    s.Label,
			Kind:     PluginScrape,
			Enabled:  enabled,
			Builtin:  builtin,
			NeedsKey: keyField != "",
			KeyField: keyField,
			Fields:   keyedSourceFields[s.Name], // 需 Key 源内嵌 Key 输入框（插件页直接配置）
		}, func(n string, on bool, _ map[string]string) {
			ms.SetEnabled(n[len("scrape."):], on)
		})
	}
	// FFmpeg 工具：默认按当前环境探测结果决定开关——探测到二进制才开启，
	// 未安装/未配置时默认关闭（持久化状态优先于该默认值，见 loadPluginsFromDB）。
	ff := &Plugin{
		Name:        "tool.ffmpeg",
		Label:       "FFmpeg",
		Kind:        PluginTool,
		Enabled:     ffmpegBin(cfg) != "",
		Builtin:     true,
		Installable: true,
		Hint:        "ffHint",
		Fields: []PluginField{
			{Key: "path", Label: "ffPath", Type: "text", Placeholder: "ffPathPlaceholder"},
		},
	}
	registerPlugin(ff, nil)
	// AcoustID 插件：需先配置 API Key 才可用，默认关闭；未配 Key 时开关禁用。
	registerPlugin(&Plugin{
		Name:     "tool.acoustid",
		Label:    "AcoustID",
		Kind:     PluginTool,
		Enabled:  false,
		Builtin:  true,
		NeedsKey: true,
		Hint:     "acoustidHint",
		KeyField: "acoustid_key",
		Fields:   []PluginField{{Key: "acoustid_key", Label: "fAcoustid", Type: "password"}},
	}, nil)
	// 内置 Subsonic/OpenSubsonic 音乐网关：复用主程序扫描出的曲库。
	gatewayEnabled := cfg.GetDefault("streaming_gateway_enabled", "true") != "false"
	registerPlugin(&Plugin{
		Name: "tool.streaming_gateway", Label: "音乐统一网关（Subsonic / Jellyfin / Emby / Navidrome）", Kind: PluginTool,
		Enabled: gatewayEnabled, Builtin: true, Version: AppVersion(),
		Hint: "为第三方音乐播放器提供 Subsonic / OpenSubsonic、Jellyfin、Emby 与 Navidrome 兼容接口；四个协议统一仅用下面的流媒体密码认证（用户名任意填写）。必须设置流媒体密码后才能启用网关。",
		Fields: []PluginField{
			{Key: "stream_pass", Label: "流媒体密码（四协议播放器连接的唯一凭证；留空则播放器一律无法连接）", Type: "password", Placeholder: "设置播放器连接密码"},
		},
	}, func(_ string, on bool, cfgMap map[string]string) {
		if cfg != nil {
			_ = cfg.Set("streaming_gateway_enabled", map[bool]string{true: "true", false: "false"}[on])
			if cfgMap != nil {
				// 过滤脱敏占位符，避免 ******** 写回真实密码
				if v := cfgMap["stream_pass"]; v != "" && v != keyRedacted {
					_ = cfg.Set("stream_pass", v)
				}
			}
		}
	})
}

// pluginsHandler GET 列出全部插件（含启停/配置/Key 状态），供插件页动态渲染。
func pluginsHandler(cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list := pluginsSnapshot()
		// Key 配置状态：从插件自身 Config 判断（保存时已同步，重启后随
		// loadPluginsFromDB 恢复）；无需再读设置库。
		for i := range list {
			p := &list[i]
			if p.NeedsKey || p.KeyField != "" {
				p.KeyConfigured = keyReady(p)
			}
			// 依赖就绪门控：未就绪时强制回显为关闭，避免"开着却用不了"。
			switch p.Name {
			case "tool.ffmpeg":
				p.Available = ffmpegBin(cfg) != ""
				if !p.Available {
					p.Enabled = false
					p.Unavailable = "ffmpeg_missing"
				}
			case "tool.acoustid":
				p.Available = p.KeyConfigured
				if !p.Available {
					p.Enabled = false
					p.Unavailable = "key_missing"
				}
			case "tool.streaming_gateway":
				// 音乐统一网关强制设置密码：未配置流媒体密码（stream_pass，
				// 兼容 LNH_STREAM_PASS 环境变量）时网关视为不可用，强制回显为关闭。
				p.Available = strings.TrimSpace(cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS"))) != ""
				if !p.Available {
					p.Enabled = false
					p.Unavailable = "gateway_password_missing"
				}
			default:
				if p.NeedsKey {
					p.Available = p.KeyConfigured
					if !p.Available {
						p.Enabled = false
					}
				} else {
					p.Available = true
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"plugins": list})
	}
}

// pluginImportDecl 第三方插件导入声明（与 Plugin 同构的 JSON 子集）。
type pluginImportDecl struct {
	Name     string        `json:"name"`
	Label    string        `json:"label"`
	Kind     string        `json:"kind"`
	Version  string        `json:"version,omitempty"`
	Hint     string        `json:"hint,omitempty"`
	Fields   []PluginField `json:"fields,omitempty"`
	KeyField string        `json:"keyField,omitempty"`
}

// pluginsImportHandler POST /api/plugins/import 导入第三方插件。
// 支持两种形态：
//  1. ZIP 插件包（推荐）：包内 plugin.json + 可执行入口，导入即解压即用；
//  2. JSON 声明：单对象 / {"plugins":[...]} / 裸数组。
//
// 校验：name 非空且不得与已注册插件冲突（防止覆盖内置/重复导入）、
// kind 必须合法。导入成功即注册并持久化（重启后仍在插件列表）。
func pluginsImportHandler(cfg *cstore.Config, configDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
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
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			addLog("act_plugin_import", true, 1, 0, 0, "导入插件 "+p.Label+"（"+p.Name+"）")
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
			writeErr(w, http.StatusBadRequest, "插件声明为空或格式不正确")
			return
		}
		imported := []Plugin{}
		for _, d := range decls {
			if d.Name == "" || d.Label == "" {
				writeErr(w, http.StatusBadRequest, "插件 name/label 不能为空: "+d.Name)
				return
			}
			kind := PluginKind(d.Kind)
			switch kind {
			case PluginScrape, PluginTool:
			default:
				writeErr(w, http.StatusBadRequest, "非法插件类型 kind: "+d.Kind+"（可选 scrape/tool）")
				return
			}
			if old, ok := reg.plugins[d.Name]; ok && old.Builtin {
				writeErr(w, http.StatusBadRequest, "内置插件不可覆盖: "+d.Name)
				return
			}
			// 覆盖导入：已导入的第三方插件允许覆盖（更新定义），保留原配置。
			oldConfig := map[string]string{}
			if old, ok := reg.plugins[d.Name]; ok {
				oldConfig = old.Config
			}
			replacePlugin(&Plugin{
				Name:     d.Name,
				Label:    d.Label,
				Kind:     kind,
				Enabled:  true,
				Version:  d.Version,
				Hint:     d.Hint,
				Fields:   d.Fields,
				KeyField: d.KeyField,
				Config:   oldConfig,
			}, nil)
			imported = append(imported, Plugin{Name: d.Name, Label: d.Label, Kind: kind, Enabled: true, Version: d.Version, Hint: d.Hint, Fields: d.Fields, KeyField: d.KeyField, Config: oldConfig})
			addLog("act_plugin_import", true, 1, 0, 0, "导入插件 "+d.Label+"（"+d.Name+"）")
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
			writeErr(w, http.StatusBadRequest, "missing plugin name")
			return
		}
		reg.mu.RLock()
		p := reg.plugins[name]
		reg.mu.RUnlock()
		if p == nil || p.Builtin {
			writeErr(w, http.StatusBadRequest, "插件不存在或是内置插件，无法删除: "+name)
			return
		}
		if !unregisterPlugin(name) {
			writeErr(w, http.StatusBadRequest, "插件不存在或是内置插件，无法删除: "+name)
			return
		}
		savePluginsToDB(cfg)
		deletePluginZip(p)
		// 卸载无残留：清掉该插件同步到设置库的 API Key 字段（acoustid_key /
		// spotify_id / secret 及第三方声明 KeyField），避免卸载后配置残留。
		for _, k := range pluginKeyFields(p) {
			_ = cfg.Del(k)
		}
		addLog("act_plugin_delete", true, 1, 0, 0, "删除插件 "+p.Label+"（"+p.Name+"）")
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
			writeErr(w, http.StatusNotFound, "插件不存在: "+name)
			return
		}
		if p.ZipDir == "" || len(p.Entries) == 0 {
			writeErr(w, http.StatusBadRequest, "插件无可执行入口（内置插件不支持直接调用）: "+name)
			return
		}
		var args map[string]any
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &args); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
				return
			}
		}
		op, _ := args["op"].(string)
		if op == "" {
			writeErr(w, http.StatusBadRequest, "缺少 op 字段")
			return
		}
		delete(args, "op")
		res, err := runPluginEntry(p, op, args)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error(), "detail": res})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// pluginsUpdateHandler POST 批量更新插件状态：
// body: { "scrape.qq": {"enabled": false}, "tool.ffmpeg": {"config": {"path": "/usr/bin/ffmpeg"}} }
// 只更新传了 enabled / config 的字段；未知插件名直接报错。
func pluginsUpdateHandler(cfg *cstore.Config, ms *scrape.MultiSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]struct {
			Enabled *bool             `json:"enabled"`
			Config  map[string]string `json:"config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		byName := map[string]*Plugin{}
		for _, p := range pluginsSnapshot() {
			pp := p
			byName[pp.Name] = &pp
		}
		changes := []string{}
		for name, upd := range req {
			p, ok := byName[name]
			if !ok {
				writeErr(w, http.StatusBadRequest, "unknown plugin: "+name)
				return
			}
			enabled := p.Enabled
			if upd.Enabled != nil {
				enabled = *upd.Enabled
			}
			// 仅 auto（内置兜底源）不可停用；其余内置插件（刮削源/FFmpeg）
			// 允许开关控制，仅禁止删除（删除由 pluginsDeleteHandler 限制 Builtin）
			if p.Name == "scrape.auto" {
				enabled = true
			}
			// FFmpeg：未探测到二进制时拒绝开启，避免"开关开着却无转码能力"。
			// （路径在下方合并配置后才可见，故门控放在 applyPluginState 之后统一判定。）
			// 汇总变更摘要（启停 + 配置键），用于操作日志
			if upd.Enabled != nil && enabled != p.Enabled {
				if enabled {
					changes = append(changes, "启用 "+p.Label)
				} else {
					changes = append(changes, "停用 "+p.Label)
				}
			}
			for k, v := range upd.Config {
				// 前端未改动的 API Key（脱敏占位符）：跳过，避免把 "********" 误写回
				if v == keyRedacted && isKeyField(p, k) {
					continue
				}
				// API Key 类字段的值不写入日志（避免密钥明文泄露）
				val := v
				for _, kf := range pluginKeyFields(p) {
					if k == kf {
						val = "***"
						break
					}
				}
				changes = append(changes, p.Label+"."+k+"="+val)
			}
			applyPluginState(name, enabled, upd.Config)
			// Key 字段同步到设置库（cstore）：AcoustID/刮削源使用点直接生效
			// （脱敏占位符跳过，防止 "********" 覆盖真实密码）
			for _, k := range pluginKeyFields(p) {
				if v, ok := upd.Config[k]; ok && v != "" && v != keyRedacted {
					cfg.Set(k, strings.TrimSpace(v))
				}
			}
			// 门控 1：FFmpeg 开启但探测不到二进制（含本次新填路径）→ 拒绝开启，
			// 避免"开关开着却无转码能力"的死状态。
			if name == "tool.ffmpeg" && enabled && ffmpegBin(cfg) == "" {
				applyPluginState(name, false, nil)
				writeErr(w, http.StatusBadRequest, "未检测到 FFmpeg，请先一键安装或填写可执行文件绝对路径后再开启")
				return
			}
			// 门控 2：需 Key 插件开启，但合并配置后仍未配好 Key → 自动停用。
			if p.NeedsKey && enabled {
				merged := *p
				merged.Config = getPluginConfig(name)
				if !keyReady(&merged) {
					applyPluginState(name, false, nil)
					enabled = false
					changes = append(changes, "停用 "+p.Label+"（未配置 Key）")
				}
			}
			// 门控 3：音乐统一网关强制设置密码——开启但未配置流媒体密码
			// （stream_pass，兼容 LNH_STREAM_PASS）→ 拒绝开启，避免
			// "网关开着却无法连接"的死状态。
			if name == "tool.streaming_gateway" && enabled {
				if strings.TrimSpace(cfg.GetDefault("stream_pass", os.Getenv("LNH_STREAM_PASS"))) == "" {
					applyPluginState(name, false, nil)
					writeErr(w, http.StatusBadRequest, "请先在插件配置中设置流媒体密码，再开启音乐统一网关")
					return
				}
			}
		}
		savePluginsToDB(cfg)
		// Key 同步到内存刮削客户端（与设置页保存行为一致）
		syncClientKeys(cfg, ms)
		detail := ""
		for i, c := range changes {
			if i > 0 {
				detail += "；"
			}
			detail += c
		}
		if detail == "" {
			detail = "无变更"
		}
		addLog("act_plugin", true, len(changes), 0, 0, detail)
		writeJSON(w, http.StatusOK, map[string]any{"plugins": pluginsSnapshot()})
	}
}

// pluginPanelHandler GET /api/plugins/{name}/panel 返回插件面板入口文件
// （单文件形态，兼容「全内联单文件面板」：读 plugin.json 的 panel 字段直接
// ServeFile）。多文件面板可继续用 /panel/{path...} 路由加载资源。
func pluginPanelHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		reg.mu.RLock()
		p := reg.plugins[name]
		reg.mu.RUnlock()
		if p == nil || p.ZipDir == "" {
			http.NotFound(w, r)
			return
		}
		declPath := filepath.Join(p.ZipDir, "plugin.json")
		b, err := os.ReadFile(declPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var decl struct {
			Panel string `json:"panel"`
		}
		if json.Unmarshal(b, &decl) != nil || decl.Panel == "" {
			http.NotFound(w, r)
			return
		}
		root := filepath.Clean(p.ZipDir)
		panelPath := filepath.Clean(filepath.Join(root, filepath.FromSlash(decl.Panel)))
		if !strings.HasPrefix(panelPath, root+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mimeTypeByExt(filepath.Ext(panelPath)))
		http.ServeFile(w, r, panelPath)
	}
}

// panelAssetHandler GET /api/plugins/{name}/panel/{path...} 静态服务第三方
// 插件的 Web 面板资源（zip 内 ui 目录等）。仅服务已导入 ZIP 插件；路径做
// 规范化与前缀校验，杜绝目录穿越。MIME 由扩展名推断，未知类型按 text/plain。
func panelAssetHandler(cfg *cstore.Config, configDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		reg.mu.RLock()
		p := reg.plugins[name]
		reg.mu.RUnlock()
		if p == nil || p.ZipDir == "" {
			http.NotFound(w, r)
			return
		}
		rel := r.PathValue("path")
		if rel == "" {
			http.NotFound(w, r)
			return
		}
		root := filepath.Clean(p.ZipDir)
		fp := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
		if !strings.HasPrefix(fp, root+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(fp)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mimeTypeByExt(filepath.Ext(fp)))
		// 面板 HTML 允许脚本与样式（同源服务，无 CSP 限制）
		http.ServeContent(w, r, filepath.Base(fp), st.ModTime(), f)
	}
}

// mimeTypeByExt 按扩展名返回 Content-Type（面板资源常用类型；未知按 text/plain）。
func mimeTypeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".woff", ".woff2":
		return "font/woff2"
	default:
		return "text/plain; charset=utf-8"
	}
}

// fsScanDirHandler POST /api/fs/scan-dir 列出目录内匹配后缀的文件（供第三方
// 插件面板使用：如解锁插件扫描加密音乐）。仅返回常规文件，递归子目录。
// body: {"dir":"...", "exts":[".qmc0",".mflac",...]}；exts 为空时列出全部文件。
func fsScanDirHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Dir  string   `json:"dir"`
			Exts []string `json:"exts"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		dir := strings.TrimSpace(req.Dir)
		if dir == "" {
			writeErr(w, http.StatusBadRequest, "缺少目录")
			return
		}
		dir = filepath.Clean(dir)
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			writeErr(w, http.StatusBadRequest, "目录不可访问: "+dir)
			return
		}
		extSet := map[string]bool{}
		for _, e := range req.Exts {
			e = strings.ToLower(strings.TrimSpace(e))
			if e != "" {
				extSet[e] = true
			}
		}
		files := []map[string]any{}
		err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 跳过无权限子项
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if len(extSet) > 0 && !extSet[ext] {
				return nil
			}
			fi, err := d.Info()
			if err != nil || !fi.Mode().IsRegular() {
				return nil
			}
			files = append(files, map[string]any{
				"path":     path,
				"fileName": filepath.Base(path),
				"ext":      ext,
				"size":     fi.Size(),
			})
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": files, "total": len(files)})
	}
}
