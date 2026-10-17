# Lnh-Music-Tag 内置音乐统一网关

本功能以内置 HTTP 路由复用 Lnh-Music-Tag 已经扫描入库的音乐曲库，**不要求单独运行第二个服务**。同一端口（默认 `10248`）同时暴露四套兼容接口：

| 协议 | 入口前缀 | 面向客户端 |
|---|---|---|
| Subsonic / OpenSubsonic | `/rest` | DSub、Tempo、Ultrasonic、Subtracks、音流等 Subsonic 客户端，以及所有支持 Subsonic 协议的应用 |
| Jellyfin | `/`（根路径），也兼容 `/jellyfin` | Jellyfin 官方客户端、JellyBox、Findroid、Finamp 等 |
| Emby | `/emby` | Emby 客户端 |
| Navidrome 原生 REST | `/auth/login` + `/api/*` | 直接按 Navidrome REST 接入的应用 |

> 协议边界：Plexamp 依赖 Plex 专有 API，本项目未实现 Plex 协议。其他播放器是否可用取决于它实际支持的服务端协议与 API 子集，不能仅凭协议名称宣称兼容。

## 1. 启用与关闭

在管理界面「插件」页找到「音乐统一网关（Subsonic / Jellyfin / Emby / Navidrome）」开关。关闭后上述网关路由返回 404，标签管理本体不受影响。

## 2. 认证

### Subsonic / OpenSubsonic（`/rest`）

- 用户名、密码使用 Lnh-Music-Tag 管理员账号；支持明文 `p=`、`enc:` 十六进制密码，以及 `t`/`s` MD5 token 认证。
- MD5 token 模式需要为服务进程配置与客户端一致的流凭证：环境变量 `LNH_STREAM_PASS`（管理员密码在数据库中为哈希存储，无法反向计算 Subsonic token，因此 token 模式单独使用流凭证）。
- 默认 XML 响应；追加 `?f=json` 返回 OpenSubsonic 风格 JSON（数值字段为 JSON 数字、空列表为 `[]`）。

### Jellyfin / Emby

- `POST /Users/AuthenticateByName`（Emby 为 `/emby/Users/AuthenticateByName`）完成认证，响应返回 `AccessToken`。
- 后续请求通过 `X-Emby-Token`、`X-MediaBrowser-Token` 或兼容的 `Authorization: MediaBrowser Token="..."` 头携带令牌。

### Navidrome REST

- `POST /auth/login`（body `{"username","password"}`）返回 Bearer `token` 与 `subsonicToken/subsonicSalt`；后续 `/api/*` 用 `Authorization: Bearer <token>`。

> 不要把管理端口直接暴露到公网。建议先在可信局域网测试，远程访问用 HTTPS / VPN / 反向代理保护。

## 3. Subsonic / OpenSubsonic 已实现接口

- **连通性与能力**：`ping`、`getLicense`、`getOpenSubsonicExtensions`、`getMusicFolders`、`getScanStatus`、`startScan`
- **曲库浏览**：`getIndexes`、`getArtists`、`getArtist`、`getArtistInfo`、`getArtistInfo2`、`getAlbum`、`getAlbumInfo`、`getAlbumInfo2`、`getAlbumList`、`getAlbumList2`、`getMusicDirectory`、`getGenres`
- **曲目与搜索**：`getSong`、`search`、`search2`、`search3`、`getRandomSongs`、`getNewestSongs`、`getSongsByGenre`、`getTopSongs`、`getSimilarSongs`、`getSimilarSongs2`（同歌手/同专辑/同流派打分推荐）
- **媒体**：`stream`、`download`、`getCoverArt`、`getLyrics`、`getLyricsBySongId`（内嵌歌词优先，其次同目录 `.lrc`，带时间戳时输出 structuredLyrics）
- **播放与统计**：`scrobble`、`nowPlaying`、`star`、`unstar`、`setRating`
- **播放列表**：`getPlaylists`、`getPlaylist`、`createPlaylist`、`updatePlaylist`、`deletePlaylist`（持久化到配置库）
- **收藏**：`getStarred`、`getStarred2`（持久化）
- **用户**：`getUser`（完整 user DTO）、`getUserList`、`changePassword`、`createUser`、`updateUser`、`deleteUser`（单用户服务器，写操作返回 ok）
- **书签**：`getBookmarks`、`createBookmark`、`deleteBookmark`（持久化到配置库 `streaming_bookmarks`）
- **播放队列**：`getPlayQueue`、`savePlayQueue`（持久化到配置库 `streaming_playqueue`）
- **分享**：`getShares`、`createShare`、`deleteShare`（单用户本地库不生成外部分享链接，返回合法空列表）
- **电台/播客/下载**：`getInternetRadioStations`、`getPodcasts`、`getNewestPodcasts`、`getDownloads`（无源时返回合法空数组）
- **点唱机**：`jukeboxControl`（`status`/`get` 返回合法状态，其余动作返回 ok）
- **头像**：`getAvatar`（无头像 404）、`uploadAvatar`、`deleteAvatar`

