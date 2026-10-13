# LNH-MusicTag 开发文档

> 版本：v1.4.9 · 仓库：github.com/lahuolou/lnh-musictag · 协议：MIT
> 本文面向开发者：介绍项目架构、模块职责、数据模型、API、插件扩展点、音频识别与一键刮削流水线、发布流程。

---

## 1. 项目概述

LNH-MusicTag 是一个**自托管音乐库标签管理工具**：扫描本地音乐目录，读写音频标签（标题/艺术家/专辑/封面/歌词等），多源刮削补全元数据，智能去重，并支持格式转换、乱码修复、简繁体转换、音频内容识别等功能。只提供 Web 后台（账号/密码/API Key 均存本地数据库）。

核心设计目标：

- **单二进制交付**：Go 后端通过 `go:embed` 内嵌 Vue3 构建产物，运行只有一个可执行文件。
- **无 CGo**：标签读写采用 go-taglib（WASM 封装 TagLib），SQLite 采用 modernc.org/sqlite（纯 Go），全静态编译。
- **插件化扩展**：刮削源 / 工具 / 下载器 / 服务协议统一走插件注册表，新能力无需改页面。
- **后台化任务**：扫描、批量刮削、识别、转换、修复等全部异步执行，进度可视、成功/失败/跳过统计、可取消、刷新/换设备可恢复。
- **一条流水线**：识别、一键刮削、乱码修复、简繁转换共用同一套异步任务框架与公共实现，避免重复代码。

### 1.1 技术栈

| 层 | 技术 | 说明 |
|----|------|------|
| 后端 | Go 1.27（标准库 net/http + ServeMux 路由） | 无第三方 Web 框架 |
| 标签读写 | github.com/sentriz/go-taglib（WASM） | 免 CGo，支持多值标签 |
| 音频指纹 | chromaprint（fpcalc 子进程）+ AcoustID API | 音频内容识别 |
| 元数据补全 | MusicBrainz Web Service v2 | Recording ID → 完整元数据 |
| 简繁转换 | OpenCC（zhscript 封装） | 简繁互转 |
| 存储 | modernc.org/sqlite（纯 Go SQLite） | 配置 + 曲目统一入库 |
| 前端 | Vue 3 + Vite | 无 UI 框架，自绘样式 |
| 部署 | Docker 多阶段构建 → ghcr.io 镜像 | CI 自动构建推送 |
| 更新检查 | GitHub API + jsDelivr CDN + raw 文件 | 三源容错 |

### 1.2 版本规划

- 版本号语义化递增：v1.4.1 → v1.4.2 → … → v1.4.50 → **v1.5.0** → v1.5.1 …
- 每次发布必须：递增 `main.go` 的 `AppVersion`、更新根目录 `VERSION` 文件、创建对应 GitHub Release tag（tag 名带 `v` 前缀）。
- 更新检查依据 `VERSION` 文件与 Release tag，语义化逐段比较（`1.4.50 → 1.5.0` 正确判新）。
- **版本不同步递增会导致项目检测不到更新**——这是登录后"检查更新"误报"已是最新"的最常见原因。

---

## 2. 系统架构

```
浏览器 (Vue3 SPA)
   │  HTTP /api/*  (Cookie 会话 + CSRF + 限流)
   ▼
Go 后端 (单二进制，端口 10248)
   ├── 会话鉴权 auth.go          ── 登录/登出/改密/会话 Cookie
   ├── 安全防护 security.go      ── CSRF / XSS / SQL 注入防护 / 输入净化
   ├── 插件框架 plugin.go        ── 刮削源/工具/下载/协议 统一注册表
   ├── 刮削引擎 internal/scrape  ── 各源实现 + MultiSource 调度（语言优先）
   ├── 标签读写 internal/taglibx ── go-taglib 封装
   ├── 任务引擎 scrapejob.go     ── 批量任务（进度/恢复/结果统计/取消）
   ├── 工具链 convert.go / fixencoding.go / zhscript.go / identify.go
   ├── 流水线 pipeline.go        ── 一键刮削（可选去重 → 修复 → 转换 → 刮削）
   ├── 曲目存储 tracks_db.go     ── SQLite 持久化（重启不丢、增量落库）
   ├── 更新检查 version.go       ── 多源最新版本查询 + 三态结果
   └── 静态资源 static.go        ── index.html no-cache / 哈希资源 immutable
           │
           ├── SQLite (lnh.db)   ── settings 表（账号/Key/选项/插件配置）
           │                        tracks 表（曲目 JSON）
           └── 音乐目录 (/music) ── 读写标签/封面/歌词文件
```

### 2.1 请求链路

