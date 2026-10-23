# 开发文档

面向在本地构建、调试、扩展 Lnh-Music-Tag 的开发者。本文基于当前代码库（v1.0.11）的实际实现编写。

---

## 1. 技术栈与依赖

| 层 | 技术 | 说明 |
|---|---|---|
| 后端 | Go 1.27 | 纯 Go 单二进制，无 CGO 依赖 |
| 数据库 | modernc.org/sqlite | 嵌入式 SQLite（曲目表 / 配置库 / 任务历史 / 操作日志） |
| 标签读写 | go.senan.xyz/taglib（WASM 封装） | 读取/写入 ID3v2、FLAC、MP4 等标签 |
| 简繁转换 | github.com/liuzl/gocc | 简体 ↔ 繁体批量转换 |
| 前端 | Vue 3 + Vite 5 | 单页应用，构建产物经 `go:embed` 内嵌进二进制 |

> 项目模块名 `lnh-music-tag`，Go 版本见 `go.mod`。前端构建见 `web/package.json`。

---

## 2. 目录结构

```
Lnh-Music-Tag-main/
├── main.go              # 入口：配置、路由注册、中间件、启动
├── auth.go              # 会话（cookie session）、CSRF 校验
├── security.go          # 登录、改密、防暴力枚举
├── tracks_db.go         # 曲目持久化：批量事务、原子 seq、启动加载
├── scan*.go / helpers.go# 目录扫描、曲目模型操作
├── scrapejob.go         # 后台任务框架：jobStore / 信号量 / panic 恢复 / 进度轮询
├── pipeline.go          # 一键刮削流水线（乱码→繁简→补艺术家→刮削）
├── plugin.go            # 插件注册表与状态
├── plugins_api.go       # 插件管理 API（列表/更新/导入/删除/运行）
├── plugin_runner.go     # 第三方插件运行器
├── convert.go           # 批量格式转换任务
├── ffmpeg.go            # FFmpeg 探测/安装/版本缓存（TTL + 超时）
├── identify.go          # 音频内容识别（指纹库 + AcoustID）
├── fixencoding.go       # 乱码标签修复
├── renameclean.go       # 文件名后缀清理
├── zhscript.go          # 简繁转换任务
├── scheduler.go         # 任务计划：cron 解析 / 后台监控静默刮削（watcher）
├── streaming.go         # 流媒体直传 / 转码回退
├── lyrics_cache.go      # 歌词短 TTL 缓存（30s，四协议共享）
├── entity_cache.go      # 网关实体响应缓存（30s TTL / 1024 上限，写时失效）
├── subsonic.go          # Subsonic / OpenSubsonic 协议实现
├── navidrome.go         # Navidrome 兼容 REST 子集
├── media_servers.go     # Jellyfin / Emby 协议适配
├── trash.go             # 回收站 API 与 manifest
├── activity.go          # 操作日志
├── version.go           # 版本 / 更新检查
├── static.go            # 前端静态资源（go:embed）
├── internal/
│   ├── model/           # 曲目模型
│   ├── store/           # 配置库（Config 封装）
│   ├── scrape/          # 多源刮削聚合器（17 个源）
│   ├── finger/          # 音频指纹（ffmpeg 转码 + 特征提取）
│   ├── taglibx/         # taglib 封装（含 SHA-256 哈希）
│   ├── audiofmt/        # 音频格式探测
│   └── dedup/           # 智能去重
└── web/                 # Vue 3 前端源码（src/ 见下）
    └── src/
        ├── App.vue          # 布局 / 顶栏（回收站入口等）
        ├── views/           # Home / Login / Settings / Plugin
        ├── components/      # MusicTree / EditPanel / BatchPanel / DedupPanel /
        │                    # TrashModal / FormatDialog / ActivityLog / UpdateModal / AboutModal
        └── store.js         # 全局状态：曲目、选择集、任务轮询
```

---

## 3. 核心架构

### 3.1 单进程单端口

后端与前端共用同一 HTTP 端口（默认 `:10248`）：

