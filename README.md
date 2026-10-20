# Lnh-Music-Tag

自托管的**多源音乐元数据刮削与标签整理工具**：把本地目录里标题缺失、乱码、无封面、无歌词的曲目，通过聚合十几个国内外音源自动匹配并补全标题、艺术家、专辑、年份、流派、曲目号、封面与歌词，一键写回音频文件。内置一套可选的多协议音乐网关，让 Subsonic / Navidrome / Jellyfin / Emby 播放器直接读取同一份曲库。

> 当前版本见根目录 [`VERSION`](VERSION)（三段语义化 `主.次.修订`）。流媒体网关是协议**适配子集**，不是完整的 Subsonic / Navidrome / Jellyfin / Emby 服务器，能力边界以 [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md) 为准。

技术栈：Go 1.27（纯 Go，SQLite 使用 modernc.org/sqlite，标签读写走 WASM 封装的 taglib，无 CGO 依赖）+ Vue 3 / Vite（构建产物经 `go:embed` 内嵌进单二进制）。

## 多源刮削（核心）

### 内置音源

刮削源以插件形式注册在 Web「插件」页，可单独启停。`auto` 是始终启用的聚合伪源。

| 分组 | 音源 | 是否需要 API Key |
|---|---|---|
| 国内（免 Key） | 网易云、QQ 音乐、酷狗、酷我、咪咕、B 站、汽水音乐、波点、5sing | 否 |
| 海外（免 Key） | iTunes、MusicBrainz、ListenBrainz、Deezer | 否 |
| 海外（需 Key） | Last.fm、Spotify、Discogs、Jamendo | 是，配好后参与自动刮削 |

### 匹配与聚合

- **语言路由**：`auto` 模式下，含中文的查询先走国内链、再回落海外；拉丁字母查询先走海外链、再回落国内。
- **并发聚合**：批量刮削并发查询全部启用源并合并命中，单源挂起或限流不会阻塞整条链。
- **按字段融合（多源择优）**：不再整条采用某一个源。先对每个候选做标题归一化（小写、去空白、去掉伴奏/现场等括号变体）并用 Dice 二元组相似度评分，再让每个 ID3 字段独立从候选中择优——标题取匹配度最高（≥0.35 阈值，低于阈值不乱写），艺术家/专辑/年代/曲目号/作曲/作词/语言等取匹配度高且非占位（Unknown/未知/Various）的完整值，流派跨源去重合并，专辑艺术家优先取 MusicBrainz；同分并列时完整度（非空字段数）高者优先。
- **详情补全**：对 MusicBrainz 候选先拉取发行详情（专辑艺术家、流派、发行日期、语言、专辑类型、碟数与曲目号）再参与融合。

### 封面与歌词

- **封面回退链**：先用命中源取封面，失败则按偏好源→语言链逐源尝试；完整「标题+艺术家」全失败后，用纯标题再查一轮（更易命中 iTunes / MusicBrainz）。下载带国内 CDN 的 Referer 防盗链头，并校验响应确实是图片（拦截 HTML 错误页/坏链），失败自动重试。
- **歌词链路**：网易云 / QQ 按源歌曲 ID 取；取不到走 QQ、网易搜索兜底；海外曲目最终落到 lrclib。

### 音频指纹识别（可选）

配置 AcoustID API Key 后，存疑曲目（Unknown、乱码、缺艺术家）可先做音频指纹识别，再回写元数据；未配 Key 时该功能关闭，不影响普通刮削。

## 批量整理

在扫描结果上勾选曲目后，可按需要组合执行：

- **一键补全**：批量刮削并写回勾选的字段（标题、艺术家、专辑、专辑艺术家、年份、流派、曲目号等）。
- **乱码修复**：修正 GBK / 拉丁误码导致的标题、艺术家乱码。
- **繁简转换**：统一繁简字。
- **文件重命名**：按「艺术家 - 标题.后缀」规范命名。
- **去重**：检测并删除重复曲目。
- **格式转换**（可选 FFmpeg）：批量转码为其他格式。

## 曲库扫描与浏览

- 异步、可暂停地扫描本地目录，progressive 入库，结果持久化到 SQLite，重启不丢。
- 左侧曲目树支持搜索，并按「乱码 / 繁体 / 无歌词 / 无海报 / 待识别」快速筛选。
- 右侧编辑面板可手动修改标题、艺术家、专辑、年份、音轨号、流派，管理内嵌封面与外挂/内嵌歌词并写回文件。

## 附带：四协议播放网关

同一端口提供 Subsonic/OpenSubsonic、Navidrome 风格 REST、Jellyfin、Emby 四种兼容接口，复用已扫描曲库，供第三方播放器在线播放、下载与浏览封面/歌单。完整端点覆盖与未实现项见 [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md)。

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
| `LNH_STREAM_PASS` | 空 | 四协议播放器连接的**唯一密码**；未配置时播放器一律无法连接 |
| `LNH_INSTALL_FFMPEG` | 关 | 容器启动时自动安装 FFmpeg |

## 接入第三方播放器（同一端口，可选）

先在 Web 界面「扫描」加入音乐目录，再在播放器里按下表填写：

四个协议统一只填同一个**流媒体密码**（插件「音乐统一网关」或 `LNH_STREAM_PASS`），用户名任意，不使用 Web 管理员密码。

| 服务类型 | 基础地址 | 认证 |
|---|---|---|
| Subsonic / OpenSubsonic | `http://<IP>:10248/rest` | 密码填流媒体密码（明文或 token 模式均可） |
| Navidrome 兼容 | `http://<IP>:10248` | `/auth/login`，密码填流媒体密码 |
| Jellyfin | `http://<IP>:10248` | `/Users/AuthenticateByName`，密码填流媒体密码 |
| Emby | `http://<IP>:10248/emby` | 密码填流媒体密码 |

## 插件门控

- **需 Key 的海外源**（Last.fm / Spotify / Discogs / Jamendo）：未配 Key 时开关禁用，不会参与自动刮削。
- **FFmpeg（`tool.ffmpeg`）**：服务「格式转换」批量任务与网关按需实时转码（播放器请求 `format=mp3/aac/opus` 或 Emby HLS）。插件页按「配置路径 → PATH → 配置卷」探测二进制；探测不到时开关自动回显关闭并禁用，保存开启被拒绝。**关闭/未安装不影响刮削、原始文件直传、下载、浏览封面与歌单**，此时转码自动回退直传。
- **AcoustID（`tool.acoustid`）**：在线音频指纹识别，必须先配 API Key；未配 Key 时开关禁用。它只用于存疑曲目识别，不参与播放。

## 安全提示

当前为**单用户自托管**模型：Web 后台用管理员账号登录，四个流媒体协议统一只认流媒体密码 `stream_pass`（不宽松放行）；暴露到公网前应使用 HTTPS，并分别设置强管理员密码与流媒体密码。日志不记录完整 query、认证头或登录正文。

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

GPL-3.0 — see [LICENSE](LICENSE).

---

## English

Lnh-Music-Tag is a self-hosted multi-source metadata scraper and tag cleaner for your local music library. It matches tracks against a dozen domestic and international sources and fills in title, artist, album, year, genre, track number, cover art and lyrics, writing the results back into the audio files. An optional built-in gateway serves the same library to Subsonic, Navidrome-style, Jellyfin and Emby clients over a single port. The gateway is a protocol subset, not a full media server. See Docker Compose above for deployment and [docs/STREAMING_GATEWAY.md](docs/STREAMING_GATEWAY.md) for exact protocol coverage.

Released under the GPL-3.0 license — see [LICENSE](LICENSE).