1. 公开首页 `/`：无需登录（品牌 + 介绍 + 搜索占位），登录入口弹层。
2. 登录后进入后台：顶栏（曲目数/列表/首页/齿轮菜单），合并页布局（左侧列表 + 右侧编辑/批量/去重）。
3. 所有 `/api/*` 除 `login` 外均要求会话 Cookie，经 `csrfGuard` + `limitBody` 中间件。
4. 前端 `api.js` 统一封装 fetch，自动带 `X-Requested-With: LNH-MusicTag` 头（CSRF 校验需要）。

---

## 3. 目录结构

```
项目根
├── main.go              程序入口：路由注册、中间件、静态资源、服务启动、store
├── auth.go              会话与账号：登录/登出/改密/随机初始密码/会话 Cookie
├── security.go          安全：CSRF、XSS 净化、SQL 注入防护、敏感键校验
├── plugin.go            插件框架：注册表/快照/启停/配置持久化
├── plugins_api.go       插件注册与 API：刮削源注册、GET/POST /api/plugins
├── version.go           更新检查：多源查询、三态结果、语义化版本比较
├── static.go            静态缓存策略：index.html no-cache、assets immutable
├── scrapejob.go         批量任务引擎：进度、恢复、成功/失败/跳过统计、取消
├── pipeline.go          一键刮削流水线：自动去重(可选)→乱码修复→繁转简→补艺术家→刮削
├── identify.go          音频内容识别：14 格式扫描→转码→指纹→AcoustID→MB→写标签→重命名
├── fixencoding.go       乱码修复（GBK/UTF-8 错乱检测与重写；与流水线共用 repairTrackText）
├── zhscript.go          简繁体转换（OpenCC；识别/修复结果统一转简体）
├── convert.go           格式转换（ffmpeg 转码）
├── ffmpeg.go            FFmpeg 插件化：路径探测、状态、一键安装
├── renameclean.go       修正文件后缀（探测真实格式后重命名，如 .mp3.flac → .flac）
├── activity.go          操作日志（日志页：记录刮削/转换/修复等动作，可删除）
├── helpers.go           通用工具：writeJSON、路径安全、标签标准化等
├── tracks_db.go         曲目 SQLite 持久化：落库/加载/外部删除清理
├── *.go *_test.go       测试
├── internal/
│   ├── store/config.go  SQLite 键值配置存储（settings 表）
│   ├── model/model.go   数据模型：Track、DuplicateGroup、音频扩展名
│   ├── taglibx/         go-taglib 标签读写封装
│   ├── dedup/           去重策略（哈希/格式/标签）
│   ├── finger/          Chromaprint 指纹封装（含 AcoustID 用压缩指纹）
│   ├── audiofmt/        音频格式探测
│   └── scrape/          刮削源：sources.go + 各源实现（含 MusicBrainz 客户端）
├── web/
│   ├── package.json / vite 配置
│   └── src/
│       ├── main.js      入口
│       ├── App.vue      根组件：首页/后台切换、顶栏、齿轮菜单、全局弹窗
│       ├── store.js     全局状态（reactive）+ 任务轮询 + 更新检查
│       ├── api.js       fetch 封装
│       ├── i18n.js      中英双语字典
│       ├── style.css    全局样式（明暗主题变量）
│       ├── views/       Home（公开首页）/ Login / Settings / Plugin
│       └── components/  MusicTree（左列表）/ EditPanel / BatchPanel /
│                         DedupPanel / FormatDialog / Toast / UpdateModal / AboutModal
├── Dockerfile          多阶段：Vue 构建 → Go 静态编译 → alpine 运行
├── docker-compose.yml  推荐部署（端口/卷/环境变量/FFmpeg 开关）
├── entrypoint.sh       LNH_INSTALL_FFMPEG=true 时启动前自动安装 ffmpeg
├── VERSION             当前版本号（更新检查用）
└── README.md           使用说明（中英双语）
```

---

## 4. 数据模型

### 4.1 settings 表（SQLite 键值）

| 键 | 说明 |
|----|------|
| `admin_user` / `admin_pass` | 后台账号 / 密码（bcrypt 哈希） |
| `session_secret` | 会话签名密钥（首启生成） |
| `acoustid_key` 等 | 各 API Key（Last.fm / Spotify ID+Secret / Discogs / Jamendo / AcoustID） |
| `ffmpeg_path` | 旧键：FFmpeg 路径（兼容迁移） |
| `plugins` | 插件启停与配置 JSON（全量持久化） |
| `sources_enabled` | 旧键：刮削源开关（兼容迁移） |

### 4.2 tracks 表

| 列 | 说明 |
|----|------|
| `id` | 稳定 ID = SHA256[:16] |
| `path` | 服务器绝对路径 |
| `data` | Track 完整 JSON |
| `seq` | 扫描顺序（重启后按序恢复，保持"先扫先排"） |

