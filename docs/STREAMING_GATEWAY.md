# 音乐统一网关：协议、认证与接入

音乐网关是主程序的一个内置能力，与标签管理共用同一 HTTP 端口（默认 `10248`）和同一份扫描曲库，在「设置 → 插件 → 音乐统一网关」中开关。它是面向播放器的**协议适配子集**，不是完整的 Subsonic、Navidrome、Jellyfin 或 Emby 服务器；能力边界以本文为准。

---

## 1. 开启前提（强制密码）

> **网关强制要求配置流媒体密码（`stream_pass`）。**
> 未配置密码时：插件列表显示红色警告「未设置流媒体密码」，开关被禁用；即使强行调用 API 也会被拒绝开启。

配置位置：设置 → 插件 → 音乐统一网关 → 流媒体密码。

兼容旧部署：未配置 `stream_pass` 时可读取环境变量 `LNH_STREAM_PASS` 作为兜底。

---

## 2. 认证

四种协议统一使用 **管理员账号 + 流媒体密码** 认证：

- Subsonic：`?u=<admin>&p=<stream_pass>`（支持 `p` 明文、`t/s` token 与 `apiKey` 三种）
- Navidrome 兼容：`/auth/login` 提交 `{username, password}` 获取 token，后续请求带 `x-nd-authorization`
- Jellyfin / Emby：`/auth/login` 或 `?api_key=` 等标准方式

认证失败统一返回协议标准错误（如 Subsonic `{"subsonic-response":{"status":"failed","error":{"code":40,...}}}`）。

---

## 3. 协议与端点

### 3.1 Subsonic / OpenSubsonic

基础 URL：`http://IP:10248/rest`（示例请求须带 `v` 版本号，如 `v=1.16.1`、`c` 客户端名、`f=json|xml`）

已实现的端点（按能力分组）：

**浏览与索引**
`ping`、`getLicense`、`getMusicFolders`、`getIndexes`、`getArtists`、`getArtist`、`getAlbum`、`getAlbumList2`、`getSong`、`getMusicDirectory`、`getGenres`、`getSongsByGenre`、`getRandomSongs`、`getTopSongs`、`getNewestSongs`、`getSimilarSongs`、`getStarred`、`getStarred2`、`search` / `search2` / `search3`、`getAlbumInfo`、`getArtistInfo`、`getCoverArt`

> `getAvatar` 不支持（单用户服务器返回明确错误）。

**播放与流媒体**
`stream`（直传/转码，支持 `format`、`maxBitRate`、`timeOffset`）、`download`（原始文件直传）、`getLyrics`、`getOpenSubsonicExtensions`

**播放列表 / 收藏 / 队列**
`getPlaylists`、`getPlaylist`、`createPlaylist`、`updatePlaylist`、`deletePlaylist`、`star`、`unstar`、`setRating`、`getPlayQueue`、`savePlayQueue`、`getBookmarks`、`createBookmark`、`deleteBookmark`、`getShares`、`createShare`、`deleteShare`

**统计与状态**
`getNowPlaying`、`scrobble`、`getStatistics`、`getScanStatus`、`startScan`、`changePassword`、`getUser`、`getUserList`、`createUser`、`uploadAvatar`、`getInternetRadioStations`、`getPodcasts`、`getNewestPodcasts`、`jukeboxControl`、`jukeboxPlaylist`

### 3.2 Navidrome 兼容子集

基础 URL：`http://IP:10248`

| 端点 | 说明 |
|---|---|
| `POST /auth/login` | 登录换取 token |
| `POST /auth/token` | 刷新 token |
| `GET /ping` | 健康检查 |
| `GET /api/song`、`/api/song/{id}`、`/api/song/{id}/stream` | 歌曲与流媒体 |
| `GET /api/album`、`/api/album/{id}`、`/api/album/{id}/songs`、`/api/album/{id}/tracks`、`/api/album/{id}/coverart` | 专辑 |
| `GET /api/artist`、`/api/artist/{id}`、`/api/artist/{id}/albums`、`/api/artist/{id}/coverart`、`/api/artist/{id}/tracks`、`/api/artist/{id}/image` | 艺术家 |
| `GET /api/playlist`、`/api/playlist/{id}`、`/api/playlist/{id}/songs`、`/api/playlist/{id}/tracks` | 播放列表 |
| `GET /api/search`、`/api/search/songs`、`/api/search/albums`、`/api/search/artists` | 搜索 |
| `GET /api/starred`、`/api/starred/{id}`、`/api/genres`、`/api/albumlist/{type}`、`/api/songlist/{type}` | 浏览 |
| `GET /api/library/scanstatus`、`/api/library/statistics`、`/api/statistics`、`/api/nowplaying` | 状态 |
| `GET /api/lyrics/{songId}` | 歌词 |
| `GET /api/stream/{id}`、`/api/coverart/{id}`、`/api/coverArt`、`/api/coverArt/{id}` | 媒体资源 |

### 3.3 Jellyfin

基础 URL：`http://IP:10248/jellyfin`（兼容根路径，即 `http://IP:10248/System/Info` 等也可用）

已适配的端点族（JSON 响应，播放器按 Jellyfin 客户端标准发现）：

`/System/Info`、`/Users`、`/Users/{id}`、`/Items`、`/Items/{id}`、`/Items/{id}/PlaybackInfo`、`/Artists`、`/Artists/{id}`、`/Albums`、`/Albums/{id}`、`/Playlists`、`/Playlists/{id}`、`/Audio/{id}/stream*`、`/Genres`、`/Sessions`、`/Library/...`、`/Branding/...`

