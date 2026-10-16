# LNH-MusicTag 开发文档

版本：v1.6.2（与 `VERSION` 文件同步）｜ 仓库：github.com/lahuolou/lnh-musictag

本文档面向维护者/贡献者，说明项目架构、核心流程、数据模型、API、构建与发布。
插件开发另见 [PLUGINS.md](PLUGINS.md)，用户部署见 [README.md](../README.md)。

---

## 1. 项目概览

LNH-MusicTag 是自托管的音乐库标签管理 Web 工具：标签读写、去重、多源刮削、
一键流水线、批量修正、格式转换、异步扫描、音频内容识别。**单静态二进制**
（前端 `go:embed` 进 Go 程序），Docker 多阶段构建，无运行时依赖。

- **后端**：Go 1.22；TagLib 经 `go.senan.xyz/taglib`（WASM，无 CGo）；
  SQLite 经 `modernc.org/sqlite`（纯 Go，无 CGO）。
- **前端**：Vue 3 + Vite，构建产物 ~170KB JS，嵌入二进制。
- **识别**：Chromaprint `fpcalc`（容器镜像内置）。
- **转换**：FFmpeg 可选插件（镜像不捆绑）。

## 2. 目录与模块边界

```
├── main.go                 程序入口：配置、路由注册、中间件、store 生命周期
├── activity.go             最近活动（日志/操作记录）
├── auth.go / security.go   登录、会话、CSRF、限流、安全响应头
├── plugins_api.go          插件注册表：内置插件注册、启停、配置、导入导出
├── plugin.go / plugin_runner.go  插件框架（Plugin 类型、ZIP 包运行协议）
├── pipeline.go             一键刮削流水线（去重→乱码→简繁→刮削）
├── scrapejob.go            批量刮削后台任务（进度、阶段、取消、恢复）
├── identify.go             音频内容识别（AcoustID → MusicBrainz → 库内匹配）
├── renameclean.go          文件名修正（按真实编码去重复/错误后缀）
├── fixencoding.go          乱码修复（GBK/UTF-8 双候选取优）
├── zhscript.go             简繁体转换（OpenCC）
├── convert.go              FFmpeg 格式转换
├── ffmpeg.go               FFmpeg 插件（探测/安装/持久化到配置卷）
├── tracks_db.go            曲目持久化（tracks 表读写）
├── version.go / static.go  版本检查、前端静态服务（含缓存头）
├── helpers.go              公共工具（JSON 读写、参数解析等）
└── internal/
    ├── model/model.go      Track 统一模型（29 元素 + 属性）
    ├── store/config.go     SQLite 配置读写（settings 表）
    ├── scrape/             17 个刮削源 + 并发调度（Source 接口、SearchWithFallback、pickBest）
    ├── dedup/              并发 SHA256 去重
    ├── finger/             Chromaprint 指纹封装（fpcalc）
    ├── taglibx/            TagLib WASM 封装（标签读写）
    └── audiofmt/           音频格式探测/转换辅助
```

> v1.6.2 起已移除全部协议输出（`/rest`、`/emby`、`/jellyfin`、Navidrome `/api/*`、
> MusicBridge `/api/musicbridge/v1`、`/mb/rest`）及其内置插件与文件，
> 项目为纯标签管理工具，无对外协议入口。

## 3. 核心业务流程

### 3.1 扫描

- 异步协程递归遍历音乐目录，边扫边入库（内存 `store` + SQLite `tracks` 表）。
- 增量扫描：已存在 `byPath` 的路径跳过；已知文件不哈希（快速）。
- 支持暂停/继续；扫描结果持久化，重启后 `loadTracksFromDB` 恢复（按 `seq` 排序）。
- 发现阶段自动设置 `HasTrad / Garbled / NeedsIdentify` 标记（供列表筛选）。

### 3.2 音频内容识别

```
无标签/无文件名曲目 → fpcalc 指纹 → AcoustID 查询（3 QPS，需 Key）
  → 命中 → MusicBrainz Recording 补全（1 QPS）→ 写标签 → 按标签重命名
  → AcoustID 未命中/未配 Key → 回退库内已标注曲目 raw 指纹海明距离匹配
```

- 需转码格式（DSD 等）先经 ffmpeg 转 WAV 再指纹。
- 库内指纹匹配失败时标记「待识别」。

### 3.3 一键刮削流水线（pipeline.go）

```
勾选曲目 → ① 自动去重（可选，SHA256 并发+可取消）
         → ② 乱码修复 → ③ 繁体转简体 → ④ 文件名补艺术家
         → ⑤ 多源并发刮削（全部启用源同时查询，pickBest 挑最佳写入）
```

- 进度条显示**当前阶段**（去重哈希数 / 乱码修复 / 简繁转换 / 刮削元素各自计数）。
- 取消即时生效（阶段内检查取消标志）；结果三态：封面失败 / 智能跳过 / 无数据。

