# 开发文档

本文面向在本地构建、调试、扩展 Lnh-Music-Tag 的开发者，基于当前代码库（v1.0.14）的实际实现编写。

---

## 1. 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| 后端 | Go 1.27（见 `go.mod`） | 纯 Go 单二进制，无 CGO 依赖 |
| 数据库 | `modernc.org/sqlite` | 嵌入式 SQLite（曲目表 / 配置库 / 任务历史 / 操作日志） |
| 标签读写 | `go.senan.xyz/taglib`（WASM 封装） | 读写 ID3v2、FLAC、MP4 等标签 |
| 简繁转换 | `github.com/liuzl/gocc` | 简体 ↔ 繁体批量转换 |
| 前端 | Vue 3 + Vite 5 | 单页应用，构建产物经 `go:embed` 内嵌进二进制 |

---

## 2. 目录与模块

```
Lnh-Music-Tag-main/
├── main.go              入口：配置、路由注册、中间件、优雅停机、启动单实例锁
├── auth.go / security.go 会话（cookie）、登录/改密、防暴力枚举、CSRF（X-Requested-With）
├── tracks_db.go         曲目持久化：批量事务（300 条/批）、原子 seq、启动加载
├── helpers.go / scan    目录扫描、曲目模型操作、工具函数
├── scrapejob.go         后台任务框架：jobStore / 信号量（容量 2）/ panic 恢复 / 进度轮询
├── pipeline.go          一键刮削流水线（乱码修复→简繁转换→补艺术家→刮削元素）
├── plugin.go            插件注册表与状态持久化
├── plugins_api.go       插件管理 API（列表/更新/导入/删除/运行）
├── plugin_runner.go     第三方插件运行器（ZIP 校验、解压沙箱）
├── convert.go / ffmpeg.go  批量转码 + FFmpeg 探测/安装（TTL 缓存 + 超时）
├── identify.go          音频内容识别（指纹库 + AcoustID）
├── fixencoding.go / zhscript.go / renameclean.go  乱码修复 / 简繁转换 / 后缀清理
├── scheduler.go         任务计划（cron）+ 后台监控（watcher 静默刮削）
├── media_servers.go     网关总入口：协议分派、流媒体密码认证、流媒体
├── subsonic.go / navidrome.go   Subsonic/OpenSubsonic、Navidrome 协议实现
├── jellyfin/emby 适配   见 media_servers.go 内分派
├── streaming.go         流媒体直传 / 转码回退
├── lyrics_cache.go / entity_cache.go  歌词/实体响应缓存（30s TTL，写时失效）
├── trash.go             回收站 API 与 manifest
├── activity.go          操作日志
├── version.go / static.go  版本检查 / 前端静态资源（go:embed）
└── internal/
    ├── model/           曲目模型
    ├── store/           配置库封装（Config）
    ├── scrape/          多源刮削聚合器（17 个源）
    ├── finger/          音频指纹（ffmpeg 转码 + 特征提取）
    ├── taglibx/         taglib 封装（含 SHA-256 哈希）
    ├── audiofmt/        音频格式探测
    └── dedup/           智能去重（hash/format/tags 三种口径）
```

---

## 3. 架构要点

### 3.1 单进程单端口

后端与前端共用同一 HTTP 端口（默认 `:10248`）：

- `/api/*` —— 浏览器 UI 的后端 API（会话认证 + CSRF）；
- `/rest/*`、`/jellyfin/*`、`/emby/*`、`/auth/*`、部分 `/api/*` —— 流媒体网关（协议认证，流媒体密码）；
- `/` —— 内嵌前端静态资源。

### 3.2 数据持久化

- **曲目表**：`tracks`（SQLite），扫描结果持久化，重启恢复列表；
- **配置库**：`LNH_CONFIG_DIR` 下 SQLite，保存管理员账号、插件状态与配置、API Key；
- **任务历史**：后台任务完成后落库，重启可查；
- **操作日志**：`activity` 表，扫描/刮削/转换/任务计划/监控全程留痕；
- **整理方式**：`organize_mode` 配置键（`off`/`album`/`artist`，默认 `off`），扫描/刮削完成后自动把曲目移动到库根下的「专辑名/」或「歌手名/」子目录（`organize.go`，幂等、不覆盖、同名 `.lrc` 跟随）；
- **回收站**：`trash/` 目录 + `_manifest.jsonl` 清单（记录原路径，支持恢复）。

### 3.3 后台任务框架（scrapejob.go）

批量刮削、一键刮削、格式转换、音频识别、乱码修复、简繁转换共用同一套任务框架：

1. `jobStore.create(total, kind)` —— 创建任务（running、进度 0）；
2. `jobStore.runBounded(job, fn)` —— 在容量为 2 的信号量下启动 goroutine；
3. 任务内逐项推进 `Done / Current / Results`，支持取消（`Cancel` 置位）；
4. 收尾统一由 `finish` 兜底：`running → done / cancelled`，并落库；
5. `recoverJob` 捕获 panic：任务置 `failed` 终态，崩溃详情写入 `Message` 与 `Results`。

