# Lnh-Music-Tag

自托管的本地音乐库标签管理工具，内置一套面向第三方播放器的多协议音乐网关。通过浏览器整理本地音频的标签、封面与歌词，并让支持 Subsonic、Navidrome、Jellyfin、Emby 协议的播放器直接读取和播放同一份曲库。

> 当前版本见根目录 [`VERSION`](VERSION)（三段语义化 `主.次.修订`）。流媒体网关是协议**适配子集**，不是完整的 Subsonic / Navidrome / Jellyfin / Emby 服务器，能力边界以 [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md) 为准。

技术栈：Go 1.27（纯 Go，SQLite 使用 modernc.org/sqlite，标签读写走 WASM 封装的 taglib，无 CGO 依赖）+ Vue 3 / Vite（构建产物经 `go:embed` 内嵌进单二进制）。

## 功能概览

- **曲库扫描与浏览**：异步、可暂停地扫描本地目录， progressive 入库；左侧曲目树支持搜索与按「乱码 / 繁体 / 无歌词 / 无海报 / 待识别」筛选，扫描结果持久化到 SQLite，重启不丢。
- **标签编辑**：在右侧编辑面板修改标题、艺术家、专辑、年份、音轨号、流派等字段，管理内嵌封面与外挂/内嵌歌词，写回音频文件。
- **批量处理**：批量补全标签、乱码（GBK/拉丁误码）修复、繁简转换、文件名清理、重复曲目检测与删除。
- **多源刮削**：聚合国内（网易云、QQ、酷狗、酷我、咪咕、B 站、汽水、波点、5sing）与海外（MusicBrainz、iTunes、ListenBrainz、Deezer、Last.fm、Spotify、Discogs、Jamendo）音源补全元数据与封面；需 Key 的源在插件页配置后启用。
- **音频识别**：可选 AcoustID 指纹识别，为存疑曲目匹配元数据。
- **格式转换**：可选 FFmpeg，把曲目批量转码为其他格式。
- **插件机制**：内置刮削源、FFmpeg、AcoustID、音乐网关以插件形式管理；支持导入第三方 ZIP 插件包（带 Web 面板）。
- **音乐网关**：同一端口提供 Subsonic/OpenSubsonic、Navidrome 风格 REST、Jellyfin、Emby 四种兼容接口，复用扫描曲库。
- **双语界面**：中文 / 英文，明暗主题。

## 部署

### Docker Compose（推荐）

```yaml
services:
  lnh-music-tag:
    image: ghcr.io/lahuolou/lnh-music-tag:latest
    container_name: lnh-music-tag
    restart: unless-stopped
    ports:
      - "10248:10248"
    volumes:
      - /你的音乐目录:/music        # 应用会把标签/封面写回文件，必须可读写
      - ./config:/config           # SQLite 配置、密码、第三方插件包持久化
    environment:
      - LNH_ADMIN_USER=admin       # 仅首次启动的管理员用户名种子
      - LNH_ADMIN_PASS=请设置强密码 # 留空则首次启动生成随机密码并打印到日志
      - LNH_INSTALL_FFMPEG=false   # true 时容器启动自动 apk 安装 FFmpeg（需 root）
```

```bash
docker compose up -d
```

访问 `http://<服务器IP>:10248`。首次未设 `LNH_ADMIN_PASS` 时，随机初始密码打印在容器日志中，登录后到「设置」修改。

### 本地构建

环境要求：Go 1.27.x、Node.js 20+、npm。

```bash
cd web
npm ci
npm run build
cd ..
go test ./...
go vet ./...
go build -o lnh-music-tag .
```

Docker 构建会在镜像内重新构建前端并嵌入：

```bash
docker build -t lnh-music-tag:local .
```

### 环境变量（均为可选种子/覆盖）

| 变量 | 默认 | 说明 |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP 监听端口 |
| `LNH_CONFIG_DIR` | `/config` | SQLite 配置目录（`lnh.db`） |
| `LNH_ADMIN_USER` / `LNH_ADMIN_PASS` | `admin` / 随机 | 仅首次启动写入管理员账号；之后以数据库为准 |
| `LNH_STREAM_PASS` | 空 | 播放器流媒体密码；Subsonic token 认证与 Navidrome 登录建议设置 |
| `LNH_INSTALL_FFMPEG` | 关 | 容器启动时自动安装 FFmpeg |

## 接入第三方播放器（同一端口）

先在 Web 界面「扫描」加入音乐目录，再在播放器里按下表填写：

| 服务类型 | 基础地址 | 认证 |
|---|---|---|
| Subsonic / OpenSubsonic | `http://<IP>:10248/rest` | 管理员用户名；明文密码可用管理员密码或流媒体密码，token 模式需流媒体密码 |
| Navidrome 兼容 | `http://<IP>:10248` | `/auth/login`，用户名=管理员，密码=流媒体密码（未配则用管理员密码） |
| Jellyfin | `http://<IP>:10248` | 本地单用户模式，`/Users/AuthenticateByName` 放行 |
| Emby | `http://<IP>:10248/emby` | 同上 |

完整端点覆盖与未实现项见 [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md)。

## FFmpeg 与 AcoustID 插件门控

- **FFmpeg（`tool.ffmpeg`）**：服务「格式转换」批量任务与网关按需实时转码（播放器请求 `format=mp3/aac/opus` 或 Emby HLS）。插件页按「配置路径 → PATH → 配置卷」探测二进制；探测不到时开关自动回显关闭并禁用，保存开启被拒绝。**关闭/未安装不影响原始文件直传、下载、浏览封面与歌单**，此时转码自动回退直传。
- **AcoustID（`tool.acoustid`）**：在线音频指纹识别，必须先配 API Key；未配 Key 时开关禁用。它只用于存疑曲目识别，不参与播放。

## 安全提示

当前为**单用户自托管**模型，Jellyfin/Emby 认证在本地单用户模式下宽松放行；暴露到公网前应收紧认证、使用 HTTPS，并修改默认/随机密码。日志不记录完整 query、认证头或登录正文。

## 自检

服务启动并已扫描曲目后：

```bash
LNH_TEST_USER=admin LNH_TEST_PASS='你的密码' \
  python3 scripts/gateway-smoke.py --base http://127.0.0.1:10248
```

脚本只做只读的认证、列表、封面与 Range 流检查，不写标签、不改收藏。它不能替代真实播放器的实机测试。

## 文档

- [开发者文档](docs/DEVELOPMENT.md)：结构、构建、测试、发布约定。
- [流媒体网关协议矩阵](docs/STREAMING_GATEWAY.md)：四协议端点、认证、转码回退与未实现项。
- [插件开发指南](docs/PLUGINS.md)：第三方 ZIP 插件格式、生命周期与门控。
- [更新日志](CHANGELOG.md)：版本变更记录。

## License

MIT — see [LICENSE](LICENSE).

---

## English

Lnh-Music-Tag is a self-hosted music-tag manager with a built-in multi-protocol streaming gateway. Edit tags, embedded covers and lyrics for a local library from the browser, and serve the same library to Subsonic, Navidrome-style, Jellyfin and Emby clients over a single port. The gateway is a protocol subset, not a full media server. See Docker Compose above for deployment and [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md) for exact protocol coverage.
