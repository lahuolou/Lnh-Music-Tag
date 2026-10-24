// 通用插件框架：统一管理「刮削源 / 工具」两类插件。
//   - 插件声明：名称、展示名、类型、配置字段、是否内置、是否可安装；
//   - 启停与配置：通过 /api/plugins 统一读写，持久化到 SQLite（plugins 键）；
//   - 扩展点：后续音源、自动换源策略等插件都按
//     同一套注册表接入，前端设置页按类型自动渲染，无需改页面。
package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
)

// PluginKind 插件类型。
type PluginKind string

const (
	PluginScrape PluginKind = "scrape" // 刮削/音源（元数据、歌词、封面）
	PluginTool   PluginKind = "tool"   // 工具（FFmpeg 等）
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
	Name          string     `json:"name"`  // 唯一标识，如 scrape.qq / tool.ffmpeg
	Label         string     `json:"label"` // 展示名
	Kind          PluginKind `json:"kind"`
	Enabled       bool       `json:"enabled"`
	Builtin       bool       `json:"builtin"`            // 内置不可删除；auto 等不可停用
	Installable   bool       `json:"installable"`        // 支持一键安装（容器内执行）
	NeedsKey      bool       `json:"needsKey"`           // 依赖外部 API Key（默认关闭，配置后开启）
	KeyField      string     `json:"keyField,omitempty"` // 对应设置库 Key 键名
	KeyConfigured bool       `json:"keyConfigured"`      // 当前 Key 是否已配置（快照时由 API 层计算）
	Version       string     `json:"version,omitempty"`
	APIVersion    int        `json:"apiVersion,omitempty"` // 插件宿主协议版本；第三方插件当前支持 v1
	Hint          string     `json:"hint,omitempty"`
	// Available 由 API 层按当前环境实时计算（不持久化）：依赖是否就绪。
	// false 时前端禁用开关并展示 Unavailable 原因，避免"开着但用不了"的死状态。
	Available   bool              `json:"available"`
	Unavailable string            `json:"unavailable,omitempty"`
	Fields      []PluginField     `json:"fields,omitempty"`
	Config      map[string]string `json:"config"` // 始终输出（可能为空 {}），避免前端渲染崩溃
	// —— ZIP 插件包字段（第三方上传导入，解压即用）——
	ZipDir  string            `json:"-"`                 // 解压目录（绝对路径，仅宿主内部使用）
	Entries map[string]string `json:"entries,omitempty"` // 平台(linux/windows/darwin) -> zip 内相对入口
	ZipName string            `json:"zipName,omitempty"` // 插件包文件名（导入时上传的原文件名）
	Panel   string            `json:"panel,omitempty"`   // Web 面板入口（zip 内相对路径，如 ui/index.html）；有则主界面出现插件 tab
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

// replacePlugin 覆盖注册（覆盖导入用）：替换已存在第三方插件的元数据，
// 保留注册顺序；apply 为空时沿用旧回调（内置刮削源的 SetEnabled 等不得丢失）。
func replacePlugin(p *Plugin, apply applyFn) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, ok := reg.plugins[p.Name]; !ok {
		reg.plugins[p.Name] = p
		reg.order = append(reg.order, p.Name)
		reg.apply[p.Name] = apply
		return
	}
	reg.plugins[p.Name] = p
	if apply != nil {
		reg.apply[p.Name] = apply
	}
}

// pluginsSnapshot 返回全部插件快照（按注册顺序，深拷贝，防外部篡改）。
// pluginsSnapshot 返回插件注册快照（深拷贝，避免调用方改动注册状态）。
// API Key 类字段脱敏：已配置的 Key 只回传占位符，避免密码明文下发前端。
const keyRedacted = "********"

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
				if v != "" && isKeyField(p, k) {
					np.Config[k] = keyRedacted
				} else {
					np.Config[k] = v
				}
			}
		} else {
			np.Config = map[string]string{} // 保证前端始终拿到非空 config，避免渲染崩溃
		}
		out = append(out, np)
	}
	return out
}