### 3.4 Emby

基础 URL：`http://IP:10248/emby`，端点与 Jellyfin 适配族基本对应（`/emby/System/Info`、`/emby/Users`、`/emby/Items` 等）。

---

## 4. 客户端接入示例

### 音流（SoundFlow）/ Archer
- 服务器类型选 **Subsonic**，地址填 `http://IP:10248/rest`
- 账号：管理员账号；密码：流媒体密码

### StreamMusic / 箭头音乐
- 协议选 **Subsonic**，地址填 `http://IP:10248/rest`，账号：管理员账号
- 认证方式二选一：
  - **API 密钥认证**（推荐）：API 密钥填网关的 Subsonic token（登录 Web 后设置页可见，或等于 `md5(stream_pass + "lnh-navidrome")`）
  - **用户名/密码认证**：密码填流媒体密码

### Subtracks
- 地址填 `http://IP:10248/rest`，账号：管理员账号，密码：流媒体密码
- 保持 **Token authentication** 开启（默认推荐）

### Jellyfin 官方 App
- 服务器地址填 `http://IP:10248/jellyfin`（或根地址 `http://IP:10248`）
- 账号/密码同上

### Emby 官方 App
- 服务器地址填 `http://IP:10248/emby`
- 账号/密码同上

### Navidrome 官方 App
- 服务器地址填 `http://IP:10248`
- 账号/密码同上

---

## 5. 行为说明

- **流媒体**：优先直传原始文件；客户端请求转码（`format`/`maxBitRate` 与源不匹配）时，经 FFmpeg 转码后以 HTTP 流式响应，探测失败自动回退直传
- **歌词**：优先内嵌歌词与同目录 `.lrc`；Subsonic 兼容 `getLyrics` / `structuredLyrics`（OpenSubsonic 扩展）
- **播放统计**：`scrobble` 记录播放次数，`getTopSongs` / `getFrequent` 等据此排序；`getNowPlaying` 展示当前播放状态
- **收藏与评分**：`star` / `setRating` 写回曲目标签与库内状态
- **转码缓存**：编解码探测结果带 10 分钟 TTL 缓存，失败不缓存，避免高并发重复探测
- **并发**：播放状态与「最近访问」记录使用独立锁，高并发流媒体不互相阻塞

---

## 6. 常见问题

| 现象 | 处理 |
|---|---|
| 客户端提示认证失败 | 确认用的是**流媒体密码**（不是 Web 登录密码）；确认网关已开启且已设置密码 |
| 开关无法开启 | 先到插件页填写流媒体密码（强制要求） |
| 播放器搜不到服务器 | 确认端口已放行、地址带对协议前缀（`/rest`、`/jellyfin`、`/emby` 或根路径） |
| 转码失败 | 确认 FFmpeg 已安装（插件页探测状态）；无 FFmpeg 时流媒体回退直传 |
| 部分端点 404 | 网关是协议子集，未适配的端点会返回 404/错误；可在 GitHub Issues 提出需求 |

---

## 7. 客户端兼容性实测记录

> 测试环境：雷电模拟器（Android）+ 局域网网关 `192.168.100.29:10248`，网关版本 v1.0.10。
> 认证凭据：`admin` / 流媒体密码（明文 `p`、`t/s` token、`apiKey` 三种均已验证）。

| 客户端 | 协议 | 认证方式 | 结果 | 备注 |
|---|---|---|---|---|
| 音流（cn.aqzscn.stream_music） | Navidrome | Bearer token | ✅ 全通 | 艺术家/专辑/歌词/播放/搜索均正常 |
| Ultrasonic（org.moire.ultrasonic） | Subsonic | 明文 `p` | ✅ 全通 | 拼音分组艺术家列表、FLAC 码率显示、播放正常 |
| DSub（github.paroj.dsub2000） | Subsonic | `t/s` token | ⚠️ API 全通 | 网关侧 getAlbumList/getUser 全 200；客户端本地明文口令弹窗需手动确认 |
| StreamMusic（com.sumsg.musichub，箭头音乐） | Subsonic | `apiKey` | ✅ 全通 | 艺术家网格、歌手详情、歌词、播放正常；**依赖网关 apiKey 认证支持** |
| Subtracks（com.subtracks v3.0.0） | Subsonic | `t/s` token | ✅ 全通 | 连接测试 OK、同步、封面、专辑详情、播放正常 |
| Jellyfin 官方（org.jellyfin.mobile） | Jellyfin | — | ❌ 客户端限制 | 客户端握手校验不满足（/System/Info/Public 四种伪装修复均无效），非网关 bug |
| Findroid（dev.jdtech.jellyfin） | Jellyfin | — | ❌ 客户端崩溃 | 启动即崩溃，客户端自身问题 |
| com.subsonic20.player | Subsonic | — | ❌ 客户端崩溃 | 缺 Google Play Services 动态模块，客户端自身问题 |

### 网关侧已修复的兼容性缺口

- **apiKey 参数认证**（v1.0.10）：Navidrome 风格客户端（StreamMusic/箭头音乐）以 `?apiKey=<subsonicToken>` 认证；网关新增校验 `apiKey == md5(stream_pass + "lnh-navidrome")`（即 subsonicToken）
- **Navidrome /rest 前缀路由**（v1.0.10）：客户端把 Base URL 配成带 `/rest` 后缀时，`/rest/auth/*`、`/rest/api/*`、`/rest/ping` 路由可正常命中
- **Subsonic 列表响应补 `x-total-count` 头**：音流客户端统计信息不再显示"未知"
