// 通用插件框架：统一管理「刮削源 / 工具 / 下载 / 服务协议」四类插件。
//  - 插件声明：名称、展示名、类型、配置字段、是否内置、是否可安装；
//  - 启停与配置：通过 /api/plugins 统一读写，持久化到 SQLite（plugins 键）；
//  - 扩展点：后续音源、下载器、自动换源策略、Subsonic 等协议插件都按
//    同一套注册表接入，前端设置页按类型自动渲染，无需改页面。
package main

import (
	"encoding/json"
	"os"
	"sync"

	cstore "LNH-musictag/internal/store"
)

// PluginKind 插件类型。
type PluginKind string

const (
	PluginScrape   PluginKind = "scrape"   // 刮削/音源（元数据、歌词、封面）
	PluginTool     PluginKind = "tool"     // 工具（FFmpeg 等）
	PluginDownload PluginKind = "download" // 下载器（预留）
	PluginProtocol PluginKind = "protocol" // 服务端协议（Subsonic 等，预留）
)

// PluginField 插件的可配置字段（前端据此渲染表单）。
type PluginField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // text / password / bool
	Placeholder string `json:"placeholder,omitempty"`
}

// Plugin 插件描述（返回给前端的快照）。
type Plugin struct {
	Name       string            `json:"name"`   // 唯一标识，如 scrape.qq / tool.ffmpeg
	Label      string            `json:"label"`  // 展示名
	Kind       PluginKind        `json:"kind"`
	Enabled    bool              `json:"enabled"`
	Builtin    bool              `json:"builtin"`    // 内置不可删除；auto 等不可停用
	Installable bool             `json:"installable"` // 支持一键安装（容器内执行）
	NeedsKey   bool              `json:"needsKey"`    // 依赖外部 API Key（默认关闭，配置后开启）
	KeyField   string            `json:"keyField,omitempty"`   // 对应设置库 Key 键名
	KeyConfigured bool           `json:"keyConfigured"` // 当前 Key 是否已配置（快照时由 API 层计算）
	Version    string            `json:"version,omitempty"`
	Hint       string            `json:"hint,omitempty"`
	Fields     []PluginField     `json:"fields,omitempty"`
	Config     map[string]string `json:"config"` // 始终输出（可能为空 {}），避免前端渲染崩溃
	// —— ZIP 插件包字段（第三方上传导入，解压即用）——
	ZipDir  string            `json:"-"`                 // 解压目录（绝对路径，仅宿主内部使用）
	Entries map[string]string `json:"entries,omitempty"` // 平台(linux/windows/darwin) -> zip 内相对入口
	ZipName string            `json:"zipName,omitempty"` // 插件包文件名（导入时上传的原文件名）
}

// applyFn 插件配置生效回调（启停/配置变更时执行）。
type applyFn func(name string, enabled bool, cfg map[string]string)

type pluginRegistry struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
	order   []string // 注册顺序：插件列表按注册序返回，国内源在前
	apply   map[string]applyFn
}

var reg = &pluginRegistry{plugins: map[string]*Plugin{}, apply: map[string]applyFn{}}

// registerPlugin 注册插件；apply 为配置生效回调（可 nil）。
func registerPlugin(p *Plugin, apply applyFn) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.plugins[p.Name]; !ok {
		reg.plugins[p.Name] = p
		reg.order = append(reg.order, p.Name)
		reg.apply[p.Name] = apply
	}
}

// pluginsSnapshot 返回全部插件快照（按注册顺序，深拷贝，防外部篡改）。
func pluginsSnapshot() []Plugin {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]Plugin, 0, len(reg.order))
	for _, n := range reg.order {
		p := reg.plugins[n]
		np := *p
		if p.Config != nil {
			np.Config = make(map[string]string, len(p.Config))
			for k, v := range p.Config {
				np.Config[k] = v
			}
		} else {
			np.Config = map[string]string{} // 保证前端始终拿到非空 config，避免渲染崩溃
		}
		out = append(out, np)
	}
	return out
}

// unregisterPlugin 删除插件（仅非内置插件；内置插件受保护）。
// 用于「上传导入的第三方插件」的删除操作。返回是否删除成功。
func unregisterPlugin(name string) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	p, ok := reg.plugins[name]
	if !ok {
		return false
	}
	if p.Builtin {
		return false // 内置插件不可删除
	}
	delete(reg.plugins, name)
	delete(reg.apply, name)
	for i, n := range reg.order {
		if n == name {
			reg.order = append(reg.order[:i], reg.order[i+1:]...)
			break
		}
	}
	return true
}

// getPluginConfig 返回某插件的配置 map（nil 安全）。
func getPluginConfig(name string) map[string]string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if p, ok := reg.plugins[name]; ok && p.Config != nil {
		out := make(map[string]string, len(p.Config))
		for k, v := range p.Config {
			out[k] = v
		}
		return out
	}
	return map[string]string{}
}

// applyPluginState 应用单个插件状态（启停+配置）。必须按 name 定位注册表
// 原对象——传快照副本修改不会生效。
func applyPluginState(name string, enabled bool, cfg map[string]string) {
	reg.mu.Lock()
	p, ok := reg.plugins[name]
	if !ok {
		reg.mu.Unlock()
		return
	}
	p.Enabled = enabled
	if cfg != nil {
		if p.Config == nil {
			p.Config = map[string]string{}
		}
		for k, v := range cfg {
			p.Config[k] = v
		}
	}
	fn := reg.apply[name]
	reg.mu.Unlock()
	if fn != nil {
		fn(name, enabled, cfg)
	}
}