- `/api/*` —— 浏览器 UI 的后端 API（会话认证 + CSRF）
- `/rest/*`、`/jellyfin/*`、`/emby/*`、`/auth/*`、部分 `/api/*` —— 流媒体网关（协议认证）
- `/` —— 内嵌前端静态资源

### 3.2 数据持久化

- **曲目表**：`tracks`（SQLite），扫描结果持久化，重启恢复列表
- **配置库**：`LNH_CONFIG_DIR` 下的 SQLite 配置库，保存管理员账号、插件状态与配置、Key
- **任务历史**：后台任务完成后写库，重启可查
- **操作日志**：`activity` 表，扫描/刮削/转换全程留痕
- **回收站**：`trash/` 目录 + `_manifest.jsonl` 清单（记录原路径，支持恢复）

### 3.3 后台任务框架（scrapejob.go）

批量刮削、一键刮削、格式转换、音频识别、乱码修复、简繁转换共用同一套任务框架：

1. `jobStore.create(total, kind)` —— 创建任务（running、进度 0）
2. `jobStore.runBounded(job, fn)` —— 在容量为 2 的信号量 `jobSem` 下启动 goroutine
3. 任务内逐项推进 `Done / Current / Results`，支持取消（`Cancel` 置位）
4. 收尾统一由 `finish` 兜底：`running → done / cancelled`，并落库
5. `recoverJob` 捕获 panic：任务置 `failed` 终态，崩溃详情写入 `Message` 与 `Results`，前端轮询可感知

前端通过 `GET /api/jobs/{id}`（工具任务）与 `GET /api/scrape/jobs/{id}`（批量）轮询进度；页面刷新或换设备登录后 `GET /api/jobs` 可恢复进行中的任务。

### 3.4 任务计划与后台监控（scheduler.go）

**任务计划**：5 段 cron（分 时 日 月 周），30 秒轮询到点触发：

- `parseCron` 支持 `*`、`*/n`、`a-b`、`a,b`（含组合与步进），周段 `0-6`（`7` 等价周日）
- `nextAfter` 计算下次运行时间，设置页实时显示；`check` 到点后 `fire` 执行（防重入：`running` 置位）
- 刮削模式 `mode`：`onekey` = 一键完整流水线（`runOneKeyPipeline`，乱码→简繁→补艺术家→刮削）；`dedup` = 先自动去重（`stageDedup`：按哈希/同名/艺术家+标题合并，每组保留音质最好的一首）再跑一键流水线
- 设置页用下拉生成 cron（频率 每天/每周/自定义 + 星期 + 24 小时制时间），`buildSchedCron` 生成字符串、`parseSchedCron` 加载回填；无法匹配的旧 cron 落到「自定义」保留原输入
- 任务计划与后台监控默认关闭（`enabled` / `watchEnabled` 配置键缺省 `0`）

**后台监控（watcher）**：`watchCheck` 每 30 秒轮询，按 `watchInterval`（分钟，1–1440）检查目录：

- `watchScan` 每次 `WalkDir` 收集音频清单（复用 `skipScanDirs` 与 `model.AudioExts`），与内存基线对比
- 首次运行只建立基线不刮削（防止进程重启后对全库重复刮削）；发现新增文件 → `taglibx.ReadTrackLight` 读标签入库 → `runBatch(...,"auto",...)` 静默刮削 → 更新 `lastWatchRun/lastWatchMsg` 并写 `act_watch` 日志 → 并入基线
- `watchRunning` 锁防重入；配置键 `watcher_enabled` / `watcher_interval`

### 3.5 数据库批量事务（tracks_db.go）

- 扫描、批量补全等大写入走 `upsertTracksDB / upsertTrackBatch`，每批 300 条单事务提交，避免逐条提交的性能开销
- 曲目序号 `seq` 为原子计数器（`atomic.Int64`），并发分配不重号
- 启动时 `loadTracksFromDB` 在锁外构建列表、一次性加锁，缩短持锁时间

### 3.6 外部进程调用（P1-1 超时保护）

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

