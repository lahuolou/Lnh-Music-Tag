# 内置音乐网关：协议覆盖、认证与限制

音乐网关是主程序的一个内置能力，与标签管理共用同一 HTTP 端口（默认 `10248`）和同一份扫描曲库，在「插件 → 音乐统一网关」中开关。它是面向播放器的**协议适配子集**，不是完整的 Subsonic、Navidrome、Jellyfin 或 Emby 服务器；以本文列出的端点为准。

## 1. 入口与认证

四个协议**统一只认流媒体密码 `stream_pass`**（插件「音乐统一网关」中设置），不校验 Web 管理员密码，也不接受空/残留密码；未配置 `stream_pass` 时四协议一律拒绝连接。用户名仅作占位，可任意填写。

| 协议 | 路由入口 | 认证 |
|---|---|---|
| Subsonic / OpenSubsonic | `/rest/*` | `p` 明文精确匹配 `stream_pass`，或 `t`(token)=MD5(`stream_pass`+`s`(salt)) |
| Navidrome 风格 REST | `POST /auth/login`、`POST /auth/token`、`GET /ping`，其后 `/api/*` | 登录体 `password` 精确匹配 `stream_pass` 得 Bearer token，后续 `Authorization: Bearer <token>` |
| Jellyfin | 根路径 `/System`、`/Users`、`/Items`、`/Artists`、`/Albums`、`/Playlists`、`/Audio`、`/Library` 等（另挂 `/jellyfin/*`） | `POST /Users/AuthenticateByName` 的 `Password` 精确匹配 `stream_pass`，之后 `X-Emby-Token`（兼容 MediaBrowser token / Authorization 头） |
| Emby | `/emby/*` | `POST /emby/Users/AuthenticateByName` 的 `Password` 精确匹配 `stream_pass`，之后 `X-Emby-Token` |

单用户模型：所有客户端共用同一份收藏、歌单、播放队列，不做多用户隔离。

### FFmpeg 门控与转码回退

- 浏览、封面、歌单、收藏、直传播放与下载**都不依赖 FFmpeg**。
- FFmpeg 只用于：① 流请求带 `format=mp3/aac/opus`（或 Emby HLS）时的实时转码；② Web「格式转换」批量任务。
- `ffmpegActive` 同时检查插件开关与二进制是否探测到，任一不满足即**回退原始文件直传**，不中断播放。插件页探测不到二进制时开关自动关闭且不可强行开启。

## 2. Subsonic / OpenSubsonic（`/rest`）

### 已实现

| 类别 | 方法 |
|---|---|
| 连通 | `ping`、`getLicense`、`getOpenSubsonicExtensions`（只通告真实实现的扩展） |
| 曲库浏览 | `getMusicFolders`、`getIndexes`、`getArtists`、`getArtist`、`getAlbum`、`getSong`、`getMusicDirectory`、`getGenres`、`getArtistInfo(2)`、`getAlbumInfo(2)`、`getStatistics`（真实歌曲/专辑/歌手/歌单计数） |
| 列表/推荐 | `getAlbumList`、`getAlbumList2`、`getRandomSongs`、`getNewestSongs`、`getSongsByGenre`、`getTopSongs`、`getSimilarSongs(2)`；`getAlbumList` 的 `random/newest/frequent` 有真实排序语义 |
| 搜索 | `search`（简单字符串） |
| 媒体 | `stream`（默认直传、支持 HTTP Range，按需转码）、`download`（始终直传原文件）、`getCoverArt` |
| 歌词 | `getLyrics`、`getLyricsBySongId`（嵌入歌词或同名 `.lrc`） |
| 歌单 | `getPlaylists`、`getPlaylist`、`createPlaylist`、`updatePlaylist`、`deletePlaylist`（持久化到数据库） |
| 收藏 | `star`、`unstar`、`getStarred`、`getStarred2`（曲目/专辑/艺人，校验 ID） |
| 评分 | `setRating`（0~5 星，`rating=0` 清除，持久化并以 `userRating` 回传） |
| 播放状态 | `scrobble`、`getNowPlaying`、`getPlayQueue`、`savePlayQueue` |
| 书签 | `getBookmarks`、`createBookmark`、`deleteBookmark` |
| 用户 | `getUser`、`getUserList`（单用户视图） |

### 明确不支持（返回协议失败或空占位，不伪装成功）

`changePassword`、`createUser/updateUser/deleteUser`、`createShare/deleteShare/getShares`、`startScan`、`getAvatar/uploadAvatar/deleteAvatar`、`jukeboxControl`（不连接音频输出）、`getPodcasts/getNewestPodcasts`、`getInternetRadioStations`、`getDownloads`。不声明未实现的 OpenSubsonic 扩展（如 `transcodeOffset`）。

## 3. Navidrome 风格 REST（`/api`）