// loadPluginsFromDB 启动时从设置库恢复全部插件启停/配置。
// 兼容迁移：旧键 sources_enabled（刮削源开关）与 ffmpeg_path 首次迁移到 plugins。
// 同时会把 DB 中已保存但尚未注册的第三方插件（导入的）重新注册，保证重启后仍存在。
func loadPluginsFromDB(cfg *cstore.Config, configDir string) {
	if cfg == nil {
		return
	}
	type record struct {
		Enabled *bool             `json:"enabled"`
		Config  map[string]string `json:"config,omitempty"`
		Name    string            `json:"name,omitempty"`
		Label   string            `json:"label,omitempty"`
		Kind    string            `json:"kind,omitempty"`
		Version string            `json:"version,omitempty"`
		Hint    string            `json:"hint,omitempty"`
		Fields  []PluginField     `json:"fields,omitempty"`
		ZipName string            `json:"zipName,omitempty"`
		Entries map[string]string `json:"entries,omitempty"`
	}
	var all = map[string]record{}
	if raw, ok := cfg.Get("plugins"); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &all)
	}
	// 旧键迁移：sources_enabled -> plugins
	if len(all) == 0 {
		if raw, ok := cfg.Get("sources_enabled"); ok && raw != "" {
			var old map[string]bool
			if json.Unmarshal([]byte(raw), &old) == nil {
				for n, on := range old {
					all["scrape."+n] = record{Enabled: &on}
				}
			}
		}
		if p, ok := cfg.Get("ffmpeg_path"); ok && p != "" {
			all["tool.ffmpeg"] = record{Config: map[string]string{"path": p}}
		}
	}
	// 恢复 DB 中已保存但尚未注册的第三方导入插件（含完整声明）。
	for name, r := range all {
		if _, ok := reg.plugins[name]; ok {
			continue
		}
		if r.Name == "" || r.Label == "" || r.Kind == "" {
			continue
		}
		kind := PluginKind(r.Kind)
		switch kind {
		case PluginScrape, PluginTool, PluginDownload, PluginProtocol:
		default:
			continue
		}
		enabled := r.Enabled != nil && *r.Enabled
		registerPlugin(&Plugin{
			Name:    name,
			Label:   r.Label,
			Kind:    kind,
			Enabled: enabled,
			Version: r.Version,
			Hint:    r.Hint,
			Fields:  r.Fields,
			ZipName: r.ZipName,
			Entries: r.Entries,
			// 解压目录按 插件名 命名（与导入时一致），重启后据此重建，
			// 使插件入口可用、删除时可清理。
			ZipDir: pluginsRoot(cfg, configDir) + string(os.PathSeparator) + name,
		}, nil)
	}
	for _, p := range pluginsSnapshot() {
		r, ok := all[p.Name]
		if !ok {
			continue
		}
		enabled := p.Enabled
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		if p.Builtin { // 内置不可停用（如 auto）
			enabled = true
		}
		// 用户要求：所有不需要 Key 的刮削源始终开启（无论旧配置）；
		// 需 Key 的源保持按配置（默认关闭，配置 Key 后开启）。
		if p.Kind == PluginScrape && !p.NeedsKey && !p.Builtin {
			enabled = true
		}
		applyPluginState(p.Name, enabled, r.Config)
	}
}

// savePluginsToDB 把当前插件启停/配置全量持久化到 SQLite（plugins 键），
// 并同步写回旧键（sources_enabled / ffmpeg_path）保持向后兼容。
func savePluginsToDB(cfg *cstore.Config) {
	if cfg == nil {
		return
	}
	out := map[string]any{}
	srcMap := map[string]bool{}
	ffPath := ""
	for _, p := range pluginsSnapshot() {
		rec := map[string]any{
			"enabled": p.Enabled,
			"name":    p.Name,
			"label":   p.Label,
			"kind":    string(p.Kind),
		}
		if p.Version != "" {
			rec["version"] = p.Version
		}
		if p.Hint != "" {
			rec["hint"] = p.Hint
		}
		if len(p.Fields) > 0 {
			rec["fields"] = p.Fields
		}
		if p.ZipName != "" {
			rec["zipName"] = p.ZipName
		}
		if len(p.Entries) > 0 {
			rec["entries"] = p.Entries
		}
		if len(p.Config) > 0 {
			rec["config"] = p.Config
		}
		out[p.Name] = rec
		if p.Kind == PluginScrape {
			srcMap[p.Name[len("scrape."):]] = p.Enabled
		}
		if p.Name == "tool.ffmpeg" {
			ffPath = p.Config["path"]
		}
	}
	if b, err := json.Marshal(out); err == nil {
		_ = cfg.Set("plugins", string(b))
	}
	if b, err := json.Marshal(srcMap); err == nil {
		_ = cfg.Set("sources_enabled", string(b))
	}
	if ffPath != "" {
		_ = cfg.Set("ffmpeg_path", ffPath)
	}
}