前端通过 `GET /api/jobs/{id}`（工具任务）与 `GET /api/scrape/jobs/{id}`（批量）轮询进度；`GET /api/jobs` 可在刷新后恢复进行中的任务。

### 3.4 任务计划与后台监控（scheduler.go）

**任务计划**：5 段 cron（分 时 日 月 周），30 秒轮询到点触发：

- `parseCron` 支持 `*`、`*/n`、`a-b`、`a,b`（含组合与步进），周段 `0-6`（`7` 等价周日）；
- `nextAfter` 计算下次运行时间，设置页实时显示；`check` 到点后 `fire` 执行（防重入：`running` 置位）；
- 刮削模式 `mode`：`onekey` = 一键完整流水线；`dedup` = 先自动去重（按哈希/同名/艺术家+标题合并，每组保留音质最好）再跑一键流水线；
- 设置页用下拉生成 cron（频率 每天/每周/自定义 + 时间），`buildSchedCron` 生成、`parseSchedCron` 回填；无法匹配的旧 cron 落到「自定义」保留原输入；
- 默认关闭（`scheduler_enabled` / `scheduler_cron` / `scheduler_mode` / `scheduler_scan_first`）。

**后台监控（watcher）**：`watchCheck` 每 30 秒轮询，按 `watchInterval`（分钟 1–1440）检查目录：

- `watchScan` 每次 `WalkDir` 收集音频清单（复用 `skipScanDirs` 与 `model.AudioExts`），与内存基线对比；
- 首次运行只建立基线不刮削（防止进程重启后对全库重复刮削）；发现新增文件 → 读标签入库 → 静默刮削 → 更新 `lastWatchRun/lastWatchMsg` 并写 `act_watch` 日志 → 并入基线；
- `watchRunning` 锁防重入；配置键 `watcher_enabled` / `watcher_interval`。

### 3.5 数据库批量事务（tracks_db.go）

- 扫描、批量补全等大写入走 `upsertTracksDB / upsertTrackBatch`，每批 300 条单事务提交；
- 曲目序号 `seq` 为原子计数器（`atomic.Int64`），并发分配不重号；
- 启动时 `loadTracksFromDB` 在锁外构建列表、一次性加锁，缩短持锁时间。

### 3.6 外部进程调用（超时保护）

所有外部命令调用都有超时与缓存保护：

| 调用 | 超时 | 缓存 |
|---|---|---|
| FFmpeg 版本探测 | 3s | 10 分钟 TTL |
| 音频指纹转码（finger） | 60s | — |
| AcoustID 识别转码 | 2min | — |
| 流媒体转码探测 | 3s | 10 分钟 TTL（失败不缓存） |
| 批量转换 | 30min | — |
| FFmpeg 安装/卸载 | 5min | — |

### 3.7 网关响应缓存（性能）

四协议网关对高重复、高体积的响应做进程内缓存：

| 缓存 | 键 | 失效策略 |
|---|---|---|
| 封面（store 内） | 文件路径 | 按文件 mtime 校验，文件更新即失效 |
| 歌词（lyrics_cache.go） | 曲目 ID | 30s TTL |
| 实体响应（entity_cache.go） | `协议:类型:ID[:json]` | 30s TTL + 写时失效（曲库增删/标签刷新/收藏/评分变动即清） |

> 注意：实体缓存是「序列化结果」缓存。曲库改动、收藏、评分必须经过对应失效挂点（`addMany/remove/removeByPath`、`refreshTrack`、`saveStarred`、`saveRatings` 等），新增写路径时需同步挂失效。

### 3.8 网关分页与字段投影

- Navidrome 风格列表接口返回 `x-total-count` 与 `Content-Range` 分页头；
- 分页解析 `parseNonNegParam` 将负数/非法值归零，`sliceRange` 再兜底（`total<=0` 早退、end 上界钳制），杜绝负切片索引 panic；
- 空列表返回合法分页头（`items */0`）与 `[]` 而非 `null`；
- 列表响应只投影轻量字段（id/title/artist/album/duration/coverArtId 等），不搬运歌词全文与封面二进制。

### 3.9 插件导入安全（plugin_runner.go）

- 第三方插件 ZIP 导入：解压前校验 `plugin.json` 的 name（字符集白名单）、entries/panel 相对路径（防穿越、zip-slip）；
- 文件数上限 10000、单文件解压上限 256MiB；解压全程路径归一化校验；
- 插件状态与配置持久化于数据库，覆盖升级保留已填配置。

### 3.10 优雅退出

`main()` 注册 `SIGINT/SIGTERM`：停止接收新连接、等待在途请求完成（上限 5s），随后 `defer cfg.Close()` 关闭配置库。容器 `docker stop` 不会硬中断在途写库/流。

### 3.11 刮削可靠性（P0）

