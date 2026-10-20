# 更新日志

版本号采用三段语义化 `主.次.修订`，与根目录 `VERSION` 文件、GitHub Release tag（`v<版本>`）保持一致；重大变更推进主版本，功能新增推进次版本，问题修复推进修订号。

---

## 1.0.5

修复音流（Subsonic 协议）「我的歌单」列表计数正确、但点进详情看不到任何歌曲、退回后计数归零的问题。

### Subsonic 网关

- `getPlaylist` 详情曲目改用标准 `<entry>` 子节点输出（此前误输出为 `<song>`）：音流等客户端按 `entry` 解析，旧写法导致详情页读不到曲目，列表计数却仍正确。
- 详情页 `songCount` 改为与实际成功返回的曲目数对齐，避免计数与可见条目矛盾。
- `sxForceArray` 为 `playlist` 节点补 `entry` 强制数组，单曲歌单也稳定返回数组。

### 流媒体：下载失败 / 转码后无法播放

- `/rest/download` 改回标准语义：始终直传原始文件（附件），不再按 `format`/`maxBitRate` 转码；客户端即使带 `format=mp3` 也直接下载原文件，避免转码失败被报为“下载失败”。
- 实时转码改为“先读到首个有效输出再写 200”：ffmpeg 启动失败、进程立即退出或零字节产出（解码/编码不支持该源）时回退直传原始文件，不再在已写 200 后吐出空流/截断流导致“无法播放”。
- 编码器按当前 ffmpeg 实际可用集合自适应选择（mp3：libmp3lame→mp3_mf→mp3；opus：libopus→opus；aac 内置），缺编码器时直接回退直传，不再硬编码一个缺失编码器而失败。
- 排空 ffmpeg stderr，避免错误缓冲写满导致进程阻塞。
- 码率参数同时兼容标准驼峰 `maxBitRate` 与小写 `maxBitrate`（URL query key 大小写敏感，只读小写会漏掉箭头等客户端的标准请求）。

## 1.0.4

修复同名不同艺人的元数据/歌词串歌（典型：EO2《内疚》被配上陈奕迅《内疚》的歌词、专辑与封面），并补齐 Subsonic 专辑节点标题。

### 刮削：艺人匹配强校验，杜绝同名异人顶替

- 新增艺人归一化与匹配（`ArtistSimilar`）：小写、去括号备注、拆分 `&`/feat./ft./with 等并列、去标点，按相等/包含打分。
- 多源融合 `fuseMetadata` 引入艺人匹配分：目标艺人非空时，艺人匹配候选整体排在异名候选之前；艺术家字段、标题、代表源（封面/源ID/专辑ID）均优先同艺人候选，异名同名曲目不再覆盖标题/艺人/专辑/封面。
- 歌词兜底（QQ/网易/lrclib）改为取多候选并按「艺人匹配×2 + 标题匹配」择优；目标艺人非空时艺人不符的候选直接淘汰（宁缺毋滥），lrclib 结果按自带 `artistName`/`trackName` 同样校验。
- 封面兜底 `FetchCoverFallback` 增加艺人约束，只采用艺人匹配候选的封面，避免短/纯标题命中异名专辑封面。

### Subsonic 网关

- 专辑节点（`getArtist`/`getAlbumList`/`getAlbum`）补 `title` 属性：此前只写 `name`，部分客户端（音流）只读 `title`，缺失时专辑名渲染为占位“?”。
- 艺人头部 `coverArt` 改引用曲目 ID（原误用专辑 ID），与专辑节点封面口径一致。

### 说明

- 本次修复作用于**新刮削**；此前已被错配写盘的旧曲目，重新刮削一次即可纠正，不会自动回改历史标签。

## 1.0.3

将 1.0.2 在 Subsonic 网关补齐的评分、真实计数、歌单能力，同步覆盖到 Navidrome、Jellyfin、Emby 三套协议，并修复歌单标识匹配。

### 认证统一为流媒体密码

- 四个协议（Subsonic/OpenSubsonic、Navidrome、Jellyfin、Emby）统一只校验流媒体密码 `stream_pass`：移除管理员密码认证分支与“任意/残留密码宽松放行”，未配置 `stream_pass` 时四协议一律拒绝连接；用户名仅作占位。
- Subsonic 明文 `p` 精确匹配 `stream_pass`，token 仍为 MD5(`stream_pass`+salt)；ND `/auth/login`、JF/Emby `AuthenticateByName` 的 `Password` 精确匹配 `stream_pass`，派生 Bearer/Access token 也只以 `stream_pass` 为基值。
- 修复 Emby 评分写 `POST /Users/{uid}/Items/{id}/Rating` 被通用曲目读取吞掉、`POST /Playlists` 建歌单被 GET 歌单列表吞掉的问题（写动作路由前置于通用读取/列表之前）。

### 概览统计

- 补齐 Subsonic `getStatistics`：回传真实 `songCount/albumCount/artistCount/playlistCount`，解决概览页专辑/歌手/歌单显示“未知”。

### 评分（四协议共享 `streaming_ratings`）

- Navidrome 歌曲 DTO 的 `rating` 不再恒为 0，回传与 Subsonic 共享的星级评分。
- Jellyfin / Emby 歌曲 `UserData.Rating` 回传共享评分；新增评分写端点 `POST /Users/{uid}/Items/{id}/Rating?Rating=0..5`（`Rating=0` 清除），仅对真实曲目生效。

