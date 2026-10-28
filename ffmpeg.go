// FFmpeg 插件化：不再把 ffmpeg 当作硬依赖捆绑。
//   - 路径可配置：设置页指定绝对路径，优先于 PATH 探测；
//   - 可选安装：容器内（需 root）可一键安装（apk/apt/yum）；
//   - 持久化安装：安装完成后把二进制与动态库复制到配置卷
//     <configDir>/ffmpeg/，容器更新重建后无需重新安装；
//   - 未就绪时格式转换明确报错并引导去设置页配置。
package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cstore "lnh-music-tag/internal/store"
)

// ffmpegBin 返回可用的 ffmpeg 可执行文件路径：优先插件配置
// （tool.ffmpeg.config.path），其次设置库旧键 ffmpeg_path，然后 PATH 探测，
// 最后回退到配置卷中的持久化安装（容器更新重建后系统层丢失、/config 保留）。
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
	if err == nil {
		return p
	}
	wrapper := filepath.Join(ffmpegPersistDir(), "ffmpeg")
	if st, err := os.Stat(wrapper); err == nil && !st.IsDir() {
		return wrapper
	}
	return ""
}

// ffmpegVerTTL 版本探测结果的缓存有效期：探测是低频展示用，10 分钟内
// 不重复执行外部进程（避免每次 /api/ffmpeg/status 都拉起一次 ffmpeg）。
const ffmpegVerTTL = 10 * time.Minute

// ffmpegVerTimeout 单次版本探测超时：异常二进制（权限/动态链接卡死）时
// 3 秒内返回失败而不是永久阻塞调用方。
const ffmpegVerTimeout = 3 * time.Second

var ffVerMu sync.Mutex
var ffVerCache = map[string]ffVerEntry{}

type ffVerEntry struct {
	ver string
	at  time.Time
}

// ffmpegVersion 返回 ffmpeg -version 的第一行（供设置页展示），失败返回空。
// 带超时与 TTL 缓存：探测失败不写缓存（下次重新探测），成功结果 10 分钟
// 内复用——替换二进制后最多 10 分钟自动更新，不会永久固化旧版本。
func ffmpegVersion(bin string) string {
	if bin == "" {
		return ""
	}
	ffVerMu.Lock()
	defer ffVerMu.Unlock()
	if e, ok := ffVerCache[bin]; ok && time.Since(e.at) < ffmpegVerTTL {
		return e.ver
	}
	ctx, cancel := context.WithTimeout(context.Background(), ffmpegVerTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").Output()
	if err != nil {
		return "" // 探测失败不写缓存，下次重新探测
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	ver := strings.TrimSpace(line)
	ffVerCache[bin] = ffVerEntry{ver: ver, at: time.Now()}
	return ver
}

// ffmpegActive 判定转码是否真正可用：插件开关已开启，且探测到可执行文件。
// 插件关闭或未安装时，四协议流/格式转换一律回退直传原始文件，不中断播放。
func ffmpegActive(cfg *cstore.Config) bool {
	return pluginOn("tool.ffmpeg") && ffmpegBin(cfg) != ""
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
// 每步带超时：包管理器在网络异常时可能挂起，不能让其永久阻塞安装流程。
const installTimeout = 5 * time.Minute

func installFFmpeg() (string, error) {
	attempts := [][]string{
		{"sh", "-c", "apk add --no-cache ffmpeg"},
		{"sh", "-c", "apt-get update -qq && apt-get install -y -qq ffmpeg"},
		{"sh", "-c", "yum install -y ffmpeg"},
	}
	var lastErr error
	for _, args := range attempts {
		ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		cancel()
		if err == nil {
			return string(out), nil
		}
		lastErr = err
	}
	return "", lastErr
}

// ffmpegPersistDir 返回持久化安装目录（配置卷下 ffmpeg/）。
func ffmpegPersistDir() string {
	if d := os.Getenv("LNH_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "ffmpeg")
	}
	return filepath.Join("/config", "ffmpeg")
}

// persistFFmpeg 把容器内已安装的 ffmpeg 二进制与动态库复制到配置卷，
// 生成 wrapper 脚本并把插件 path 指向它。之后更新镜像重建容器时，
// 系统层安装会丢失，但 /config 卷保留——ffmpegBin 的持久化回退直接可用。
func persistFFmpeg(cfg *cstore.Config) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	// 解析动态库依赖；ldd 失败（非 Linux/静态二进制）时仍复制二进制，
	// wrapper 仅设置 LD_LIBRARY_PATH，系统库环境不受影响。
	var libs []string
	if out, err := exec.Command("ldd", bin).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			i := strings.Index(line, "=> ")
			if i < 0 {
				continue
			}
			path := strings.TrimSpace(line[i+3:])
			if j := strings.Index(path, " "); j >= 0 {
				path = path[:j]
			}
			// 跳过动态加载器：其路径写死在 ELF 头，系统层始终存在
			base := filepath.Base(path)
			if path == "" || strings.Contains(base, "ld-musl") || strings.HasPrefix(base, "ld-linux") {
				continue
			}
			libs = append(libs, path)
		}
	}
	return persistFFmpegTo(bin, libs, ffmpegPersistDir(), cfg)
}

