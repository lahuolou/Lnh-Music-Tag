# 开发文档

面向在本仓库本地构建、调试、扩展 Lnh-Music-Tag 的开发者。

## 1. 技术栈

- **后端**：Go 1.27，`CGO_ENABLED=0` 纯静态编译。SQLite 使用 `modernc.org/sqlite`（纯 Go 驱动）；音频标签读写走 `go.senan.xyz/taglib`（WASM 封装，无 CGo）；简繁转换用 `github.com/liuzl/gocc`；HTTP 用标准库 `net/http`（Go 1.22+ 方法+路由模式）。
- **前端**：Vue 3 + Vite（仅依赖 `vue`），构建产物 `web/dist` 通过 `go:embed` 内嵌进单二进制，服务端无独立静态目录。
- **运行镜像**：多阶段 Dockerfile（node:20-alpine 建前端 → golang:1.27-alpine 构建 → alpine:3.20 运行，预装 `ca-certificates`、`chromaprint/fpcalc`，FFmpeg 改为可选）。

## 2. 目录结构

```
.
├── main.go               # 装配、HTTP 路由表、四协议挂载、会话/CSRF/限流中间件
├── auth.go               # Web 会话登录、改密、/api/me
├── tracks_db.go         # SQLite 曲目库：扫描入库、查询、收藏、歌单持久化
├── streaming.go         # 直传 / 按需转码（FFmpeg）、Range、封面、歌词
├── subsonic.go          # Subsonic / OpenSubsonic 适配（/rest）
├── navidrome.go         # Navidrome 风格 REST（/auth、/api）
├── media_servers.go      # Jellyfin 与 Emby JSON 适配（根路径 /jellyfin /emby）
├── plugin.go            # 插件注册表、快照、门控（available/enabled 分离）
├── plugins_api.go       # 内置插件注册、GET/POST/import/delete/run/panel API
├── plugin_runner.go     # 第三方 ZIP 插件解压、安全校验、exec 运行器
├── convert.go           # 格式转换（FFmpeg 批量任务）
├── identify.go          # AcoustID 指纹识别任务
├── fixencoding.go       # 乱码（GBK/拉丁误码）修复
├── zhscript.go           # 简繁转换
├── renameclean.go       # 文件名清理
├── scrapejob.go          # 批量 / 一键刮削任务、进度，及 fuseMetadata 多源按字段融合
├── pipeline.go / activity.go / security.go / helpers.go / static.go / version.go
├── internal/
│   ├── model/           # 曲目/专辑/艺人/歌单等领域模型
│   ├── store/           # SQLite 配置与持久化
│   ├── scrape/          # 各刮削源实现 + MultiSource 聚合 + 歌词
│   ├── dedup/          # 重复检测
│   ├── finger/         # 音频指纹（AcoustID）
│   ├── audiofmt/       # 格式探测与转码参数
│   └── taglibx/        # 标签读写封装
├── web/
│   ├── src/App.vue、views/（Home/Login/Plugin/Settings）、components/
│   └── src/i18n.js、api.js、store.js、fields.js
├── scripts/gateway-smoke.py   # 只读协议冒烟脚本
├── docs/                 # 本文档、协议矩阵、插件指南
└── Dockerfile / entrypoint.sh / docker-compose.yml
```

## 3. 本地构建

前置：Go 1.27.x、Node.js 20+、npm。

```bash
# 1. 构建前端（产出 web/dist，go:embed 需要）
cd web
npm ci
npm run build
cd ..

# 2. 后端检查与构建
go test -count=1 ./...
go vet ./...
gofmt -l .        # 期望无输出
go build -o lnh-music-tag .
```

Windows 本地二进制直接 `go build -o lnh-music-tag.exe .`。

开发模式前端可单独 `npm run dev`（Vite），但后端 `/` 提供的是内嵌 `web/dist`；改前端后需重新 `npm run build` 再编译后端，或用 Vite 代理到运行中的后端调试。

## 4. 测试与自检

```bash
go test -count=1 ./...      # 单元测试（乱码样本、去重、指纹、FFmpeg 探测等）
go vet ./...
```

服务启动并扫描曲目后，跑只读协议冒烟（不写标签、不改收藏）：

```bash
LNH_TEST_USER=admin LNH_TEST_PASS='密码' \
  python3 scripts/gateway-smoke.py --base http://127.0.0.1:10248
```

冒烟只验证认证、列表、封面与 Range 流可达，**不能替代真实播放器实机测试**；协议能力边界以 [STREAMING_GATEWAY.md](STREAMING_GATEWAY.md) 为准。

## 5. 配置与环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `LNH_PORT` | `:10248` | 监听端口 |
| `LNH_CONFIG_DIR` | `/config` | SQLite 目录（`lnh.db`，含账号/配置/插件/歌单） |
| `LNH_ADMIN_USER` / `LNH_ADMIN_PASS` | `admin` / 随机 | 仅首次启动种子，之后以数据库为准 |
| `LNH_STREAM_PASS` | 空 | 流媒体密码（Subsonic token、ND 登录） |
| `LNH_INSTALL_FFMPEG` | 关 | 容器启动自动装 FFmpeg |

运行期配置（扫描目录、自动修标题/重命名、编辑字段、FFmpeg 路径、插件开关与 Key）都落 SQLite，重启保留。

## 6. 扩展点

- **新增刮削源**：在 `internal/scrape/` 实现 `Source` 接口并在 `NewMultiSource` 注册；需 Key 的源在 `plugins_api.go` 的 `keyedSources` / `keyedSourceFields` 登记。
- **第三方工具插件**：按 [PLUGINS.md](PLUGINS.md) 打 ZIP（`plugin.json` + `entries` + 可选 `panel`），插件页导入。
- **新增协议端点**：在对应适配层（`subsonic.go` / `navidrome.go` / `media_servers.go`）按现有模式实现，并同步更新协议矩阵文档。

## 7. 版本与发布约定

- 唯一版本源是根目录 `VERSION`（三段语义化 `主.次.修订`，如 `1.0.0`），编译期 `embed` 为 `AppVersion()`；版本比较按数值逐段、缺段补零。
- CI（`.github/workflows/docker.yml`）：push `main` 打 `latest`；push tag `v<VERSION>` 时先校验 **tag 去掉 v 后必须与 VERSION 逐字符相等**，再构建推送 GHCR。
- **本地修改阶段不要打 tag、不要 push**；发布由维护者确认后执行。

## 8. 安全注意

- 单用户自托管模型，Jellyfin/Emby 在本地单用户模式下宽松放行；暴露公网前必须收紧认证并上 HTTPS。
- 后台 `/api/` 一律走会话 + CSRF；请求体有上限；扫描目录做 `filepath.Clean` 防路径遍历；ZIP 插件防 zip-slip、压缩炸弹与符号链接逃逸。
- 日志不记录完整 query、认证头或登录正文；API Key 下发前端时脱敏。