### 3.4 多源并发刮削（v1.5.0 核心）

- `SearchWithFallback(query, langs, exclude...)`：指定源 + 语言优先链全部启用源
  **并发**发请求，结果合并返回；`pickBest` 按标题相似度 + 专辑完整性挑全局最佳。
- 批量 / 一键 / 歌词 / 海报 / 编辑面板搜索共用此链路。
- 每个源独立超时与限速；单个源失败不影响其他源。

### 3.5 去重与删除安全

- 三类重复：SHA256 内容相同、同名多格式（保留音质最佳）、同艺术家+标题。
- 删除**移入回收站**（跨卷自动复制到目标卷回收站），绝不永久删除。
- 并发哈希：8 线程、可取消、单文件超时兜底（慢盘不再卡死）。

### 3.6 歌词链路

网易云（3s 短超时+重试）→ QQ → lrclib 多级兜底，不依赖搜索命中；
单曲获取/编辑/嵌入；批量可选；外挂 .lrc 一键导入。

### 3.7 封面缓存

- `coverCache`：`id → {mod, img}`，按文件 mtime 失效；重复请求 ~2ms，0 坏图。
- 专辑级封面缓存：同专辑曲目复用封面（批量刮削降低源请求峰值）。

### 3.8 任务状态与恢复

- 批量/一键刮削为后台任务（`scrapejob.go`），进度经 `/api/scrape/jobs/{id}` 轮询；
- `/api/jobs/{id}` 只返回失败项 + summary 计数（瘦身接口）；
- 取消/重启后状态清理（重启不残留任务）。

## 4. 数据模型与持久化

### 4.1 SQLite 表（`$LNH_CONFIG_DIR/lnh.db`）

| 表 | 结构 | 用途 |
|----|------|------|
| `settings` | `key TEXT PRIMARY KEY, value TEXT NOT NULL` | 配置键值：`plugins`（插件启停/配置 JSON）、`admin_user`、各 API Key、`editFields` 等 |
| `tracks` | `id TEXT PK, path TEXT NOT NULL, data TEXT(JSON), seq INTEGER` | 曲目持久化，`seq` 保持扫描顺序 |

配置库不可用时退化为纯内存模式（内存为运行期唯一事实源，落库 best-effort）。

### 4.2 Track 主要字段（internal/model）

- 文本标签（17 可编辑）：歌名/艺术家/专辑/专辑艺术家/风格/年代/曲目号/总曲目数/
  作曲家/作词/语言/专辑类型/简介/碟号/总碟数/歌词/封面；
- 属性（12 只读）：文件名/外挂 LRC/比特率/采样率/位深度/声道/时长/文件大小/
  SHA256/路径/MusicBrainz 录音/专辑；
- 派生标记：`HasTrad / Garbled / NeedsIdentify / HasLyrics / HasCover`（列表筛选用）。

### 4.3 数据一致性注意

- 写操作统一入口 `refreshTrack`（保存标签/刮削/重命名/删除后同步内存+落库）。
- 外部删除文件后，重启加载时自动跳过（下次扫描清理）。

## 5. 插件扩展机制

详见 [PLUGINS.md](PLUGINS.md)。要点：

- 插件注册表：内置（`registerBuiltinPlugins`）只开关；第三方 ZIP/JSON 可导入覆盖升级；
- 两类 `kind`：`scrape` / `tool`（`download` / `protocol` 预留类型已随 v1.6.2 移除）；
- 启停与配置持久化在 `settings.plugins`；Key 配置保存脱敏；
- 第三方插件运行协议：`<entry> run --input in.json --output out.json`，无 shell 注入；
- ZIP 包防 zip-slip，解压到 `/config/plugins/<name>/`。

## 6. API 分类索引

统一前缀 `/api`，除登录/版本外均需会话 Cookie；写操作需
`X-Requested-With: LNH-MusicTag` 头（CSRF 防护）。

