// FFmpeg 插件化：不再把 ffmpeg 当作硬依赖捆绑。
//   - 路径可配置：设置页指定绝对路径，优先于 PATH 探测；
//   - 可选安装：容器内（需 root）可一键安装（apk/apt/yum）；
//   - 未就绪时格式转换明确报错并引导去设置页配置。
package main

import (
	"os"
	"os/exec"
	"strings"

	cstore "lnh-musictag/internal/store"
)

// ffmpegBin 返回可用的 ffmpeg 可执行文件路径：优先插件配置
// （tool.ffmpeg.config.path），其次设置库旧键 ffmpeg_path，最后 PATH 探测。
// 未找到返回空串。
func ffmpegBin(cfg *cstore.Config) string {
	if p := getPluginConfig("tool.ffmpeg")["path"]; p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if cfg != nil {
		if p, ok := cfg.Get("ffmpeg_path"); ok && p != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return p
}

// ffmpegVersion 返回 ffmpeg -version 的第一行（供设置页展示），失败返回空。
func ffmpegVersion(bin string) string {
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(line)
}

// ffmpegStatus 汇总当前状态供 /api/ffmpeg/status 使用。
func ffmpegStatus(cfg *cstore.Config) map[string]any {
	bin := ffmpegBin(cfg)
	return map[string]any{
		"available": bin != "",
		"path":      bin,
		"version":   ffmpegVersion(bin),
		"configured": func() string {
			if cfg == nil {
				return ""
			}
			p, _ := cfg.Get("ffmpeg_path")
			return p
		}(),
	}
}

// installFFmpeg 尝试在容器内安装 ffmpeg（仅固定白名单命令，无注入面）。
// 按发行版依次尝试 apk（Alpine）→ apt（Debian/Ubuntu）→ yum（RHEL）。
func installFFmpeg() (string, error) {
	attempts := [][]string{
		{"sh", "-c", "apk add --no-cache ffmpeg"},
		{"sh", "-c", "apt-get update -qq && apt-get install -y -qq ffmpeg"},
		{"sh", "-c", "yum install -y ffmpeg"},
	}
	var lastErr error
	for _, args := range attempts {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err == nil {
			return string(out), nil
		}
		lastErr = err
	}
	return "", lastErr
}

// uninstallFFmpeg 卸载 FFmpeg 工具插件：清空配置路径并停用插件。
// 容器内一键安装过的会尝试包管理器移除；宿主机/系统自带（Windows 无 sh 等）
// 不删除文件（避免误删系统组件），仅清空配置并停用插件。
func uninstallFFmpeg(cfg *cstore.Config) string {
	// 容器场景：按发行版尝试包管理器卸载（白名单命令）
	attempts := [][]string{
		{"sh", "-c", "apk del ffmpeg"},
		{"sh", "-c", "apt-get remove -y -qq ffmpeg"},
		{"sh", "-c", "yum remove -y ffmpeg"},
	}
	for _, args := range attempts {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err == nil {
			_ = out
			break
		}
	}
	// 清空配置路径并停用插件（持久化）
	applyPluginState("tool.ffmpeg", false, map[string]string{"path": ""})
	savePluginsToDB(cfg)
	if cfg != nil {
		cfg.Set("ffmpeg_path", "") // 兼容旧设置键
	}
	return ""
}