### 歌单（Jellyfin / Emby）

- 新增建歌单 `POST /Users/{uid}/Playlists` / `POST /Playlists`（body `{"Name":..,"Ids":[..]}`），追加去重。
- 新增加歌 `POST /Playlists/{id}/Items?Ids=..`、删歌 `DELETE /Playlists/{id}/Items?EntryIds=..`，与 Subsonic/Navidrome 共享 `streaming_playlists`。
- 修复 `playlistByID`：存储原生 ID、展示前缀 `pl-<id>`、裸 ID 三种标识均可匹配，解决按展示 ID 定位歌单误报“Playlist not found”。

### 说明

- 经实测核实：服务端标签中文与 JSON/XML 转义均正常，历史“乱码/引号破坏 JSON”系控制台按 ANSI 误读 UTF-8 落盘文件所致，未改动显示层以免误伤正常短名。

## 1.0.2

修复 Subsonic/OpenSubsonic 网关在音流（Musiver）客户端实机暴露的四项问题。

### 播放与评分

- 实现 `setRating`（0~5 星，`rating=0` 清除）：评分持久化到配置，并在歌曲节点回传 `userRating`；收藏状态回传 `starred`，不再返回 `setRating is not implemented` 报错。

### 浏览计数

- `getIndexes` 的歌手节点补齐真实 `songCount` / `trackCount` / `albumCount`，不再因缺字段被客户端兜底显示固定“50 首”。
- `getArtists`、`getAlbumList` / `getAlbumList2`、`getPlaylists` 根节点补充 `total`（全量计数，不受分页截断），客户端服务器概览不再显示专辑/歌手/歌单“未知”。

### 歌单

- `createPlaylist` / `updatePlaylist` 兼容追加参数别名 `songId`（标准为 `songIdToAdd`），并对曲目去重，解决已有歌单追加后仍显示 0 首、点进去为空的问题。

## 1.0.1

### 许可证

- 协议由 MIT 改为 **GPL-3.0**（`LICENSE` 全文、README、关于页中英文同步）。

### 刮削：多源按字段融合

- 自动批量/一键补全不再整条采用单一来源：新增 `fuseMetadata`，对每个 ID3 字段独立从各源候选按标题匹配度（Dice 二元组，≥0.35 阈值）与完整度择优后合并。
- 标题取匹配最高；艺术家/专辑/年代/曲目号/作曲/作词/语言/专辑类型/注释/碟号/总轨/总碟各取匹配高且非占位（Unknown/未知/Various）的最佳值；专辑艺术家优先 MusicBrainz；流派跨源去重合并；ReleaseMBID 从 MB 候选继承。
- 先对 MusicBrainz 候选拉取发行详情再参与融合；删除无调用的旧 `pickBest`。
- 手动在搜索结果中点选单条应用的行为保持不变。

### 文档

- README 改为以多源刮削为主线介绍；开发文档补充融合器位置。

## 1.0.0

首个版本。本地音乐库标签管理 + 内置四协议音乐网关。

### 标签管理

- 异步可暂停扫描本地目录，曲目持久化到 SQLite，重启保留；支持按乱码 / 繁体 / 无歌词 / 无海报 / 待识别筛选。
- 编辑标题、艺术家、专辑、年份、音轨号、流派等字段，管理内嵌封面与歌词并写回文件。
- 批量处理：批量补全、乱码（GBK/拉丁误码）修复、繁简转换、文件名清理、重复曲目检测与删除。
- 多源聚合刮削（国内网易云/QQ/酷狗/酷我/咪咕/B站/汽水/波点/5sing，海外 MusicBrainz/iTunes/ListenBrainz/Deezer/Last.fm/Spotify/Discogs/Jamendo）；需 Key 源配好后启用。
- 可选 AcoustID 音频指纹识别；可选 FFmpeg 格式转换。

### 音乐网关（单端口复用同一曲库）

- Subsonic / OpenSubsonic（`/rest`）：浏览、搜索、stream/download、封面、歌词、歌单 CRUD、收藏、scrobble、播放队列、书签。
- Navidrome 风格 REST（`/auth`、`/api`）：登录、曲目/专辑/艺人/歌单/收藏读写、搜索、统计、歌词、流与封面。
- Jellyfin（根路径与 `/jellyfin`）：服务信息、登录、曲目/专辑/艺人/歌单/封面/播放信息/歌词/收藏。
- Emby（`/emby`）：服务信息、登录、曲目/专辑/歌单/封面/直传与按需转码/歌词。
- 直传播放、下载、浏览、封面、歌单不依赖 FFmpeg；仅在客户端请求目标格式且 FFmpeg 就绪时实时转码，否则回退原始直传。

### 插件

- 内置刮削源、FFmpeg、AcoustID、音乐网关统一在插件页启停配置；开关意愿（enabled）与依赖就绪（available）分离，未探测到二进制 / 未配 Key 时禁用并回退，避免开着却用不了。
- 支持导入第三方 ZIP 插件包（声明 + 按平台入口 + 可选 Web 面板），含包大小、解压总量、zip-slip 与符号链接防护。

### 界面与安全

- Vue 3 内嵌单二进制；中文 / 英文双语、明暗主题；操作日志、版本检查。
- 单用户自托管；后台会话 + CSRF + 请求体上限；日志不记录完整 query、认证头或登录正文；API Key 下发前端脱敏。