| 分类 | 端点 |
|------|------|
| 认证 | `POST /api/login` `POST /api/logout` `GET /api/me` `POST /api/change-password` |
| 扫描 | `POST /api/scan` `POST /api/scan/pause` `GET /api/scan/status` |
| 曲目 | `GET /api/tracks` `GET /api/tracks/refresh` `GET /api/track/{id}` `POST /api/tracks/{id}/tags` `POST /api/tracks/delete` |
| 封面/LRC | `GET /api/tracks/{id}/cover` `GET /api/tracks/{id}/lrcfile` `POST /api/tracks/{id}/cover` |
| 刮削 | `POST /api/tracks/{id}/scrape` `POST /api/scrape/batch` `POST /api/scrape/onekey` `GET /api/scrape/jobs/{id}` `GET /api/scrape/search` `GET /api/scrape/sources` `GET /api/lyrics` |
| 后台任务 | `GET /api/jobs` `GET /api/jobs/{id}` `POST /api/jobs/{id}/cancel` |
| 去重 | `GET /api/duplicates` `POST /api/duplicates/remove` |
| 文件处理 | `POST /api/convert` `POST /api/fix-encoding` `POST /api/convert-script` `POST /api/identify` `POST /api/rename-clean` |
| 插件 | `GET/POST /api/plugins` `POST /api/plugins/import` `DELETE /api/plugins/{name}` `POST /api/plugins/{name}/run` `GET /api/plugins/{name}/panel/{path...}` `POST /api/fs/scan-dir` |
| FFmpeg | `GET /api/ffmpeg/status` `POST /api/ffmpeg/install` `POST /api/ffmpeg/uninstall` |
| 设置 | `GET/POST /api/settings` |
| 日志 | `GET /api/logs` `POST /api/logs/delete` `POST /api/logs/clear` |
| 其他 | `GET /api/version`（免认证）`GET /api/version/check` |

## 7. 部署与配置

### 7.1 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `LNH_CONFIG_DIR` | `/config` | 配置目录（lnh.db、admin.json、plugins/、ffmpeg/） |
| `LNH_PORT` | `:10248` | HTTP 监听地址 |
| `LNH_ADMIN_USER` | `admin` | 首次启动管理员种子账号 |
| `LNH_ADMIN_PASS` | 空 | 留空则首启生成随机密码（打印日志） |
| `LNH_INSTALL_FFMPEG` | 空 | `true/1/yes/on` 时 entrypoint 启动前 `apk add ffmpeg` |

> 账号、密码、Key 等**默认存数据库**（`/config/lnh.db`），环境变量仅作首启种子。
> AcoustID / Jamendo / Last.fm / Discogs / Spotify Key 在「插件」页填写（存数据库），
> 环境变量 `ACOUSTID_API_KEY` 已弃用、代码不再读取。

### 7.2 Docker

- 多阶段：node:20-alpine 构建前端 → golang:1.27-alpine 构建静态二进制 → alpine:3.20 运行。
- 运行层内置 `ca-certificates`（HTTPS 刮削源）与 `chromaprint`（fpcalc 识别）。
- 卷：音乐目录（读写）与配置卷；`ENTRYPOINT ["/entrypoint.sh"]`。

### 7.3 构建

```bash
cd web && npm run build          # 前端 → web/dist（go:embed 使用）
go build -o lnh-musictag.exe .   # 后端 + 嵌入前端
go vet ./... && go test ./...    # 静态检查与单测
```

## 8. 开发、测试与发布

### 8.1 本地调试

- 设置 `LNH_CONFIG_DIR` 指向临时目录可隔离配置库；`LNH_PORT` 换端口并行实例。
- 日志：stderr/stdout；`go test` 覆盖去重、指纹、乱码修复、插件框架等。

### 8.2 发布流程

1. 更新 `VERSION` 文件与 `CHANGELOG.md`；
2. 提交并推送 `main`（GitHub Actions 自动构建镜像推 ghcr）；
3. 打 tag `v<版本>`（CI 校验 VERSION == tag 去 v）触发发布；
4. 推送/发布前必须用户本人确认。

### 8.3 发布前检查表

- [ ] `go vet ./...` / `go test ./...` / `npm run build` 全过
- [ ] 双实例冒烟：登录、列表、批量、去重、设置、插件页渲染、console 无业务错误
- [ ] 协议类路由确认 404（本项目已无协议输出）
- [ ] 文档与功能一致（README / DEVELOPMENT / PLUGINS / CHANGELOG）

## 9. 安全与运维

- **安全措施**：密码加盐哈希（旧明文自动迁移）、登录失败限流、CSRF 守卫、
  安全响应头（CSP/nosniff/X-Frame）、请求体大小限制、SQL 参数化、无 shell 注入。
- **插件边界**：运行协议无 shell/环境变量注入；ZIP 防 zip-slip；Key 脱敏；
  内置插件不可覆盖/删除；面板静态服务防路径穿越。
- **故障排查速查**：
  - 首启随机密码：看容器日志（`[LNH] initial admin password`）。
  - FFmpeg 不可用：插件页探测 → 一键安装 → 挂载宿主机 /usr/bin。
  - 识别失败：确认 fpcalc 在镜像内、插件页已填 AcoustID Key。
  - 列表为空：检查音乐目录挂载与「设置」中目录配置。

## 10. 版本记录

详见 [CHANGELOG.md](../CHANGELOG.md)。当前 v1.6.2：移除全部协议输出（Subsonic /
Navidrome / Jellyfin / Emby / MusicBridge）与相关文档章节，清理死代码与数据库残留。