启动时加载全部曲目（文件已外部删除的自动丢弃），扫描/编辑/重命名/删除均同步落库。

### 4.3 Track 结构（model）

```go
type Track struct {
    ID, Path, FileName, Ext string
    Size int64
    SHA256 string          // 去重策略1：内容哈希
    Duration float64        // 秒
    Bitrate, SampleRate, Channels uint
    HasCover, HasLrcFile bool
    HasTrad bool            // 标签含繁体（OpenCC 检测，供"繁体"筛选）
    Garbled bool            // 标签疑似乱码（GBK/UTF-8 错乱）
    NeedsIdentify bool      // 无文件名与标签信息 → 需音频内容识别
    Tags map[string]string  // 扁平化标签（标准键）
}
```

---

## 5. 音频内容识别流水线（identify.go）

按"扫描 → 分流 → 指纹 → AcoustID → MusicBrainz → 写标签 → 重命名"流水线实现，对标 AcoustID + MusicBrainz 官方方案：

```
音频文件
   │  14 种格式：.mp3 .m4a .aac .flac .alac .wav .aiff .ogg
   │              .opus .wma .ape .wv .dsf .dff（兼容 .mpc/.mka）
   ▼
格式分流（identifyNeedsTranscode）
   ├── 免转码族：.mp3 .flac .wav .ogg .opus → 直接指纹
   └── 其他（含 DSD .dsf/.dff）→ FFmpeg 转 WAV（16bit/44.1kHz/双声道）再指纹
   ▼
指纹生成（internal/finger）
   └── fpcalc -json → Chromaprint 压缩指纹 + 时长（AcoustID 需要压缩指纹而非 raw）
   ▼
AcoustID 查询（3 QPS 限速，acOnce）
   └── /v2/lookup?client=KEY&meta=recordings+releasegroups+compress
       → MusicBrainz Recording ID + 基础元数据（score 置信度）
   ▼
MusicBrainz 补全（1 QPS 限速，mbOnce）
   └── LookupRecording(mbid, artists, releases, release-groups, tags)
       → 标题/艺术家/专辑/年代/流派完整元数据
   ▼
写标签 + 重命名（finalizeIdentify 公共收尾）
   ├── 标题/艺术家/专辑/流派自动转简体（AcoustID 与库内回退两路径都转）
   ├── taglibx 写标签 → 抓歌词 → refreshTrack 落库
   └── 重命名"艺术家 - 标题.后缀"（不覆盖已存在目标）
```

关键行为：

- **未配 AcoustID Key / 未命中**：回退**库内指纹匹配**（指纹余弦相似度阈值 0.85），与已标注曲目比对补全。
- **指纹库异步构建**：`POST /api/identify` 立即返回 `{jobId, total}`；任务首步先构建指纹库（`Current="构建指纹库…"`），随后逐曲目识别。
- **识别判定**：`filenameNoInfo`（文件名空/Unknown/乱码/纯数字如 track01）+ `needsIdentify`（文件名无信息且 TITLE 空或等于去后缀 basename）。
- **格式探测**：`.mp3.flac` 这类多后缀文件先探测真实编码格式再重命名正确后缀。

---

## 6. 一键刮削流水线（pipeline.go）

一键刮削把日常整理步骤串成一条流水线，逐曲目 × 阶段执行，**每个（曲目 × 阶段）占一个进度点**（进度按曲目计数，不再出现 4 倍进度）：

```
阶段 0（可选）自动去重 ── 勾选才执行，默认不勾选
   按三种口径合并分组（与去重页一致）：
   ① 艺术家+标题一致（同人同歌不同格式/版本）
   ② 同名多格式（广岛之恋.mp3 + 广岛之恋.flac）
   ③ SHA256 内容一致（完全相同副本）
   每组保留音质最好的一首（无损优先；hash 组保留第一首），
   其余删除文件并从列表移除。任一组的 keep 优先于其他组的删除
   （同一曲目同时出现在多组时不会被误删）。
阶段 1 乱码修复 ── stageFixEncoding，与 /api/fix-encoding 共用 repairTrackText
阶段 2 繁体转简体 ── stageScript，OpenCC t2s
阶段 3 文件名补艺术家 ── stageFixArtist（文件名不含艺术家时补）
阶段 4 刮削元素 ── stageScrape（歌名+海报+歌词；源没搜到海报自动匹配下一条源）
```

- 每阶段从 store 取**最新状态**（前一阶段可能重写标签/重命名/删除）。
- `Stage` / `StageLabel`（"刮削元素（3/4）"）单独显示当前阶段，前端实时更新列表不整表刷新。
- **取消**：`POST /api/jobs/{id}/cancel` → 任务在下一步前停止，状态 `cancelled`。
- 曲目被去重阶段删除后，其余阶段对该曲目直接计完成（done 仍能到 total）。

