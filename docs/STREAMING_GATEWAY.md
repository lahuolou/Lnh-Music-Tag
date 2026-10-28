# 流媒体网关接入指南

Lnh-Music-Tag 内置**音乐统一网关**：一个网关对外提供 **Subsonic / OpenSubsonic、Jellyfin、Emby、Navidrome** 四种协议，音流、箭头音乐、Jellyfin 官方 App 等数十款播放器可直接连接播放。

---

## 1. 快速启用

1. 进入 **设置 → 插件 → 音乐统一网关**；
2. **必须配置流媒体密码**（`stream_pass`）——未配置时网关无法启用，防止未授权访问；
3. 保存并开启网关；
4. 在播放器里按协议添加服务器（地址见下），账号填管理员用户名（任意用户名均可），密码填流媒体密码。

> 兼容旧部署：未配置 `stream_pass` 时，可读取环境变量 `LNH_STREAM_PASS` 作为兜底。

---

## 2. 认证策略（重要）

网关认证**只校验流媒体密码，不校验用户名**。这是有意的兼容行为：

- 音流、箭头音乐等客户端连接时会提交**任意用户名**（有的写死 `admin`，有的填手机号/邮箱），校验用户名会导致这些客户端登录失败；
- 因此网关对用户名字段**宽容处理**，只要密码正确即可访问；
- **安全边界**：流媒体密码是网关的唯一凭证，请使用足够强度的密码，不要与后台登录密码共用。

管理后台登录（浏览器）与网关认证是两套独立体系：后台用「管理员账号 + 密码」（数据库校验），网关用「任意用户名 + 流媒体密码」。

---

## 3. 协议接入地址

**统一填根地址即可，后端按请求自动选择协议**：`http://IP:10248`

| 协议 | Base URL | 认证方式 |
|---|---|---|
| Subsonic / OpenSubsonic | `http://IP:10248`（自动路由；`/rest` 前缀兼容保留） | `u` + `p`（明文或 salt+token）、`t`+`s`、`apiKey` 头 |
| Navidrome 兼容 | `http://IP:10248`（`/auth/login`、`/api/*`） | body `username/password` 登录换 token |
| Jellyfin | `http://IP:10248`（`/jellyfin` 前缀兼容保留） | `X-Emby-Token` 头 / `api_key` |
| Emby | `http://IP:10248`（`/emby` 前缀兼容保留） | `X-Emby-Token` 头 / `api_key` |

**协议自动路由规则（按请求特征，无需手动区分后缀）**：

| 协议 | 识别方式 |
|---|---|
| Subsonic / OpenSubsonic | 请求路径以 `.view` 结尾（如 `/ping.view`、`/getIndexes.view`）→ 自动接入 Subsonic 网关；`/rest/*` 前缀仍兼容 |
| Navidrome 兼容 | `/auth/login`、`/auth/token`、`/ping`、`/api/*` → Navidrome 兼容层；`/rest/api/*` 前缀仍兼容 |
| Jellyfin / Emby | `/System/`、`/Items`、`/Users`、`/Artists` 等 Jellyfin/Emby 端点 → 网关自动适配；`/jellyfin`、`/emby` 前缀仍兼容 |

> 结论：客户端**统一填 `http://IP:10248`** 即可，不需要在地址里区分 `/rest`、`/jellyfin`、`/emby` 等后缀——后端自动选择协议；带后缀的旧地址也全部兼容。

---

## 4. 客户端兼容性实测记录

| 客户端 | 协议 | 状态 | 备注 |
|---|---|---|---|
| 音流（SoundFlow） | Subsonic / Navidrome | ✅ 实测通过 | 列表/搜索/播放/统计正常；曾出现统计「未知」——已通过补 `x-total-count` 响应头修复 |
| 箭头音乐（Archer） | Subsonic | ✅ 实测通过 | 登录/列表/播放正常；**已知限制**：iOS CarPlay 下封面不显示（客户端取封面地址方式与网关返回不一致，待客户端侧观察） |
| Jellyfin 官方 App | Jellyfin | ✅ 实测通过 | 登录/浏览/播放正常 |
| Findroid | Jellyfin | ✅ 实测通过 | 登录/浏览/播放正常 |
| StreamMusic | Subsonic（apiKey） | ✅ 实测通过 | `apiKey` 头认证正常 |
| Subtracks | Subsonic（token） | ✅ 实测通过 | salt+token 认证正常 |
| Ultrasonic | Subsonic | ✅ 实测通过 | 登录/浏览/播放正常 |
| DSub | Subsonic | ✅ 实测通过 | 登录/浏览/播放正常 |
| Musly | Subsonic | ⏳ 待实测 | 需真实客户端环境验证 |
| Symfonium | Subsonic | ⏳ 待实测 | 需真实客户端环境验证 |
| Feishin | Subsonic | ⏳ 待实测 | 需真实客户端环境验证 |
| Supersonic | Subsonic | ⏳ 待实测 | 需真实客户端环境验证 |