- **来源健康度 / 熔断 / 自适应退避**（`internal/scrape/health.go`）：每个源独立统计连续失败（阈值 3），熔断期间 `SearchWithFallback` / `FetchCoverFallback` / `FetchLyrics` 直接跳过该源；冷却指数退避（5m→30m 封顶），成功清零；时钟可注入（`var now`），单测覆盖熔断/恢复/退避/回退链跳过
- **匹配置信度**（`confidence.go`）：`candidateConfidence` 按标题 50% + 艺人 30% + 时长 20% 输出 0-100；目标维度缺失不扣分并归一化权重；融合结果在 `scrapejob.go` 回填 `SearchResult.Confidence`
- **低置信度队列**（`pending.go` + MusicTree.vue）：`Confidence < 55` 的结果按 path 入 `pending_tracks` 表（upsert 覆盖，best-effort）；曲库「待确认」筛选 + 核对模态（确认/忽略），接口 `GET /api/scrape/pending`、`POST /api/scrape/pending/resolve`

---

## 4. 构建与测试

```bash
# 前端（改了前端必须重建）
cd web && npm install && npm run build && cd ..

# 后端（单二进制，含前端资源）
go build -o lnh-music-tag .

# 静态检查 + 测试
go vet ./...
go test -count=1 ./...

# 本地运行（开发）
LNH_CONFIG_DIR=./config LNH_PORT=:10248 go run .
```

已有测试覆盖：乱码修复、FFmpeg 探测/安装、网关功能（评分/播放列表/认证）、流媒体回退、流媒体 Range/HEAD/416、分页边界（负数/越界/空库/多参数优先级）、插件路径校验、scheduler（cron 解析/监控）、任务框架、辅助函数、改名、主流程、来源响应夹具（网易云 mock）、标签原子写/损坏文件安全、品质分级边界等。

```bash
# 移动端/桌面 UI 冒烟测试（Playwright + 系统 Edge，不下载浏览器）
# 先启动后端（如 :10248），再执行：
cd web && npm run e2e
# 或指定目标与环境变量：
LNH_E2E_BASE=http://localhost:10248 LNH_E2E_USER=admin LNH_E2E_PASS=xxx npm run e2e
```

E2E 覆盖：Web 登录（表单提交）、桌面列表与「品质」列/筛选标签、移动端（420×900）卡片展示/品质文本/无横向溢出/全选条/滚动加载；截图证据存 `web/e2e/*.png`（已 gitignore）。

> 本机无 C 编译器时 `go test -race` 可能无法运行（依赖 cgo），可用普通 `go test` 替代。

---

## 5. 代码约定

### 5.1 并发安全
- 共享状态必须持锁：`store.mu`（曲目）、`job.mu`（任务）、`a.mu / a.recMu`（Subsonic 播放状态与最近访问独立锁）；
- 大锁只保护必要范围：列表构建在锁外完成、批量更新一次加锁；
- 后台任务并发有界（信号量），禁止无上限 `go func()` 批量启动。

### 5.2 外部调用
- 所有 `exec.Command` 必须带超时（`CommandContext` + `Timeout`）；
- 探测类调用结果必须带 TTL 缓存，避免高频重复探测。

### 5.3 数据库
- 批量写入使用事务批提交（`upsertTrackBatch`），禁止循环内逐条事务；
- 配置 Key（API Key、密码）写入前必须脱敏，禁止明文写日志。

### 5.4 错误处理
- 任务处理函数任何 panic 都由 `recoverJob` 兜底为 `failed` 终态，不得让 goroutine 裸崩溃；
- 前端轮询需处理 `done / cancelled / failed` 三种终态。

### 5.5 前端
- 状态集中在 `store.js`（曲目列表、选择集、任务轮询），组件通过 `state` 读写；
- 文案走 `i18n.js` 中英双语，key 与后端返回字符串一致；
- API 请求统一走 `api.js`（自动带 CSRF 头 `X-Requested-With`）。

### 5.6 测试
- 测试数据一律用 ASCII 路径（Windows 下中文路径经 PowerShell/API 传递可能变 `???` 导致 `os.Stat` 失败）；
- 分页/边界类函数必须覆盖负数、越界、空数据、多参数组合。

---

## 6. 扩展指引

### 新增一个刮削源
1. 在 `internal/scrape` 实现 `Source` 接口（搜索 + 详情）；
2. 在插件注册表注册为 `scrape.<name>` 插件；
3. 在前端 `i18n.js` 与源列表 UI 添加名称；
4. 为可选的 API Key 字段实现 `pluginKeyFields` 返回。

### 新增一个 API
1. `main.go` 注册路由 → handler 读写 `cfg`/曲库 → 前端 `api.js` 调用；
2. 非 GET 请求记得校验 `X-Requested-With`（CSRF）；
3. 新增写路径时，同步挂上实体缓存失效点。

### 新增网关端点
在对应协议适配器（`subsonic.go` / `navidrome.go` / media_servers 内 Jellyfin、Emby 分派）实现，并在 `docs/STREAMING_GATEWAY.md` 端点清单登记。

---

## 7. 相关文档

- [API.md](./API.md)：后端 API 参考
- [STREAMING_GATEWAY.md](./STREAMING_GATEWAY.md)：网关协议与客户端接入
- [PLUGINS.md](./PLUGINS.md)：插件系统与第三方开发
- [DEPLOYMENT.md](./DEPLOYMENT.md)：部署与运维
