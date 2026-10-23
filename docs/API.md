# 后端 API 参考

本文档列出 Lnh-Music-Tag 的 HTTP API。分两部分：**浏览器管理 API**（会话认证 + CSRF）与**流媒体网关 API**（协议认证，供播放器调用）。

---

## 1. 认证与会话

### 1.1 登录

```
POST /api/login
Body: {"user":"admin","pass":"***"}
```

成功返回 `{"ok":true}` 并写入会话 Cookie。失败返回 401。登录成功后：

- 浏览器 API 凭 Cookie 会话访问；
- 所有非 GET 请求必须带请求头 `X-Requested-With: Lnh-Music-Tag`（CSRF 防护），否则拒绝；
- `POST /api/logout` 退出登录；`GET /api/me` 返回当前身份；`POST /api/change-password` 修改密码（至少 6 位）。

### 1.2 网关认证（播放器）

网关不校验浏览器会话，使用**流媒体密码**协议认证：

| 协议 | 认证方式 |
|---|---|
| Subsonic / OpenSubsonic | `u` + `p`（明文或十六进制 salt+token）、`t`+`s`（token）、`apiKey` 头（StreamMusic 兼容） |
| Navidrome 兼容 | `POST /auth/login` body `{"username","password"}` 或 Basic 认证 |
| Jellyfin / Emby | `X-Emby-Token` 头（流媒体密码作为令牌）、`api_key` 参数 |

> 用户名字段不校验（兼容音流/箭头音乐等客户端的任意用户名习惯），**流媒体密码必填**：未配置 `stream_pass` 时网关拒绝启用/请求。

---

## 2. 浏览器管理 API

统一响应格式：成功 `{"ok":true,...}`；失败 `{"ok":false,"error":"..."}`。

### 2.1 曲目与扫描

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/scan` | 扫描音乐目录（Body 含 `dir`）；进度由 `scan/status` 轮询 |
| GET | `/api/scan/status` | 扫描进度（`scanning`/`paused`、`current`、`total`） |
| POST | `/api/scan/pause` | 暂停/恢复扫描 |
| GET | `/api/tracks` | 曲目列表（支持 `q` 搜索、`filter` 筛选：乱码/繁体/无歌词/无海报/待识别、`sort` 排序） |
| GET | `/api/tracks/refresh` | 重新读取标签刷新内存 |
| GET | `/api/track/{id}` | 单曲详情 |
| POST | `/api/tracks/delete` | 删除曲目（先移入回收站） |
| GET | `/api/tracks/{id}/cover` | 封面二进制（`?w=` 指定宽度） |
| POST | `/api/tracks/{id}/cover` | 替换封面（multipart 文件字段 `file`） |
| GET | `/api/tracks/{id}/lrcfile` | 读取外挂 .lrc 文件内容 |
| POST | `/api/tracks/{id}/tags` | 批量写标签（Body 为字段键值） |
| POST | `/api/tracks/{id}/scrape` | 对单曲刮削（Body 含 `sources`） |
| GET | `/api/duplicates` | 重复组列表（`by`：hash/name/artist_title） |
| POST | `/api/duplicates/remove` | 移除重复曲目（保留组内最佳） |

### 2.2 刮削与任务

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/scrape/batch` | 批量刮削：`{"ids":[...],"sources":[...],"fields":{...}}` |
| POST | `/api/scrape/onekey` | 一键刮削流水线：`{"ids":[...],"sources":[...],"fields":{...}}`（含乱码修复/简繁/补艺术家） |
| GET | `/api/scrape/search` | 搜索候选：`?title=&artist=&album=&source=` |
| GET | `/api/lyrics` | 获取歌词：`?title=&artist=` |
| GET | `/api/scrape/sources` | 可用刮削源列表 |
| GET | `/api/scrape/jobs/{id}` | 批量任务进度（`running/done/cancelled/failed`、`done/current`、`results`） |
| GET | `/api/jobs` | 进行中任务列表（刷新后可恢复进度） |
| GET | `/api/jobs/{id}` | 通用任务进度（转换/识别/乱码/简繁） |
| POST | `/api/jobs/{id}/cancel` | 取消任务 |

