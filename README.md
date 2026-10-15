# LNH-MusicTag

> **自托管 · 全功能 · 双语** 音乐库标签管理 Web 工具 — 纯 Docker 部署，浏览器后台完成所有操作。
> **Self-hosted, fully-featured, bilingual (中文/English)** music library tag manager — pure Docker deployment, everything done in the browser.

<div align="center">

![License](https://img.shields.io/badge/license-MIT-green)
![Language](https://img.shields.io/badge/Go-1.22-blue)
![Frontend](https://img.shields.io/badge/Vue-3-brightgreen)
[![Docker](https://img.shields.io/badge/Docker-ghcr.io-blue?logo=docker)](https://github.com/lahuolou/lnh-musictag/pkgs/container/lnh-musictag)

</div>

---

## English

### What is it

**LNH-MusicTag** is a self-hosted web application for managing your music library's tags: **tag read/write, duplicate detection, multi-source metadata & lyrics scraping, one-key full pipeline, batch fixing, format conversion, async scanning and audio-content identification** — all operated from the browser, no local CLI.

Built with **Go (go-taglib WASM wrapper of TagLib, no CGo) + pure-Go SHA256 dedup + Chromaprint audio fingerprinting + 17 scraping sources + lyrics + Vue 3 frontend**. All config (account, password, API keys, plugin settings) is stored in **SQLite** — no environment variables required.

### Docker image

```
ghcr.io/lahuolou/lnh-musictag:latest
```

The image is built automatically from the `main` branch of the GitHub repository `lahuolou/lnh-musictag` (GitHub Actions): multi-stage build → static binary on an alpine runtime (CA certificates + Chromaprint included, architecture auto-matched).

> **FFmpeg is now an optional plugin** — the image no longer bundles it. To use format conversion, either (1) mount the host's `/usr/bin` into the container and set the path in **Settings → FFmpeg Plugin**, or (2) click "Install" in the same panel (runs `apk add ffmpeg` inside the container, needs root).

### Quick deploy

#### Option A: docker compose (recommended)

```yaml
# docker-compose.yml
services:
  lnh-musictag:
    image: ghcr.io/lahuolou/lnh-musictag:latest
    container_name: lnh-musictag
    restart: unless-stopped
    ports:
      - "10248:10248"
    environment:
      - LNH_ADMIN_USER=admin        # optional; random password generated on first run if omitted
      - LNH_ADMIN_PASS=yourpassword # optional
      # - ACOUSTID_API_KEY=your_key # optional, for AcoustID fingerprint lookup
      - LNH_INSTALL_FFMPEG=false    # true to auto-install ffmpeg in the container on startup (format conversion)
    volumes:
      - /mnt/sata3-1/mp3:/music     # <- your music directory
      - ./config:/config            # SQLite DB + settings persist here
```

```bash
docker compose up -d
# open http://<host>:10248
```

#### Option B: docker run

```bash
docker run -d --name lnh-musictag --restart unless-stopped \
  -p 10248:10248 \
  -e LNH_ADMIN_USER=admin -e LNH_ADMIN_PASS=yourpassword \
  -v /mnt/sata3-1/mp3:/music \
  -v /config:/config \
  ghcr.io/lahuolou/lnh-musictag:latest
```

#### Option C: 1Panel

1. **App Store → Compose → Create** → paste the compose file above.
2. Adjust the music path bind mount (`/mnt/sata3-1/mp3:/music`), the port and credentials.
3. Start; the UI is at `http://<host>:10248`.

> Default in-app music path is `/music` (the container-side mount). You can change it later in **Settings**.

### Features

| # | Feature | Notes |
|---|---------|-------|
| 1 | Tag read/write | MP3/FLAC/M4A/OGG/WAV/WMA… via TagLib (WASM, no CGo); **29 elements**: title/artist/album/album artist/genre/year/track no./track total/composer/lyricist/language/album type/comment/disc no./disc total/lyrics + read-only audio properties (bitrate/sample rate/bit depth/channels/duration/file size/path/hash/MBID…) |
| 2 | Editable panel | Which fields appear in the edit panel is configurable in **Settings → Edit panel fields**; **12 common fields checked by default** (cover/title/artist/album/album artist/genre/year/track/track total/composer/lyricist/lyrics), one-click restore defaults |
| 3 | Async progressive scan | Tracks appear while scanning; incremental, no full reload; pause/resume; known files smart-skipped (no hashing during scan) |
| 4 | Duplicate detection | SHA256 identical files + same-name multi-format (keeps best quality) + same artist+title; user checks & deletes manually (moved to trash, never permanent); **concurrent hashing (8 workers, cancellable, per-file timeout)** — no more "stuck at 0/N" on slow/network disks |
| 5 | **Concurrent multi-source scraping** | **All enabled sources are queried in parallel and results merged**; the best match is picked across sources (title similarity + album completeness) before writing tags; batch/one-key/lyrics/cover/search all share this path |
| 6 | Lyrics | NetEase (3s short timeout + retry) → QQ → lrclib multi-level fallback, **independent of search hits**; get/edit/embed; batch included; external .lrc import |
| 7 | Batch scrape | Selectable fields (cover/title/artist/genre/year/album artist/track/lyrics…, 28+cover), smart-skip filled tracks, background job with live progress, list updates incrementally; three-state messages (cover failed / smart-skip / no data) |
| 8 | **One-key pipeline** | Check tracks and run the whole flow in one click: **auto-dedup → mojibake fix → Traditional→Simplified → filename artist fill → multi-source scrape**; the progress bar shows the **current stage** (dedup hashes / fix-encoding / script convert / scrape element stage) with per-file names |
| 9 | Audio-content identify | Untagged/un-named tracks (e.g. `track01.mp3`) matched by Chromaprint fingerprint against the library; optional external AcoustID lookup (set API key in Settings) |
| 10 | File ops | Rename-to-tag (`Artist - Title.ext`), strip bad/duplicate extensions by real codec sniff (`发如雪.mp3.flac → 发如雪.flac`), format conversion (ffmpeg plugin, optional), mojibake repair (GBK/UTF-8, **covers all 11 text elements incl. lyricist/language/album type**), Simplified⇄Traditional (OpenCC, **scrape results auto-converted to Simplified**, same full-element coverage), artist>3 → "合唱" |
| 11 | List & UX | Sticky table header, filters (**with/without lyrics, with/without cover**, mojibake/traditional/no-artist), real-time search, paginated rendering, **cached cover loading** (mtime-keyed cache, ~2ms repeat hits, 0 broken images), mobile responsive |
| 12 | Theme | Dark/light modern flat UI, language toggle 中/EN |
| 13 | Security | Salted password hashing (auto-migrates legacy plaintext), login rate limiting, CSRF guard, security headers (CSP/nosniff/X-Frame), request size limits, parameterized SQL, no shell command injection |
| 14 | Slim job API | `/api/jobs/{id}` returns only failed items + summary counts (588-track job: 170KB → 263B); lyrics fetched on demand via `GET /api/track/{id}` |
| 15 | Plugin system | Built-in plugins (scrape sources / FFmpeg / AcoustID) are toggle-only & undeletable; **3rd-party plugins import as ZIP packages** (extract & ready, `plugin.json` + executable entry, cross-platform, JSON run protocol, persistent after restart); **re-importing the same name upgrades in place and keeps your saved config** (Cookie/Key untouched); **plugin cards are collapsible** on the Plugins page; generic plugin API reserves `download` / `protocol` kinds |

### Scraping sources

**Domestic (no key, enabled by default):** NetEase Cloud, QQ Music, Kugou, Kuwo, Migu, Qishui, 5sing, Bilibili, Baidu (legacy)
**International (no key, enabled by default):** MusicBrainz, iTunes/Apple Music, Deezer, ListenBrainz, AcoustID (needs key for lookup)
**International (free key, **disabled by default** — enable after filling the key in Settings → Database config):** Last.fm, Discogs, Jamendo, Spotify

> Key-based sources start **off** by default to avoid failed lookups; the Plugins page shows a 🔑 badge and points you to the Settings page to fill in the key.
> With `source=auto`, **all enabled sources are queried concurrently and merged** — the edit-panel search shows hits from every source (e.g. searching a song returns 网易云/QQ/酷我/咪咕/iTunes/Deezer/MusicBrainz… side by side).

### UI layout

- **Public home page** (`/`): brand + feature highlights + login entry. No login required.
- **Admin area**: everything after login — music library list / edit / batch / dedup / settings / plugins.
- Key-based scrape sources start **off** by default; the Plugins page shows a 🔑 badge and points to Settings → Database config to fill in the key.

### Update check

- **How it works**: compares the built-in version with the latest release version (`VERSION` file, updated on every release).
- **Sources** (first success wins): GitHub Releases API → jsDelivr CDN (`VERSION`) → raw.githubusercontent (`VERSION`).
- **Three outcomes**: newer version → popup reminder (auto check once after login; manual check always re-queries); up to date → toast; check failed → toast (no longer falsely reports "up to date").
- Manual "Check for updates" forces a real-time re-query; auto check (login/refresh) reads a 30-minute cached snapshot.

### API overview

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/login` `/api/logout` | Auth (session cookie) |
| POST | `/api/scan` `/api/scan/pause` | Start / pause scan |
| GET | `/api/scan/status` `/api/tracks` `/api/duplicates` | Scan status / track list / dup groups |
| POST | `/api/tracks/delete` `/api/duplicates/remove` | Delete selected files (to trash) |
| GET | `/api/tracks/{id}/cover` `/api/tracks/{id}/lrcfile` `/api/track/{id}` | Cover (cached) / external LRC / full track detail |
| POST | `/api/tracks/{id}/tags` `/api/tracks/{id}/scrape` `/api/tracks/{id}/cover` | Save tags / scrape / clear cover |
| POST | `/api/scrape/batch` `/api/scrape/onekey` | Batch scrape / **one-key pipeline** (background) |
| GET | `/api/scrape/jobs/{id}` `/api/scrape/search` `/api/scrape/sources` | Job progress (slim, with **stage progress**) / concurrent search / sources |
| GET | `/api/lyrics` | Fetch lyrics (multi-level fallback) |
| POST | `/api/convert` `/api/fix-encoding` `/api/convert-script` `/api/identify` | Convert / fix mojibake / script convert / identify |
| GET/POST | `/api/plugins` | List / toggle / configure all plugins |
| POST | `/api/plugins/import` · DELETE `/api/plugins/{name}` · POST `/api/plugins/{name}/run` | Import ZIP/JSON plugin (overwrite keeps config) · delete · invoke |
| GET | `/api/ffmpeg/status` · POST `/api/ffmpeg/install` | FFmpeg probe · one-click install |
| GET/POST | `/api/settings` | Read/write settings (incl. `editFields` for the edit panel) |
| POST | `/api/change-password` | Change password |

### Tech stack

- **Backend**: Go 1.22, `go.senan.xyz/taglib` (WASM), `modernc.org/sqlite` (pure Go), Chromaprint (fpcalc), ffmpeg (optional for conversion)
- **Frontend**: Vue 3 + Vite (light runtime, ~170 KB JS, `go:embed` into the single static binary)
- **Runtime**: Docker multi-stage (node → Go → scratch), no node dependency at runtime

### Development

```bash
cd web && npm install && npm run build   # build frontend into web/dist
go build -o lnh .                        # embed frontend + build binary
```

### License

[MIT](./LICENSE) © 2026 lahuolou

---

## 中文

### 项目简介

**LNH-MusicTag** 是一款自托管的音乐库标签管理 Web 工具：**标签读写、歌曲去重、多源元数据/歌词刮削、一键刮削、批量修正、格式转换、异步扫描、音频内容识别**，全部操作通过浏览器后台完成，无需本地命令行。

技术栈：**Go 后端（go-taglib WASM 封装 TagLib，无 CGo）+ 纯 Go SHA256 去重 + Chromaprint 音频指纹识别 + 17 个刮削源 + 歌词 + Vue3 前端**；账号、密码、API Key、插件配置等全部存 **SQLite**（不依赖环境变量）。

前端采用 **Vue 3 + Vite**，构建产物约 170KB JS，`go:embed` 进单个静态二进制；Docker 多阶段构建（node → Go → scratch），运行时无 node 依赖、不额外占内存。

### Docker 镜像

```
ghcr.io/lahuolou/lnh-musictag:latest
```

镜像由 GitHub 仓库 `lahuolou/lnh-musictag` 的 `main` 分支自动构建推送（GitHub Actions）：多阶段构建 → 静态二进制 + alpine 运行层（内置 CA 证书与 Chromaprint，按目标机架构自动适配）。

> **FFmpeg 已改为可选插件**——镜像不再捆绑。需要「格式转换」时：① 把宿主机 `/usr/bin` 挂载进容器并在「设置 → FFmpeg 插件」填路径；② 或在同一面板点「一键安装」（容器内执行 `apk add ffmpeg`，需 root 权限）。

### 快速部署

#### 方式一：docker compose（推荐）

```yaml
# docker-compose.yml
services:
  lnh-musictag:
    image: ghcr.io/lahuolou/lnh-musictag:latest
    container_name: lnh-musictag
    restart: unless-stopped
    ports:
      - "10248:10248"
    environment:
      - LNH_ADMIN_USER=admin        # 可选；不填则首次启动自动生成随机密码
      - LNH_ADMIN_PASS=你的密码       # 可选
      # - ACOUSTID_API_KEY=你的Key   # 可选，用于 AcoustID 听声识曲
      - LNH_INSTALL_FFMPEG=false    # true 则容器启动时自动安装 ffmpeg（格式转换用）
    volumes:
      - /mnt/sata3-1/mp3:/music     # <- 改成你的真实音乐目录
      - ./config:/config            # SQLite 数据库与配置持久化
```

```bash
docker compose up -d
# 浏览器打开 http://<主机IP>:10248
```

#### 方式二：docker run（单条命令）

```bash
docker run -d --name lnh-musictag --restart unless-stopped \
  -p 10248:10248 \
  -e LNH_ADMIN_USER=admin -e LNH_ADMIN_PASS=你的密码 \
  -v /mnt/sata3-1/mp3:/music \
  -v /config:/config \
  ghcr.io/lahuolou/lnh-musictag:latest
```

#### 方式三：1Panel

1. **应用商店 → Compose → 创建**，粘贴上方 compose 内容；
2. 调整音乐目录挂载（`/mnt/sata3-1/mp3:/music`）、端口与账号密码；
3. 启动后访问 `http://<主机IP>:10248`。

> 应用内默认音乐目录为容器内 `/music`（即挂载点），可在「设置」页修改。

### 功能特性

| # | 能力 | 说明 |
|---|------|------|
| 1 | 标签读写 | MP3/FLAC/M4A/OGG/WAV/WMA…（TagLib WASM，无 CGo）：**29 个元素**——歌名/艺术家/专辑/专辑艺术家/流派/年份/曲目号/总曲目数/作曲家/作词/语言/专辑类型/简介/碟号/总碟数/歌词 + 只读音频属性（比特率/采样率/位深度/声道/时长/文件大小/路径/哈希/MBID…） |
| 2 | 可配置编辑面板 | 「设置 → 编辑面板显示字段」自定义显示哪些元素，**默认勾选 12 个常用项**（封面/歌名/艺术家/专辑/专辑艺术家/风格/年代/曲目号/总曲目数/作曲家/作词/歌词），一键恢复默认 |
| 3 | 异步渐进扫描 | 边扫边出、增量入库、不整表刷新；可暂停/继续；已知文件智能跳过（扫描不哈希，速度快） |
| 4 | 歌曲去重 | SHA256 内容相同 + 同名多格式（保留音质最佳）+ 艺术家+标题相同；人工勾选删除（**移入回收站**，绝不永久删除）；**并发哈希（8 线程、可取消、单文件超时兜底）**——慢盘/网络挂载下不再「卡在 0/N」 |
| 5 | **多源并发刮削** | **全部启用源同时查询、结果合并**，写入前跨源挑最佳匹配（标题相似度 + 专辑完整性）；批量/一键/歌词/海报/搜索全部共用此链路 |
| 6 | 歌词 | 网易云（3s 短超时+重试）→ QQ → lrclib 多级兜底，**不依赖搜索是否命中**；单曲获取/编辑/嵌入；批量可选；外挂 .lrc 一键导入 |
| 7 | 批量补全 | 字段可选（28 字段+封面）、智能跳过已填曲目、后台运行、进度实时可见、列表增量更新；**三态消息**（封面失败/智能跳过/无可用数据） |
| 8 | **一键刮削** | 勾选曲目一键跑完整流水线：**自动去重 → 乱码修复 → 繁体转简体 → 文件名补艺术家 → 多源刮削**；进度条显示**当前阶段**进度（去重哈希数/乱码修复/简繁转换/刮削元素各自计数 + 当前文件名），取消即时生效 |
| 9 | 音频内容识别 | 无标签/无文件名信息的曲目（如 `track01.mp3`）按 Chromaprint 指纹与库内已标注曲目匹配；可选外部 AcoustID 听声识曲（设置页填 Key） |
| 10 | 文件处理 | 保存即重命名（`艺术家 - 标题.后缀`）；按真实编码去重复/错误后缀（`发如雪.mp3.flac → 发如雪.flac`）；格式转换（ffmpeg 可选插件）；乱码修复（GBK/UTF-8，**覆盖全部 11 个文本元素，含作词/语言/专辑类型**）；简繁体转换（OpenCC，**刮削结果统一转简体**，同样全元素覆盖）；艺术家>3 位 → 一键改「合唱」 |
| 11 | 列表与体验 | 表头冻结；筛选（**有歌词/无歌词、有海报/无海报**、乱码/繁体/无艺术家）；输入即搜歌名；分页渲染不卡；**封面缓存加载**（mtime 失效缓存，重复请求 ~2ms，0 坏图）；手机横竖屏适配 |
| 12 | 主题与语言 | 黑/白现代扁平主题；界面中英双语一键切换 |
| 13 | 安全加固 | 密码加盐哈希存储（旧明文自动迁移）、登录失败限流、CSRF 防护、安全响应头（CSP/nosniff/X-Frame）、请求体大小限制、SQL 全参数化、命令无注入风险 |
| 14 | 任务接口瘦身 | `/api/jobs/{id}` 只返回失败项 + summary 计数（588 首任务 170KB → 263B）；歌词正文经 `GET /api/track/{id}` 按需拉取 |
| 15 | 插件系统 | 内置插件（刮削源 / FFmpeg / AcoustID）只有开关、不可删除；**第三方插件以 ZIP 插件包导入**（解压即用：plugin.json + 可执行入口、跨平台、JSON 调用协议、重启保留）；**重复导入同名插件 = 覆盖升级，已填配置（Cookie/Key）原样保留**；**插件卡片可折叠**（配置区默认收起，点卡片头展开）；通用插件接口预留 download/protocol 两类 |

### 刮削源清单

**国内源（免 Key，默认开启）：** 网易云、QQ 音乐、酷狗、酷我、咪咕、汽水、5sing、哔哩哔哩、百度（旧接口）
**国际源（免 Key，默认开启）：** MusicBrainz、iTunes/Apple Music、Deezer、ListenBrainz、AcoustID（查指纹需 Key）
**国际源（需免费 Key，**默认关闭**——在「设置 → 数据库配置」填好 Key 后再开启）：** Last.fm、Discogs、Jamendo、Spotify

> 需 Key 的源默认关闭，避免无效请求；插件页会显示 🔑 徽标并提示去设置页填写 Key。
> `source=auto` 时**全部启用源并发查询并合并**——编辑面板一次搜索即可看到每个源的命中（如搜一首歌同时返回网易云/QQ/酷我/咪咕/iTunes/Deezer/MusicBrainz…），跨源对比后挑最佳写入。

### 界面结构

- **公开首页**（`/`）：品牌 + 功能亮点 + 登录入口，无需登录。
- **后台区**：登录后全部管理功能——音乐列表 / 编辑 / 批量 / 去重 / 设置 / 插件。
- 需 API Key 的刮削源默认关闭；插件页显示 🔑 徽标并引导去「设置 → 数据库配置」填写 Key。

### 更新检查

- **更新依据**：对比内置版本号与仓库最新 Release 版本号（`VERSION` 文件，随每次发布更新）。
- **查询源**（任一成功即用）：GitHub Releases API → jsDelivr CDN（`VERSION`）→ raw.githubusercontent（`VERSION`）。大陆网络下 GitHub API 常不通，自动走 CDN 源。
- **三态结果**：有新版 → 弹窗提醒（登录后自动检查一次，手动检查每次都查）；已是最新 → 提示；查询失败 → 提示（不再误报"已是最新"）。
- 手动「检查更新」会强制实时重查；自动检查（登录/刷新）读 30 分钟缓存快照。

### API 一览

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/login` `/api/logout` | 登录 / 登出（会话 Cookie） |
| POST | `/api/scan` `/api/scan/pause` | 开始 / 暂停扫描 |
| GET | `/api/scan/status` `/api/tracks` `/api/duplicates` | 扫描状态 / 曲目列表 / 重复分组 |
| POST | `/api/tracks/delete` `/api/duplicates/remove` | 删除选中文件（进回收站） |
| GET | `/api/tracks/{id}/cover` `/api/tracks/{id}/lrcfile` `/api/track/{id}` | 封面（缓存）/ 外挂 LRC / 单曲完整详情 |
| POST | `/api/tracks/{id}/tags` `/api/tracks/{id}/scrape` `/api/tracks/{id}/cover` | 保存标签 / 刮削 / 清除封面 |
| POST | `/api/scrape/batch` `/api/scrape/onekey` | 批量刮削 / **一键刮削流水线**（后台任务） |
| GET | `/api/scrape/jobs/{id}` `/api/scrape/search` `/api/scrape/sources` | 任务进度（瘦身，含**阶段进度**）/ 并发搜索 / 源列表 |
| GET | `/api/lyrics` | 获取歌词（多级兜底） |
| POST | `/api/convert` `/api/fix-encoding` `/api/convert-script` `/api/identify` | 转换 / 乱码修复 / 简繁转换 / 音频识别 |
| GET/POST | `/api/plugins` | 插件列表 / 启停与配置 |
| POST | `/api/plugins/import` · DELETE `/api/plugins/{name}` · POST `/api/plugins/{name}/run` | 导入 ZIP/JSON 插件（同名覆盖保留配置）· 删除 · 调用 |
| GET | `/api/ffmpeg/status` · POST `/api/ffmpeg/install` | FFmpeg 探测 · 容器内一键安装 |
| GET/POST | `/api/settings` | 读写配置（含编辑面板字段 `editFields`） |
| POST | `/api/change-password` | 修改密码 |

### 技术栈

- **后端**：Go 1.22、`go.senan.xyz/taglib`（WASM）、`modernc.org/sqlite`（纯 Go）、Chromaprint（fpcalc）、ffmpeg（格式转换，可选）
- **前端**：Vue 3 + Vite（运行时轻量，约 170KB JS，`go:embed` 进单个静态二进制）
- **运行时**：Docker 多阶段（node → Go → alpine），运行时无 node 依赖；FFmpeg 可选安装

### 本地开发

```bash
cd web && npm install && npm run build   # 构建前端到 web/dist
go build -o lnh .                        # 嵌入前端并构建后端二进制
```

### 开源协议

[MIT](./LICENSE) © 2026 lahuolou