// pluginsRawSnapshot 返回插件注册快照（不做 Key 脱敏，保留明文），
// 仅用于持久化（savePluginsToDB）与恢复链路，避免脱敏占位符写回存储。
func pluginsRawSnapshot() []Plugin {
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
			np.Config = map[string]string{}
		}
		out = append(out, np)
	}
	return out
}

// isKeyField 判断某配置键是否属于该插件的 API Key 字段。
func isKeyField(p *Plugin, k string) bool {
	for _, kf := range pluginKeyFields(p) {
		if k == kf {
			return true
		}
	}
	return false
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

// pluginOn 返回某插件在内存中是否被用户开启（仅读状态，不探测依赖）。
// 依赖是否就绪由调用方结合 Available/探测结果另行判断。
func pluginOn(name string) bool {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if p, ok := reg.plugins[name]; ok {
		return p.Enabled
	}
	return false
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
func loadPluginsFromDB(cfg *cstore.Config, configDir string, ms *scrape.MultiSource) {
	if cfg == nil {
		return
	}
	type record struct {
		Enabled  *bool             `json:"enabled"`
		Config   map[string]string `json:"config,omitempty"`
		Name     string            `json:"name,omitempty"`
		Label    string            `json:"label,omitempty"`
		Kind     string            `json:"kind,omitempty"`
		Version  string            `json:"version,omitempty"`
		Hint     string            `json:"hint,omitempty"`
		Fields   []PluginField     `json:"fields,omitempty"`
		KeyField string            `json:"keyField,omitempty"`
		ZipName  string            `json:"zipName,omitempty"`
		Entries  map[string]string `json:"entries,omitempty"`
		Panel    string            `json:"panel,omitempty"`
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
		case PluginScrape, PluginTool:
		default:
			continue
		}
		enabled := r.Enabled != nil && *r.Enabled
		registerPlugin(&Plugin{
			Name:     name,
			Label:    r.Label,
			Kind:     kind,
			Enabled:  enabled,
			Version:  r.Version,
			Hint:     r.Hint,
			Fields:   r.Fields,
			KeyField: r.KeyField,
			ZipName:  r.ZipName,
			Entries:  r.Entries,
			Panel:    r.Panel,
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
		// 仅 auto（内置兜底源）不可停用；其余内置/第三方插件按持久化状态
		// 恢复——用户在插件页的开关应跨重启生效。
		if p.Name == "scrape.auto" {
			enabled = true
		}
		// 启动就绪门控：持久化开着但依赖此刻不可用（ffmpeg 未装、Key 未配）
		// → 强制关闭，避免重启后"开着却用不了"。
		switch p.Name {
		case "tool.ffmpeg":
			if ffmpegBin(cfg) == "" {
				enabled = false
			}
		case "tool.acoustid":
			key := r.Config["acoustid_key"]
			if key == "" {
				key, _ = cfg.Get("acoustid_key")
			}
			if strings.TrimSpace(key) == "" {
				enabled = false
			}
		}
		applyPluginState(p.Name, enabled, r.Config)
		// Key 字段同步回设置库（cstore）：重启后 AcoustID/刮削源使用点
		// （cfg.Get 读取）与插件页 KeyConfigured 状态保持一致。
		for _, k := range pluginKeyFields(&p) {
			if v, ok := r.Config[k]; ok && v != "" && v != keyRedacted {
				cfg.Set(k, v)
			}
		}
	}
	// Key 同步到内存刮削客户端（与设置页保存行为一致）
	syncClientKeys(cfg, ms)
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
	for _, p := range pluginsRawSnapshot() {
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
		if p.KeyField != "" {
			rec["keyField"] = p.KeyField
		}
		if p.ZipName != "" {
			rec["zipName"] = p.ZipName
		}
		if p.Panel != "" {
			rec["panel"] = p.Panel
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