### 2.3 工具操作

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/convert` | 批量转码：`{"ids":[...],"format":"mp3","bitrate":320,...}` |
| GET | `/api/ffmpeg/status` | FFmpeg 状态与版本（TTL 缓存） |
| POST | `/api/ffmpeg/install` | 在线安装 FFmpeg |
| POST | `/api/ffmpeg/uninstall` | 卸载 FFmpeg（移除 wrapper 配置） |
| POST | `/api/fix-encoding` | 乱码标签修复：`{"ids":[...]}` |
| POST | `/api/convert-script` | 简体↔繁体转换：`{"ids":[...],"mode":"t2s"|"s2t"}` |
| POST | `/api/rename-clean` | 后缀清理（`song.mp3.flac → song.flac`） |
| POST | `/api/identify` | 音频识别（AcoustID/指纹）：`{"ids":[...]}` |

### 2.4 插件

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/plugins` | 插件列表（含状态与配置） |
| POST | `/api/plugins` | 批量更新：`{"scrape.qq":{"enabled":true},"tool.ffmpeg":{"config":{"path":"..."}}}` |
| POST | `/api/plugins/import` | 导入第三方插件 ZIP（multipart 文件字段 `file`） |
| DELETE | `/api/plugins/{name}` | 删除第三方插件 |
| POST | `/api/plugins/{name}/run` | 运行插件（挂到后台任务） |
| GET | `/api/plugins/{name}/panel` | 插件配置面板 HTML |
| GET | `/api/plugins/{name}/panel/{path...}` | 面板静态资源 |
| POST | `/api/fs/scan-dir` | 面板通用目录扫描（只读列举） |

### 2.5 任务计划与监控

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/scheduler` | 当前配置：`{enabled,cron,mode,scan_first,watchEnabled,watchInterval,nextRun,lastRun,lastMsg}` |
| POST | `/api/scheduler` | 保存配置（`scheduler_enabled/cron/mode/scan_first` + `watcher_enabled/interval`） |
| POST | `/api/scheduler/run` | 立即运行一次任务计划（防重入） |

### 2.6 设置 / 日志 / 其他

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/settings` | 读取设置（账号/目录/选项/编辑字段） |
| POST | `/api/settings` | 保存设置 |
| GET | `/api/logs` | 操作日志分页（`?limit=&offset=`） |
| POST | `/api/logs/delete` | 删除日志（`{"id":n}` 或 `{"ids":[...]}`） |
| POST | `/api/logs/clear` | 清空日志 |
| GET | `/api/version` | 当前版本 |
| GET | `/api/version/check` | 检查 GitHub Release 更新（TTL 缓存） |
| GET | `/api/metrics` | 运行指标（内存等） |
| GET | `/api/trash` | 回收站列表 |
| POST | `/api/trash/restore` | 恢复：`{"items":[{"origPath":"...","trashPath":"..."}]}` |
| POST | `/api/trash/delete` | 永久删除：`{"items":[...]}` |
| POST | `/api/trash/clear` | 清空回收站 |

---

## 3. 流媒体网关 API

### 3.1 Subsonic / OpenSubsonic（`/rest` 前缀）

标准 Subsonic 端点（XML 响应，`u`/`p` 或 `t`+`s` 认证）：

| 端点 | 说明 |
|---|---|
| `/rest/ping` | 连通性检查 |
| `/rest/getMusicFolders` | 媒体库根目录 |
| `/rest/getIndexes` | 艺术家索引（A-Z） |
| `/rest/getArtists` | 艺术家列表 |
| `/rest/getArtist` | 艺术家详情 + 专辑 |
| `/rest/getAlbum` | 专辑详情 + 曲目 |
| `/rest/getAlbumList2` | 专辑列表（type 过滤） |
| `/rest/getSong` | 单曲 |
| `/rest/getSongs` | 曲目列表（byGenre/byYear） |
| `/rest/search2` / `/rest/search3` | 搜索 |
| `/rest/getPlaylists` / `/rest/getPlaylist` | 播放列表 |
| `/rest/star` / `/rest/unstar` | 收藏 |
| `/rest/getStarred` / `/rest/getStarred2` | 收藏列表 |
| `/rest/scrobble` | 播放统计上报 |
| `/rest/getNowPlaying` | 正在播放 |
| `/rest/getLyrics` | 歌词 |
| `/rest/getCoverArt` | 封面 |
| `/rest/stream` | 流媒体（支持 Range、转码回退） |