---

## 7. 后端模块详解

### 7.1 入口与路由（main.go）

- `AppVersion` 常量 = 当前版本号（发布时必须递增）。
- 路由：公开 `POST /api/login`、`POST /api/logout`、`GET /api/me`；其余全部挂 `pm`（plugins mux），统一套 `requireAuth → csrfGuard → limitBody`。
- 静态资源：`go:embed` 内嵌 `web/dist`，`/api/*` 优先于静态。
- 完整路由表见第 10 节。

### 7.2 会话鉴权（auth.go）

- 登录成功 → 生成签名会话 Cookie（HttpOnly + SameSite），密钥 `session_secret` 首启随机生成。
- 首次启动无密码 → 自动生成随机初始密码并打印到容器日志；登录后在「设置」页修改（写库）。
- 调试默认账号 admin/admin（仅测试库，`LNH_CONFIG_DIR` 播种）。

### 7.3 安全防护（security.go）

| 威胁 | 防护 |
|------|------|
| CSRF | 自定义头 `X-Requested-With` 校验 + Cookie SameSite |
| XSS | 前端 Vue 默认转义；后端对写入标签做 `<` `>` `&` `"` `'` 净化 |
| SQL 注入 | 全部参数化查询（database/sql 占位符） |
| 路径穿越 | 所有文件操作先 `filepath.Clean` 并校验在音乐目录内 |
| 拖库 | 密码 bcrypt 哈希存储；会话密钥随机；API Key 不在前端明文回显 |
| 请求放大 | `limitBody` 限制请求体大小；爬取外部 API 均带超时与限流 |

### 7.4 标签读写（internal/taglibx）

- 封装 go-taglib（TagLib 的 WASM 移植，无 CGo）。
- 标准化标签键：`title / artist / album / albumartist / genre / year / track / lyrics / comment`。
- 支持多艺术家（`/` 与 `_` 拆分）、多值读取、封面（APIC）与歌词（USLT）写入。
- 写操作统一走 `refreshTrack`：更新内存 + 落库（并保持列表按扫描顺序）。

### 7.5 扫描（main.go /api/scan）

- 异步增量扫描：按目录遍历音频扩展名，边扫边推（`/api/scan/status` 轮询），不等待全部完成。
- 每个文件：读标签 → 探测格式（时长/码率/采样率）→ 计算 SHA256 → 检测乱码/繁体/待识别标记 → 落库。
- 文件名解析（当标签缺失时）：`艺术家 - 标题.后缀` 拆分；多后缀先按音频格式探测。
- 暂停/继续：`POST /api/scan/pause`。排序：扫描中按扫描顺序（seq），完成后可点表头排序。

### 7.6 刮削引擎（internal/scrape）

- `MultiSource`：按源注册表调度，`auto` 自动按语言优选（中文 → 国内源，外文 → 国际源）；也可手动指定源。
- 源清单（全部插件化）：网易云 / QQ / 酷狗 / 酷我 / 咪咕 / B站 / 汽水 / 波点 / 5sing / MusicBrainz / iTunes / ListenBrainz / Deezer / Last.fm / Spotify / Discogs / Jamendo。
- 每个源实现统一 `Source` 接口：`Search(query) → SearchLyrics(title, artist)`。
- 刮削字段：标题/艺术家/专辑/专辑艺术家/流派/年代/曲目号/歌词/封面，逐字段可选、智能跳过已有标签。
- 批量任务 `/api/scrape/batch`：后台执行，进度 + 成功/失败/跳过统计，刷新/换设备可恢复。

### 7.7 批量任务引擎（scrapejob.go）

统一任务模型（批量刮削 / 一键刮削 / 格式转换 / 乱码修复 / 简繁转换 / 音频识别 / 修正后缀共用）：

- 内存任务表（`jobs`），`GET /api/jobs/{id}` 查询进度（status/done/total/current/results/ok/fail/skip/stage/stageLabel）；`GET /api/jobs` 列出进行中任务。
- 前端启动任务后存 `jobId`，登录/刷新后 `resumeJobs()` 重新挂载轮询——**后台任务跨设备持续执行**。
- 结果统计：成功 / 失败 / 跳过 / 总数，任务结束汇总显示（toast + 任务面板）。
- **取消**：`POST /api/jobs/{id}/cancel`（所有批量任务通用）。

### 7.9 批量刮削字段模型（29 项）与存疑自动识别

**字段模型（scrapejob.go）**：`ScrapeFields` 覆盖 28 个可勾选字段，按常用度排序（海报 → 核心标签 → 操作项 → 扩展标签 → 音频属性；前端展示与后端解析共用 `fieldNames()`）：

