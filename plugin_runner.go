// 第三方 ZIP 插件运行器：上传插件包（zip）→ 解压到配置目录 plugins/<name>/
// → 校验 plugin.json 声明 → 注册进插件注册表。宿主通过固定 JSON 协议
// exec 插件入口二进制（run --input in.json --output out.json），
// 实现「导入即解压、解压即可调用」。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	cstore "lnh-musictag/internal/store"
)

const (
	pluginZipMaxSize  = 256 << 20 // 插件包上限 256MiB
	pluginRunTimeout  = 120 * time.Second
	pluginZipFileName = "plugin.json"
)

// pluginZipDecl 是插件包内 plugin.json 的声明格式。
type pluginZipDecl struct {
	Name    string            `json:"name"`
	Label   string            `json:"label"`
	Kind    string            `json:"kind"`
	Version string            `json:"version,omitempty"`
	Hint    string            `json:"hint,omitempty"`
	Fields  []PluginField     `json:"fields,omitempty"`
	KeyField string           `json:"keyField,omitempty"`
	Entries map[string]string `json:"entries"`         // 平台 -> 相对入口（bin/xxx）
	Panel   string            `json:"panel,omitempty"` // Web 面板入口（zip 内相对路径，如 ui/index.html）
}

// pluginsRoot 返回插件包存放根目录（与配置库同级）。
func pluginsRoot(cfg *cstore.Config, configDir string) string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "plugins")
}

// importPluginZip 校验并解压 zip 插件包，注册后返回插件。
// 安全约束：解压目标限定在 root/<name>/ 内（防 zip-slip）；
// entry 必须是 zip 内相对路径，禁止绝对路径与 .. 穿越。
func importPluginZip(cfg *cstore.Config, configDir string, data []byte, zipName string) (*Plugin, error) {
	if len(data) == 0 || len(data) > pluginZipMaxSize {
		return nil, errors.New("插件包为空或超过 256MiB 上限")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("无法解析 zip 插件包: " + err.Error())
	}
	// 1. 读取 plugin.json
	var decl pluginZipDecl
	found := false
	for _, f := range zr.File {
		if strings.TrimPrefix(f.Name, "./") == pluginZipFileName {
			rc, err := f.Open()
			if err != nil {
				return nil, errors.New("读取 plugin.json: " + err.Error())
			}
			b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				return nil, errors.New("读取 plugin.json: " + err.Error())
			}
			if err := json.Unmarshal(b, &decl); err != nil {
				return nil, errors.New("plugin.json 格式错误: " + err.Error())
			}
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("插件包缺少 plugin.json")
	}
	// 2. 校验声明
	if decl.Name == "" || decl.Label == "" {
		return nil, errors.New("plugin.json 缺少 name/label")
	}
	kind := PluginKind(decl.Kind)
	switch kind {
	case PluginScrape, PluginTool, PluginDownload, PluginProtocol:
	default:
		return nil, errors.New("非法插件类型 kind: " + decl.Kind + "（可选 scrape/tool/download/protocol）")
	}
	if len(decl.Entries) == 0 {
		return nil, errors.New("plugin.json 缺少 entries（平台 -> 入口文件映射）")
	}
	// 3. 冲突检查：内置插件不可覆盖；已导入的第三方插件允许覆盖升级，
	//    覆盖时保留用户已填的配置（Config 不变），只更新插件定义。
	oldConfig := map[string]string{}
	if old, ok := reg.plugins[decl.Name]; ok {
		if old.Builtin {
			return nil, errors.New("内置插件不可覆盖: " + decl.Name)
		}
		oldConfig = old.Config
	}
	// 4. 解压到 root/<name>/
	root := pluginsRoot(cfg, configDir)
	if root == "" {
		return nil, errors.New("未配置插件存储目录")
	}
	dest := filepath.Join(root, decl.Name)
	cleanDest := filepath.Clean(dest)
	if err := os.RemoveAll(cleanDest); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cleanDest, 0o755); err != nil {
		return nil, err
	}
	// 总解压字节上限（防 zip bomb：压缩包很小但解压量巨大）
	const maxExtractTotal = 512 << 20
	var extracted int64
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		// zip-slip 防护：目标必须仍在 cleanDest 内
		target := filepath.Join(cleanDest, filepath.FromSlash(name))
		if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(os.PathSeparator)) &&
			filepath.Clean(target) != cleanDest {
			os.RemoveAll(cleanDest)
			return nil, errors.New("插件包包含非法路径: " + f.Name)
		}
		mode := f.Mode()
		// 拒绝符号链接与设备等非普通文件条目（防符号链接逃逸/异常文件）
		if mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 || mode&os.ModeNamedPipe != 0 {
			os.RemoveAll(cleanDest)
			return nil, errors.New("插件包包含不支持的文件类型: " + f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				os.RemoveAll(cleanDest)
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			os.RemoveAll(cleanDest)
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			os.RemoveAll(cleanDest)
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			os.RemoveAll(cleanDest)
			return nil, err
		}
		n, err := io.Copy(out, io.LimitReader(rc, pluginZipMaxSize))
		out.Close()
		rc.Close()
		if err != nil {
			os.RemoveAll(cleanDest)
			return nil, err
		}
		extracted += n
		if extracted > maxExtractTotal {
			os.RemoveAll(cleanDest)
			return nil, errors.New("插件包解压总量超过 512MiB 上限（疑似压缩炸弹）")
		}
	}
	// 5. 校验当前平台 entry 存在
	if rel, ok := decl.Entries[runtime.GOOS]; ok && rel != "" {
		ep := filepath.Join(cleanDest, filepath.FromSlash(rel))
		fi, err := os.Stat(ep)
		if err != nil || fi.IsDir() {
			os.RemoveAll(cleanDest)
			return nil, errors.New("插件入口文件不存在: " + rel)
		}
	}
	// 5b. 校验 Web 面板入口存在（panel 可选；声明了就必须有文件）
	if decl.Panel != "" {
		pp := filepath.Join(cleanDest, filepath.FromSlash(decl.Panel))
		fi, err := os.Stat(pp)
		if err != nil || fi.IsDir() {
			os.RemoveAll(cleanDest)
			return nil, errors.New("插件面板文件不存在: " + decl.Panel)
		}
	}
	// 6. 注册（覆盖导入时替换旧定义，保留原配置）
	p := &Plugin{
		Name:    decl.Name,
		Label:   decl.Label,
		Kind:    kind,
		Enabled: true,
		Version: decl.Version,
		Hint:    decl.Hint,
		Fields:  decl.Fields,
		KeyField: decl.KeyField,
		ZipDir:  cleanDest,
		Entries: decl.Entries,
		ZipName: zipName,
		Panel:   decl.Panel,
		Config:  oldConfig,
	}
	replacePlugin(p, nil)
	savePluginsToDB(cfg)
	return p, nil
}