---

## 5. 常见问题排障

### 5.1 登录失败 / 401

- 检查**流媒体密码**是否已配置且与客户端填写一致（网关唯一凭证）；
- 检查协议选择：Subsonic 系客户端（音流/箭头音乐/Ultrasonic/DSub/Subtracks/StreamMusic/Symfonium/Feishin/Supersonic）选 Subsonic；Jellyfin/Emby 官方选对应协议；Navidrome 风格客户端选 Navidrome；
- 检查 Base URL：**统一填 `http://IP:10248` 即可**（后端自动选择协议）；旧地址 `.../rest`、`.../jellyfin`、`.../emby` 仍兼容；
- 用户名**任意**——填什么都能过，重点是密码正确。

### 5.2 列表正常但播放失败

- 确认 FFmpeg 可用（设置 → 插件 → FFmpeg「已就绪」）；部分格式直传失败时会回退转码，FFmpeg 缺失则无法回退；
- 检查文件路径：网关按扫描时的路径访问音频文件，容器部署时确保挂载路径一致。

### 5.3 统计/计数显示「未知」

- 网关已为 Subsonic 列表响应补齐 `x-total-count` 头，请确认实例为 v1.0.11+。

### 5.4 分页异常

- Navidrome 风格端点支持 `_start/_end`（或 `offset/limit`），负数/越界参数已做兜底；超大页会按上限钳制。

---

## 6. 端点清单

- Subsonic / OpenSubsonic：`/rest/ping`、`/rest/getMusicFolders`、`/rest/getIndexes`、`/rest/getArtists`、`/rest/getArtist`、`/rest/getAlbum`、`/rest/getAlbumList2`、`/rest/getSong`、`/rest/getSongs`、`/rest/search2`、`/rest/search3`、`/rest/getPlaylists`、`/rest/getPlaylist`、`/rest/star`、`/rest/unstar`、`/rest/getStarred`、`/rest/getStarred2`、`/rest/scrobble`、`/rest/getNowPlaying`、`/rest/getLyrics`、`/rest/getCoverArt`、`/rest/stream`
- Navidrome 兼容：`/auth/login`、`/auth/token`、`/ping`、`/api/song`、`/api/album`、`/api/artist`、`/api/playlist`、`/api/search`、`/api/starred`、`/api/genres`、`/api/albumlist/{type}`、`/api/songlist/{type}`、`/api/library/*`、`/api/statistics`、`/api/nowplaying`、`/api/lyrics/{songId}`、`/api/stream/{id}`、`/api/coverart/{id}`（并注册 `/rest/api/*` 前缀）
- Jellyfin / Emby：`/System/Info/Public`、`/Users/AuthenticateByName`、`/Users`、`/Items`、`/Items/{id}`、`/Artists`、`/Albums`、`/Playlists`、`/Genres`、`/Audio/{id}/stream`、`/Items/{id}/Images/Primary`、`/Sessions`、`/Sessions/Playing`（Jellyfin 同时在根路径注册 `/System/`、`/Items` 等）

### 6.1 歌单（.m3u/.m3u8 自动导入）

- 扫描音乐库时自动发现 `.m3u` / `.m3u8` 歌单文件并导入为网关歌单（`streaming_playlists` 配置），Subsonic `getPlaylists` / Navidrome `/api/playlist` / Jellyfin `/Playlists` 三协议共享；
- 歌单名取文件主名（如 `最爱.m3u` → 歌单「最爱」）；条目支持相对路径（相对歌单文件所在目录、相对音乐库根）与绝对路径；`http(s)://` / `mms://` URL 条目与无法匹配的缺失文件自动跳过；Windows 路径大小写不敏感匹配；
- 每次扫描**幂等重建**：歌单文件删除或改名后，网关中对应歌单自动消失；手工通过网关创建的歌单（ID 非 `m3u-` 前缀）不受影响。

完整字段级说明见 [API.md](./API.md) 第 3 节。