- 海报
- 核心标签：歌名 / 艺术家 / 专辑 / 专辑艺术家 / 风格 / 年代 / 曲目号 / 歌词（内置方式：抓歌词写内嵌标签，兼容旧 LRC 别名）
- 操作项：文件名（按「艺术家 - 标题.后缀」重命名）
- 扩展标签：作曲家 / 作词家 / 专辑类型 / 语言 / 简介（描述并入 COMMENT）/ 光盘编号 / 总光盘数 / 总音轨数 / 曲目增益 / 专辑增益 / 曲目峰值 / 专辑峰值
- 音频属性（写标签自定义键，无需源搜索）：比特率 / 采样率 / 位深度 / 声道 / 时长 / 文件大小

前端提供**全选/反选**（批量刮削元素区第一位）与**智能跳过**开关（置顶，文案精简为"智能跳过"）。曲目列表页展示与批量刮削元素**相同的全部字段列**（29 列含勾选，窄屏自动隐藏低优先级列并支持横向滚动）；**一键刮削**与批量刮削使用同一完整字段集 `allScrapeFields()`（含海报/歌词/文件名重命名）。属性与操作项**不依赖源搜索**：勾选后即使搜索无结果也会执行（属性写本地探测值、文件名重命名）。封面降级：当前源无海报自动匹配下一条源（`FetchCoverFallback`）。

`fieldsFromList()` 支持全部中文别名（歌名/标题、流派/风格、年代/年份、海报/封面、简介/描述、歌词(LRC文件)/lrc 等）。属性与操作项**不依赖源搜索**：勾选后即使搜索无结果也会执行（属性写本地探测值、LRC 写盘、文件名重命名）；搜索失败且无操作项才报错。封面降级：当前源无海报自动匹配下一条源（`FetchCoverFallback`）。

**智能跳过（applySkipFilled）**：乱码文本与 "Unknown/未知" 占位视为"未填充"（`tagNeedsFill`），刮削阶段会修正而不是跳过；LRC 已存在、文件名已规范则跳过对应操作。

**存疑自动识别**：批量刮削（`runBatch`）与一键刮削（`stageScrape`）在**配置了 AcoustID Key** 时，对存疑曲目（`trackIsDoubtful`：文件名艺术家为 Unknown/未知/track01、标签艺术家空/乱码/占位、或无有效标题）先执行音频识别：

```
存疑曲目 → fpcalc 指纹 → AcoustID 查询(3QPS) → MusicBrainz Recording 补全(1QPS)
        → 写标签（统一转简体）→ 抓歌词 → 按「艺术家 - 标题.后缀」重命名
        → 识别产生新 ID 后，后续刮削基于新曲目继续
```

未配 Key 或 AcoustID 未命中 → 回退**库内指纹匹配**（相似度 ≥0.85，不消耗在线额度）；仍无结果则继续刮削兜底（按文件名解析的标题搜索）。消息只报告实际执行的动作（搜索失败/无属性值时不再误报"文件名+LRC"）。

### 7.8 工具链

| 模块 | 功能 | 说明 |
|------|------|------|
| convert.go | 格式转换 | 调 ffmpeg 转码（插件化定位），输出同目录不删源文件 |
| fixencoding.go | 乱码修复 | 检测 GBK/UTF-8 错乱（mojibake），重写正确编码；与流水线共用 `repairTrackText` |
| zhscript.go | 简繁体转换 | OpenCC：默认繁体→简体，可切换方向 |
| identify.go | 音频内容识别 | 见第 5 节完整流水线 |
| renameclean.go | 修正文件后缀 | 探测真实格式后重命名正确后缀（发如雪.mp3.flac → 发如雪.flac） |
| activity.go | 操作日志 | 记录"何时刮削了多少首/转换了多少首/是否成功"，日志页可删除/清空 |

---

## 8. 插件框架（核心扩展点）

### 8.1 插件模型

```go
type Plugin struct {
    Name        string            // 唯一标识：scrape.qq / tool.ffmpeg / download.* / protocol.*
    Label       string
    Kind        PluginKind        // scrape | tool | download | protocol
    Enabled     bool
    Builtin     bool              // 内置不可停用（auto）
    Installable bool              // 支持一键安装
    NeedsKey    bool              // 依赖外部 API Key（默认关闭）
    KeyField    string            // 对应 settings 库 Key 键名
    KeyConfigured bool            // 快照时计算
    Fields      []PluginField     // 前端渲染表单
    Config      map[string]string // 始终序列化（至少 {}）
}
```