// pluginEntryPath 返回当前平台可执行入口的绝对路径（不存在返回空串）。
func pluginEntryPath(p *Plugin) string {
	if p == nil || p.ZipDir == "" || len(p.Entries) == 0 {
		return ""
	}
	rel, ok := p.Entries[runtime.GOOS]
	if !ok || rel == "" {
		return ""
	}
	return filepath.Join(p.ZipDir, filepath.FromSlash(rel))
}

// runPluginEntry 通过固定 JSON 协议调用第三方插件入口：
//
//	<entry> run --input <in.json> --output <out.json>
//
// in.json 为 {op, args...} 透传；out.json 为插件返回的任意 JSON 对象。
func runPluginEntry(p *Plugin, op string, args map[string]any) (map[string]any, error) {
	if p == nil {
		return nil, errors.New("插件不存在")
	}
	if !p.Enabled {
		return nil, errors.New("插件未启用: " + p.Label)
	}
	bin := pluginEntryPath(p)
	if bin == "" {
		return nil, errors.New("插件不支持当前平台 " + runtime.GOOS + "（" + p.Label + "）")
	}
	fi, err := os.Stat(bin)
	if err != nil || fi.IsDir() {
		return nil, errors.New("插件入口不可用: " + bin)
	}
	workDir, err := os.MkdirTemp("", "lnh-plugin-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)
	inPath := filepath.Join(workDir, "in.json")
	outPath := filepath.Join(workDir, "out.json")
	in := map[string]any{"op": op}
	for k, v := range args {
		in[k] = v
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(inPath, b, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginRunTimeout)
	defer cancel()
	// exec.Command 直接执行二进制，不经过 shell，固定参数、无用户输入拼接（防注入）
	cmd := exec.CommandContext(ctx, bin, "run", "--input", inPath, "--output", outPath)
	if err := cmd.Run(); err != nil {
		return nil, errors.New("插件执行失败: " + err.Error())
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, errors.New("插件未输出结果")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, errors.New("插件输出格式错误")
	}
	if ok, _ := res["ok"].(bool); !ok {
		msg, _ := res["error"].(string)
		return res, errors.New(msg)
	}
	return res, nil
}

// deletePluginZip 删除插件包目录（解压产物），供卸载时清理。
func deletePluginZip(p *Plugin) {
	if p != nil && p.ZipDir != "" {
		_ = os.RemoveAll(p.ZipDir)
	}
}