// persistFFmpegTo 是持久化的纯文件操作部分（可独立测试）。
func persistFFmpegTo(bin string, libs []string, dir string, cfg *cstore.Config) error {
	binDir := filepath.Join(dir, "bin")
	libDir := filepath.Join(dir, "lib")
	for _, d := range []string{binDir, libDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	dstBin := filepath.Join(binDir, "ffmpeg")
	if err := copyFileMode(bin, dstBin, 0o755); err != nil {
		return err
	}
	for _, l := range libs {
		if st, err := os.Stat(l); err == nil && !st.IsDir() {
			if err := copyFileMode(l, filepath.Join(libDir, filepath.Base(l)), 0o644); err != nil {
				log.Printf("ffmpeg: persist lib %s failed: %v", l, err)
			}
		}
	}
	wrapper := filepath.Join(dir, "ffmpeg")
	w := "#!/bin/sh\n" +
		"LD_LIBRARY_PATH=\"" + shDQ(libDir) + "\"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\n" +
		"export LD_LIBRARY_PATH\n" +
		"exec \"" + shDQ(dstBin) + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(w), 0o755); err != nil {
		return err
	}
	// 插件 path 指向 wrapper：容器重建后启动探测自动认领，无需重新安装
	applyPluginState("tool.ffmpeg", true, map[string]string{"path": wrapper})
	savePluginsToDB(cfg)
	return nil
}

// copyFileMode 复制文件并设置权限。
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// shDQ 转义 shell 双引号字符串中的特殊字符（用于生成 wrapper 脚本）。
// sh 双引号内仅 "$ ` \ 后接特殊字符时有转义语义；路径中的普通反斜杠
// （非 Linux 场景）会被原样保留，无需处理。
func shDQ(s string) string {
	r := strings.NewReplacer(`"`, `\"`, "$", `\$`, "`", "\\`")
	return r.Replace(s)
}

// uninstallFFmpeg 卸载 FFmpeg 工具插件：清空配置路径并停用插件，
// 同时清理配置卷中的持久化安装产物。
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
		ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		cancel()
		if err == nil {
			_ = out
			break
		}
	}
	// 清理持久化安装产物（配置卷），下次需要时重新安装
	os.RemoveAll(ffmpegPersistDir())
	// 清空配置路径并停用插件（持久化）
	applyPluginState("tool.ffmpeg", false, map[string]string{"path": ""})
	savePluginsToDB(cfg)
	if cfg != nil {
		cfg.Set("ffmpeg_path", "") // 兼容旧设置键
	}
	return ""
}