### 3.2 Navidrome 兼容（根路径 `/auth`、`/api` 与 `/rest/api`）

| 端点 | 说明 |
|---|---|
| `POST /auth/login` / `POST /auth/token` | 登录（body `username/password`），返回 `token/subsonic-token` |
| `GET /ping` | 连通性 |
| `/api/song`、`/api/song/{id}` | 曲目列表/详情（`_start/_end/order` 分页，`x-total-count` + `Content-Range`） |
| `/api/song/{id}/stream` | 流媒体 |
| `/api/album`、`/api/album/{id}`、`/api/album/{id}/songs` | 专辑 |
| `/api/artist`、`/api/artist/{id}`、`/api/artist/{id}/albums` | 艺术家 |
| `/api/playlist`、`/api/playlist/{id}/songs` | 播放列表 |
| `/api/search`、`/api/search/songs` | 搜索 |
| `/api/starred`、`/api/starred/{id}` | 收藏 |
| `/api/genres`、`/api/albumlist/{type}`、`/api/songlist/{type}` | 类型列表 |
| `/api/library/scanstatus`、`/api/library/statistics`、`/api/statistics` | 库统计 |
| `/api/nowplaying`、`/api/lyrics/{songId}` | 正在播放/歌词 |
| `/api/stream/{id}`、`/api/coverart/{id}` | 流/封面 |

> 以上同时注册 `/rest/api/*` 前缀，兼容把 Base URL 带 `/rest` 的客户端（音流/Musiver 等）。

### 3.3 Jellyfin / Emby（`/jellyfin`、`/emby` 前缀 + 根路径 System/Users/Items…）

| 端点 | 说明 |
|---|---|
| `GET /System/Info/Public` | 服务器信息 |
| `POST /Users/AuthenticateByName` | 登录（body `Username/Password`），返回 `AccessToken` |
| `GET /Users`、`GET /Users/{id}` | 用户 |
| `GET /Items` | 媒体库浏览（`ParentId`、`searchTerm`、`SortBy`、`Limit/StartIndex` 分页） |
| `GET /Items/{id}` | 条目详情 |
| `GET /Artists`、`GET /Albums`、`GET /Playlists`、`GET /Genres` | 各类型列表 |
| `GET /Audio/{id}/stream` | 音频流（`static` 直传 / 转码） |
| `GET /Items/{id}/Images/Primary` | 封面 |
| `GET /Sessions` | 会话 |
| `POST /Sessions/Playing` | 播放状态上报 |

---

## 4. 分页与错误

- **Navidrome 风格分页**：`_start`/`_end`（或 `offset`/`limit`）参数；响应头 `x-total-count` 与 `Content-Range: items start-end/total`；
- **负数/非法分页参数**：解析层归零（`parseNonNegParam`），`sliceRange` 二次兜底（`total<=0` 早退、end 上界钳制），不会产生负切片索引；
- **空列表**：返回合法分页头（`items */0`）与 `[]`（非 `null`）；
- **Jellyfin/Emby 分页**：`StartIndex`/`Limit`，响应含 `TotalRecordCount`；
- **错误**：管理 API 统一 `{"ok":false,"error":"..."}`；Subsonic 返回 `<error code="..." message="..."/>`；Jellyfin 返回对应 HTTP 状态 + JSON 错误。

---

## 5. 认证与安全备注

- 管理 API 会话过期需重新登录；CSRF 头缺失的非 GET 请求直接拒绝；
- 网关流媒体密码保存在配置库（`config/lnh.db` 插件配置 `stream_pass`），环境变量 `LNH_STREAM_PASS` 仅作首次启动种子；
- 所有密码/Key 不写入日志。