四协议网关对高重复、高体积的响应做进程内缓存，降低大库下列表/单曲/歌词/封面的重复组装与序列化开销：

| 缓存 | 键 | 失效策略 |
|---|---|---|
| 封面（`coverCache`，store 内） | 文件路径 | 按文件 mtime 校验，文件更新即失效 |
| 歌词（`lyrics_cache.go`） | 曲目 ID | 30s TTL |
| 实体响应（`entity_cache.go`） | `协议:类型:ID[:json]` | 30s TTL + 写时失效：曲库增删/标签刷新/收藏/评分变动即清 |

> 注意：实体缓存是「序列化结果」缓存（非查库缓存）。曲库改动、收藏、评分必须经过对应失效挂点（`store.addMany/remove/removeByPath`、`refreshTrack`、`saveStarred`、`saveRatings` 等），新增写路径时需同步挂失效，否则客户端会看到陈旧状态。

### 3.8 优雅退出

`main()` 注册 `SIGINT/SIGTERM`：收到信号后停止接收新连接、等待在途请求完成（上限 5s），随后返回并 `defer cfg.Close()` 关闭配置库。容器 `docker stop`（发 SIGTERM）不会硬中断在途写库/流。

---

## 4. 构建与测试

### 4.1 构建

```bash
# 前端（可选：仓库 dist 已内嵌；改了前端必须重建）
cd web && npm install && npm run build && cd ..

# 后端（单二进制，含前端资源）
go build -o lnh-music-tag .
```

### 4.2 测试

```bash
go test ./...
go vet ./...
```

已有测试覆盖：乱码修复（`fixencoding_test.go`）、FFmpeg（`ffmpeg_test.go`）、网关功能（`gateway_features_test.go`）、流媒体回退（`streaming_fallback_test.go`）、流媒体 Range 与缓存失效（`streaming_range_test.go`）、任务框架（`scrapejob_test.go`）、辅助函数（`helpers_test.go`）、改名（`rename_test.go`）、主流程（`main_test.go`）等。

> 本机无 C 编译器时 `go test -race` 可能无法运行（依赖 cgo），可用普通 `go test` 替代。

---

## 5. 代码规范

### 5.1 并发安全
- 共享状态必须持锁：`store.mu`（曲目）、`job.mu`（任务）、`a.mu / a.recMu`（Subsonic 播放状态与最近访问独立锁）
- 大锁只保护必要范围：列表构建在锁外完成、批量更新一次加锁
- 后台任务并发有界（信号量），禁止无上限 `go func()` 批量启动

### 5.2 外部调用
- 所有 `exec.Command` 必须带超时（`CommandContext` + `Timeout`）
- 探测类调用结果必须带 TTL 缓存，避免高频重复探测

### 5.3 数据库
- 批量写入使用事务批提交（`upsertTrackBatch`），禁止循环内逐条事务
- 配置 Key（API Key、密码）写入前必须脱敏，禁止明文写日志

### 5.4 错误处理
- 任务处理函数任何 panic 都会由 `recoverJob` 兜底为 `failed` 终态，不得让 goroutine 裸崩溃
- 前端轮询需处理 `done / cancelled / failed` 三种终态

### 5.5 前端
- 状态集中在 `store.js`（曲目列表、选择集、任务轮询），组件通过 `state` 读写
- 文案走 `i18n.js` 中英双语，key 与后端返回的 `Unavailable` 等字符串必须一致
- API 请求统一走 `api.js`（自动带 CSRF 头 `X-Requested-With`）

---

## 6. 新增一个刮削源

1. 在 `internal/scrape` 实现 `Source` 接口（搜索 + 详情）
2. 在插件注册表（`plugin.go` / `main.go` 的 `registerBuiltinPlugins`）注册为 `scrape.<name>` 插件
3. 在前端 `i18n.js` 与源列表 UI 中添加名称
4. 为可选的 API Key 字段实现 `pluginKeyFields` 返回

详见 [docs/PLUGINS.md](./PLUGINS.md)。