- `plugin.go`：注册表（`registerPlugin` / `pluginsSnapshot` / `applyPluginState` / `loadPluginsFromDB` / `savePluginsToDB`）。
- `plugins_api.go`：启动时注册全部刮削源 + FFmpeg；`GET /api/plugins`（快照）、`POST /api/plugins`（启停/配置更新）。
- 免 Key 刮削源在 `loadPluginsFromDB` 中**强制开启**；需 Key 源默认关闭，配置 Key 后自动变为可开启。
- 持久化：全量写入 `settings.plugins` 键（JSON），并同步兼容旧键。

### 8.2 新增一个插件

```go
// 在 plugins_api.go 的初始化函数中加入：
registerPlugin(&Plugin{
    Name:   "download.example",
    Label:  "示例下载器",
    Kind:   PluginDownload,     // 或 PluginProtocol（Subsonic 等）
    Fields: []PluginField{{Key: "url", Label: "fUrl", Type: "text"}},
}, func(name string, enabled bool, cfg map[string]string) {
    // 启停/配置变更时的生效回调
})
```

注册后前端无需改动自动渲染：开关 + 表单 + Key 徽标。下载 / 协议类型当前为预留扩展位（音源、下载器、自动换源、Subsonic 等插件后续接入）。

### 8.3 FFmpeg 插件化

- 镜像默认**不装** ffmpeg；`LNH_INSTALL_FFMPEG=true`（compose）时 `entrypoint.sh` 启动前 `apk add ffmpeg`。
- 三种启用方式：① compose 开关自动安装；② 插件页「一键安装」；③ 插件页填宿主 ffmpeg 绝对路径。
- `GET /api/ffmpeg/status`：探测路径与版本；`POST /api/ffmpeg/install`：容器内安装。

---

## 9. 前端详解（Vue3）

### 9.1 状态与路由（store.js）

- `state`（reactive）：authed / view（home|app）/ activeTab（list|edit|batch|dedup）/ tracks / selected / scanStatus / batchJob / toolJob / version 等。
- 无 vue-router：`activeTab` + `view` 组合控制页面。
- 任务轮询：`resumeJobs()` 在登录/刷新后按 `jobId` 重新挂载进度轮询。
- `checkVersion(force)`：force（手动）→ `/api/version/check` 实时重查；自动 → `/api/version` 读缓存。

### 9.2 页面结构

| 视图 | 说明 |
|------|------|
| `views/Home.vue` | 公开首页：品牌 + 介绍 + 搜索区（默认隐藏，未接入）+ 登录入口 |
| `views/Login.vue` | 登录弹层（账号默认留空） |
| `App.vue` | 后台框架：顶栏 + 齿轮菜单（主题 / 语言切换（中文显"切换EN"、英文显"Switch CN"）/ 插件 / 日志 / 更新 / 关于 / 设置 / 退出，点一下展开再点收起） |
| `components/MusicTree.vue` | 左侧列表：表头冻结；筛选（全部/乱码/繁体/歌词/海报/无艺术家/待识别）；输入即搜歌名 |
| `components/EditPanel.vue` | 右侧编辑：载入原标签 + 文件名回填、保存后按"艺术家-标题"重命名、刮削/歌词/封面 |
| `components/BatchPanel.vue` | 批量：一键刮削（含**自动去重复选框，默认不勾选**）+ 补全字段勾选 + 智能跳过；音频识别 / 格式转换 / 乱码修复 / 简繁转换 / 修正后缀 |
| `components/DedupPanel.vue` | 去重：哈希 / 格式 / 标签分组；勾选后批量删除 |
| `views/Settings.vue` | 设置：改密 + 数据库配置（API Key） |
| `views/Plugin.vue` | 插件页：插件开关 + Key 徽标 + FFmpeg 状态/路径/安装 |
| 弹窗组件 | `FormatDialog`（转换）、`UpdateModal`（更新提醒）、`AboutModal`（关于卡片）、`Toast` |

### 9.3 国际化与样式

- `i18n.js`：`lang`（zh/en）+ `t(key)` 字典查找；所有用户可见文案走 i18n，新增文案需补中英两处。
- `style.css`：CSS 变量主题（`data-theme="dark|light"`），默认跟随系统；移动端适配（列表单列、横屏不溢出）。

---

## 10. API 参考

> 除标注外均需登录会话。请求头 `X-Requested-With: LNH-MusicTag`（CSRF）。