所有无源列表在 JSON 下输出 `[]` 而非空对象，避免严格解析器把 `{}` 当数组解析失败。

## 4. Jellyfin / Emby 已实现接口

两套协议共用一套适配逻辑，Emby 仅增加 `/emby` 前缀与少量字段差异。

- **服务信息**：`System/Info/Public`、`System/Info`、Branding
- **登录**：`Users/AuthenticateByName`、`Users/Me`、`Users/{id}/Views`
- **曲库读取**：`Items`、`Users/{userId}/Items`、`Items/MusicAlbums`、`Artists`、`Artists/{id}/Albums`、`Genres`、`Library/VirtualFolders`
- **媒体信息**：`Items/{id}`、`Items/{id}/PlaybackInfo`
- **封面**：`Items/{id}/Images/Primary`
- **音频**：`Audio/{id}/stream`、Jellyfin `Audio/{id}/universal`、Emby `Audio/{id}/file`
- **歌词**：`Audio/{id}/Lyrics`、`Items/{id}/Lyrics`、`Users/{uid}/Items/{id}/Lyrics`，统一返回 Jellyfin LyricDto（`{"Lyrics":[{"Start":ticks,"Text":...}],"Metadata":{...}}`，LRC 时间戳转 100ns ticks）
- **收藏**：`Users/{userId}/Items/{id}/Favorite`（增删）、`Users/{userId}/Items/IsFavorite`
- **播放事件**：`Sessions/Playing`、`Sessions/Playing/Progress`、`Sessions/Playing/Stopped` 基础接收

## 5. Navidrome 原生 REST 已实现接口

- `POST /auth/login`；`GET /api/song`、`/api/song/{id}`、`/api/song/{id}/stream`、`/api/stream/{id}`
- `GET /api/album`、`/api/album/{id}`、`/api/album/{id}/songs`、`/api/album/{id}/coverart`
- `GET /api/artist`、`/api/artist/{id}`、`/api/artist/{id}/albums`
- `GET /api/playlist`、`/api/playlist/{id}`、`/api/playlist/{id}/songs`
- `GET /api/coverart/{id}`、`/api/search`（含 `q=` 关键词）、`/api/search/songs|albums|artists`
- `GET /api/starred`、`/api/genres`、`/api/albumlist/{type}`、`/api/songlist/{type}`、`/api/library/scanstatus`、`/api/nowplaying`、`/api/lyrics/{id}`（`synced` 按 LRC 时间戳判定）
- 支持 `_sort`/`sort`、`_order`/`order`、`limit`、`offset` 分页。

## 6. 媒体与 ID 规则

- 曲目 ID 直接使用曲库 `Track.ID`；艺人/专辑 ID 由标签内容生成稳定摘要（`ar-*`/`al-*`）。扫描后修改艺人/专辑标签会改变聚合 ID，正在使用的客户端需刷新。
- 音频流通过 `http.ServeContent` 输出，支持 HTTP Range（拖动进度返回 206）。
- 封面读取音频内嵌图片；无封面曲目封面接口返回空/404 属正常。
- 歌词优先内嵌 LYRICS 标签，其次同目录同名 `.lrc`。

## 7. 本地验证流程

```bash
go test ./...
npm --prefix web ci && npm --prefix web run build
go build ./...
```

手动回归清单：

1. 管理界面登录后扫描曲库，曲目可见。
2. `GET /rest/ping.view?u=用户名&v=1.16.1&c=smoke` 返回 `status=ok`。
3. Subsonic 客户端登录，确认艺人、专辑、歌曲、封面、歌词、音频流可读，Range 请求返回 206。
4. Jellyfin / Emby 客户端分别验证服务发现、登录、浏览、封面、播放、拖动与歌词。
5. Navidrome REST：`/auth/login` 取 token，`/api/song|album|artist|search` 返回正确数量与字段。
6. 插件页关闭网关，确认 `/rest`、`/System/Info/Public`、`/emby/System/Info/Public`、`/api/song` 返回 404。

## 8. 已知限制

1. 单用户服务器：`createUser/updateUser/deleteUser`、`changePassword` 仅返回 ok，不暴露独立播放器账号。
2. 转码（实时音频格式协商/Transcoding）未实现，流为原始文件直出。
3. Jellyfin/Emby 的设备会话管理、用户策略、库刷新、媒体源完整协商、同步播放、复杂分页过滤仍为基础实现。
4. 外部分享链接、播客订阅、互联网电台节目无源，返回合法空列表。
5. 播放列表/收藏/书签/队列持久化在本地配置库，不做多用户同步。