| 类别 | 路由 |
|---|---|
| 认证/用户 | `POST /auth/login`、`POST /auth/token`、`GET /ping`、`GET /api/me`（ND Bearer 优先，否则回退 Web 会话） |
| 曲目 | `GET /api/song`、`/api/song/{id}`、`/api/song/{id}/stream`、`/api/stream/{id}`、`/api/coverart/{id}` |
| 专辑 | `GET /api/album`、`/api/album/{id}`、`/api/album/{id}/songs`、`/api/album/{id}/coverart` |
| 艺人 | `GET /api/artist`、`/api/artist/{id}`、`/api/artist/{id}/albums`、`/api/artist/{id}/coverart`（聚合首曲封面） |
| 歌单 | 读 `GET /api/playlist`、`/api/playlist/{id}`、`/api/playlist/{id}/songs`；写 `POST /api/playlist`、`PUT/DELETE /api/playlist/{id}`、`POST/DELETE /api/playlist/{id}/songs`（与 Subsonic 共用存储） |
| 收藏 | `GET /api/starred`；写 `PUT/DELETE /api/starred/{id}`（204）；曲目 DTO `rating` 回传与 Subsonic 共享的星级（ND 原生无公开星级写端点，评分以收藏/星形表达） |
| 其他 | `/api/search`、`/api/search/{songs|albums|artists}`、`/api/genres`、`/api/albumlist/{type}`、`/api/songlist/{type}`、`/api/library/scanstatus`、`/api/library/statistics`、`/api/statistics`、`/api/nowplaying`、`/api/lyrics/{songId}` |

仅覆盖上述旧风格路由子集，**不是**官方 `/api/v1` OpenAPI 完整实现。

## 4. Jellyfin（根路径 / `/jellyfin`）

| 类别 | 路径 |
|---|---|
| 服务信息 | `/System/Info/Public`、`/System/Info`、`/Branding/Configuration` |
| 登录/用户 | `/Users/AuthenticateByName`、`/Users/Me`、`/Users/{id}/Views` |
| 曲库 | `/Items`、`/Items/{id}`、`/Users/{id}/Items`、`/Users/{id}/Items/Latest`、`/Artists`、`/Albums`、`/Playlists`、`/Genres`、`/Library/MediaFolders` |
| 媒体/封面 | `/Items/{id}/PlaybackInfo`、`/Audio/{id}/universal`、`/Audio/{id}/stream`、`/Items/{id}/Images/*`、`/Artists/{id}/Images/Primary`、`/Audio/{id}/Images/Primary` |
| 歌词 | `/Audio/{id}/Lyrics`、`/Items/{id}/Lyrics`、`/Users/{uid}/Items/{id}/Lyrics` |
| 收藏/评分 | `POST/DELETE /Users/{id}/FavoriteItems/{id}`（收藏）；`POST /Users/{uid}/Items/{id}/Rating?Rating=0..5`（星级，`Rating=0` 清除，与 Subsonic 共享 `streaming_ratings`，曲目 `UserData.Rating` 回传）；`/Sessions/Playing*` |
| 歌单写 | `POST /Users/{uid}/Playlists` 或 `POST /Playlists`（body `{"Name":..,"Ids":[..]}`，去重建歌单）；`POST /Playlists/{id}/Items?Ids=..` 追加；`DELETE /Playlists/{id}/Items?EntryIds=..` 移除；与 Subsonic/ND 共用 `streaming_playlists` |

限制：不实现完整设备会话、策略、用户管理、媒体源协商、服务端转码会话；`Library/Refresh` 明确返回不支持。

## 5. Emby（`/emby/*`）

覆盖服务信息、`/Users/AuthenticateByName`、用户/视图、`/Items`、`/Albums`、`/Playlists`、`/Library/MediaFolders`、三类 `Images/Primary` 封面、`/Items/{id}/File`（直传/按需转码）、`/Audio/{id}/stream`、播放信息、歌词与基础播放事件。`/Items/{id}/File?format=mp3` 与 HLS `main.m3u8` 仅在 FFmpeg 就绪时转码，否则回退直传。与 Jellyfin 共用同一套写端点：收藏 `FavoriteItems`、星级 `Items/{id}/Rating`、歌单建/加/删 `Playlists*`，存储与其余协议共享。未实现完整设备会话、策略、管理与同步。

## 6. 通用限制

- 单用户，无多用户隔离；播放次数/当前播放非完整可查询历史。
- 默认原始音频直传，不做服务端强制转码；仅在 FFmpeg 就绪且客户端请求目标格式时转码。
- 媒体 Item ID 为内部形式，严格按 UUID 解析的客户端可能不兼容，需实机核验。
- 无封面/无歌词的曲目对应封面可能 404、歌词为空，属数据缺失的正常结果。
- 日志不记录完整 query、认证头或登录正文。

## 7. 客户端验收口径

HTTP 200 或服务器返回 API 版本号不等于播放器兼容；每个客户端至少要在目标设备实机跑通：登录、分页列表、中文搜索、封面、可拖动的播放流（含 Range）、歌词（如客户端支持）、歌单与收藏。状态分四档：`未测试` / `接口通过`（仅 HTTP 合约）/ `部分通过`（能登录浏览但有明确失败）/ `实机通过`（完整走通登录、浏览、播放、拖动、封面、歌词）。`scripts/gateway-smoke.py` 只做前者的只读契约检查，需配合实机验收。