### 10.1 会话

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/login` | 登录（body: user/pass），下发会话 Cookie |
| POST | `/api/logout` | 登出 |
| GET | `/api/me` | 当前会话用户（刷新恢复登录态用） |
| POST | `/api/change-password` | 修改密码 |

### 10.2 扫描与曲目

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/scan` | 开始扫描（body: dir） |
| POST | `/api/scan/pause` | 暂停/继续 |
| GET | `/api/scan/status` | 扫描进度（running/paused/added/total/done/skipped） |
| GET | `/api/tracks` | 曲目列表（按 seq） |
| POST | `/api/tracks/delete` | 批量删除选中文件（body: {ids} → {failed,removed}） |
| POST | `/api/tracks/{id}/tags` | 写标签（含重命名联动） |
| GET | `/api/tracks/{id}/cover` | 取内嵌封面 |
| POST | `/api/tracks/{id}/cover` | 写封面 |
| GET | `/api/tracks/{id}/lrcfile` | 外挂歌词文件 |

### 10.3 刮削

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/scrape/sources` | 源列表与开关 |
| GET | `/api/scrape/search` | 指定源搜索（query + source） |
| POST | `/api/tracks/{id}/scrape` | 单曲刮削（source + 字段勾选） |
| POST | `/api/scrape/batch` | 批量刮削（ids + source + 字段），返回 jobId |
| POST | `/api/scrape/onekey` | 一键刮削（body: {ids, dedup}；dedup=true 先自动去重），返回 jobId |
| GET | `/api/lyrics` | 歌词搜索/获取 |

### 10.4 批量任务

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/jobs/{id}` | 任务进度（status/done/total/current/results/ok/fail/skip/stage/stageLabel） |
| GET | `/api/jobs` | 进行中任务列表（刷新/换设备恢复） |
| POST | `/api/jobs/{id}/cancel` | 取消任务（置 cancelled） |
| POST | `/api/convert` | 格式转换（target/bitrate/sampleRate/removeSource） |
| POST | `/api/fix-encoding` | 乱码修复（body: {ids}） |
| POST | `/api/convert-script` | 简繁转换（body: {ids, to: simp/trad}） |
| POST | `/api/identify` | 音频内容识别（异步构建指纹库后逐曲目识别） |
| POST | `/api/rename-clean` | 修正文件后缀（探测真实格式重命名） |
| POST | `/api/set-chorus` | 艺术家 ≥3 时改"合唱" |

### 10.5 去重

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/duplicates` | 重复分组（hash/format/tags；format/tags 组含 keepId 音质最佳） |
| POST | `/api/duplicates/remove` | 删除组内选中文件 |

### 10.6 设置 / 插件 / 日志

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/settings` | 设置（Key 脱敏返回） |
| POST | `/api/settings` | 保存设置/Key |
| GET | `/api/plugins` | 插件快照（含 config 恒非空、KeyConfigured） |
| POST | `/api/plugins` | 更新启停/配置（body: {name: {enabled?, config?}}） |
| GET | `/api/ffmpeg/status` | FFmpeg 路径与版本 |
| POST | `/api/ffmpeg/install` | 容器内一键安装 |
| GET | `/api/logs` | 操作日志列表 |
| POST | `/api/logs/delete` | 删除指定日志 |
| POST | `/api/logs/clear` | 清空日志 |

### 10.7 更新检查

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/version` | 缓存快照（current/latest/url/status） |
| GET | `/api/version/check` | 强制实时重查（手动检查用） |

响应 `status`：`ok` / `fail`（全部源失败）/ `unknown`（未检查）。前端据此区分"有新版 / 已是最新 / 检查失败"。

---

## 11. 部署

### 11.1 docker compose（推荐）

```yaml
services:
  lnh-musictag:
    image: ghcr.io/lahuolou/lnh-musictag:latest
    container_name: lnh-musictag
    ports:
      - "10248:10248"
    volumes:
      - /mnt/sata3-1/mp3:/music        # 音乐目录（需读写）
      - lnh_config:/config             # 配置卷（密码/Key/数据库）
    environment:
      - LNH_ADMIN_USER=admin           # 仅首次初始化
      - LNH_ADMIN_PASS=                # 留空则随机初始密码打日志
      - ACOUSTID_API_KEY=
      - LNH_INSTALL_FFMPEG=false       # true 时启动自动装 ffmpeg
    restart: unless-stopped
volumes:
  lnh_config:
```

### 11.2 关键环境变量

| 变量 | 作用 |
|------|------|
| `LNH_ADMIN_USER` / `LNH_ADMIN_PASS` | 首次启动初始化种子（之后改库） |
| `LNH_INSTALL_FFMPEG` | true/1/yes/on → entrypoint 启动前安装 ffmpeg |
| `LNH_CONFIG_DIR` | 非容器运行时的配置目录（默认 `/config`） |
| `ACOUSTID_API_KEY` 等 | 各 API Key 初始化种子 |

### 11.3 构建

```bash
cd web && npm ci && npm run build   # 前端产物 → web/dist
cd .. && go build -ldflags "-s -w" -o lnh-musictag .
# Docker（CI 已自动）：
docker build -t lnh-musictag .
```

---

## 12. 发布流程（CI/CD）

1. **改码**：项目根（唯一真源）修改代码；前端改完 `npm run build`。
2. **验证**：`go vet ./...` + `go test ./...` + `go build`；本地起服务用浏览器冒烟（登录/关键页/console 无错）。
3. **清理**：删除死代码/冗余代码，避免"屎山"；检查安全防护（CSRF/XSS/SQL 注入/路径穿越）。
4. **同步**：robocopy 到发布副本（唯一 `.git`），`git add -A && git commit`（提交信息含 bump 说明）。
5. **推送**：`git push origin main` → GitHub Actions 自动构建并推送 `ghcr.io/lahuolou/lnh-musictag:latest`。
6. **版本**：先递增 `main.go` 的 `AppVersion` 与 `VERSION` 文件再提交（否则更新检查检测不到新版）。
7. **Release**：`gh release create vX.Y.Z --notes-file ...`（tag 必须带 `v`）。
8. **打包**：重新打源码 zip（含 web/dist 与 Dockerfile/compose/entrypoint），校验 dist 完整后交付。

> 用户确认前不发布。修改完先交用户核验，确认后再推送/发 Release/重打 zip。

---

## 13. 更新检查机制

- **数据源**（任一成功即用）：① GitHub Releases API（最实时）；② jsDelivr CDN 读 `VERSION` 文件；③ raw.githubusercontent 读 `VERSION` 文件。
- **三态**：`ok` / `fail`（全部源失败）/ `unknown`（未查）。查询失败不再误报"已是最新"。
- **前端**：登录后自动检查一次，有新版弹 `UpdateModal`；手动「更新」强制实时重查。
- **版本比较**：语义化数值逐段比较（`parseVersion`），正确处理 `1.4.50 → 1.5.0`、`1.10.0 > 1.9.0`。

---

## 14. 常见问题排查

| 现象 | 排查方向 |
|------|----------|
| 更新后界面还是旧的 | 浏览器/代理缓存旧 index.html：强刷一次；index.html 已 `Cache-Control: no-cache` |
| 检查更新永远"已是最新" | `VERSION` 文件 / Release tag / `AppVersion` 三者未同步递增 |
| 插件页空白 | 旧版本 bug（config omitempty 导致前端 undefined）；升级镜像即可 |
| 刮削/转换任务换设备后看不到进度 | 前端登录后 `resumeJobs()` 按 jobId 恢复轮询；确认后端 `/api/jobs` 有任务 |
| 扫描慢 | 异步增量扫描默认开启；首次全量计算哈希属正常耗时 |
| 乱码筛选不出/误筛 | 乱码判定基于标签编码检测（GBK/UTF-8 错乱）；正常中文不应被标记；短串保守不判（防误判繁体） |
| 源搜索失败（status 500 / TLS） | 源接口上游变动或网络受限；auto 自动跳过失败源 |
| 需 Key 源搜不到 | 插件页显示 🔑 未配置 → 去「设置 → 数据库配置」填 Key |
| 容器内无 ffmpeg 无法转换 | compose 设 `LNH_INSTALL_FFMPEG=true` 或插件页「一键安装」或填宿主路径 |
| mp3 转换失败（Unknown encoder libmp3lame） | 精简版 ffmpeg 缺 mp3 编码器；换完整版 ffmpeg 或用 flac/wav 等格式 |
| 一键刮削误删文件 | 自动去重默认不勾选；勾选后按"艺术家+标题/同名多格式/SHA256"分组，保留音质最好的一首 |

---

## 15. 测试

- `go test ./...`：核心逻辑单测（版本比较、去重、刮削源、乱码修复、指纹、文件名解析等）。
- 本地 UI 冒烟：`%TEMP%\lnh_ui\lnh.exe` 起服务（配置目录指向测试库），浏览器验证登录、列表、编辑、批量、插件页渲染与 console 错误。
- 接口验证：curl 登录拿 Cookie 后逐接口检查（含 CSRF 头）。

---

## 16. 安全说明

- 密码 bcrypt 哈希；会话 Cookie HttpOnly+SameSite。
- 所有 SQL 参数化；文件路径严格校验；标签文本 XSS 净化。
- CSP：`default-src 'self'`；`script-src 'self' 'unsafe-inline'`（第三方插件 panel 内联脚本所需）；`frame-ancestors 'self'`（仅放行同源 iframe 嵌入）。
- 更新检查、刮削、识别等出站请求均带超时与 UA，避免滥用第三方接口。
- 建议部署时：容器内仅挂载音乐目录与配置卷；公网访问时置于反向代理后并启用 HTTPS。
