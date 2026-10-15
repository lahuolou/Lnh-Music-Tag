# LNH-MusicTag v1.6.0 发布说明

**新增内置 Subsonic API 协议输出插件——音流 / Symfonium / Ultrasonic / DSub 等空壳播放器可直连本音乐库**

## 新增与改进

1. **Subsonic API 协议输出（内置插件 `protocol.subsonic`，用预留的插件协议接口）**
   - 在现有 Web 服务上挂载 `GET /rest/*`，对外提供标准 Subsonic 服务端 API（XML 缺省 + `format=json`），播放器直接添加服务器即可浏览艺术家/专辑/曲目、搜索、在线播放、下载、封面与歌词；
   - 已实现端点：ping / getLicense / getMusicFolders / getIndexes / getArtists / getArtist / getAlbum / getSong / getAlbumList(2) / getMusicDirectory / getGenres / getSongsByGenre / search / search2 / search3 / stream（支持 Range 断点续传）/ download / getCoverArt / getLyrics / getUser / getPlaylists / getPlaylist / getStarred(2) / getNowPlaying / scrobble / star / unstar / setRating / getTopSongs / getSimilarSongs(2) / getArtistInfo(2) / getAvatar 等，未知方法返回 status=ok；
   - **三种认证**：token（`md5(pass+salt)`）/ 明文 `p=`（含 `enc:` 前缀）/ HTTP Basic；可在「设置 → 插件 → 协议插件」配置专用密码 `subsonic_pass`，留空回退后台账号密码（仅明文/Basic）；
   - 连接地址：`http://<主机IP>:10248/rest`，用户名用后台账号；认证失败返回错误码 40，未找到返回 70；
   - 插件页新增「📡 协议插件」分组（默认展开），卡片展示连接地址与用户名提示。

2. **插件安全加固（影响所有既有插件）**
   - 插件 API Key 类字段（Subsonic 密码 / AcoustID Key / Spotify 等）快照时统一脱敏为 `********`，不再明文回显前端；保存时跳过占位符防误写回。

## 验证

- Subsonic 全量协议测试（3 首带标签 FLAC 样例库）**33 项断言全部通过**：三种认证（含错误密码 → 40）、XML/JSON 双格式、getIndexes 艺术家分组、艺术家/专辑/曲目/搜索（中文 query）/流派、stream Range（206 + `bytes 0-1023/N`）、download、getCoverArt 无封面错误码 70、getLyrics 空歌词、未知方法、收藏/评分/scrobble 空成功、Basic 认证等；
- `go vet` / `go build` / 前端 `npm run build` 全绿；本机两个实例（主服务 10248 + 测试实例 10249）`/rest/ping.view` 均返回 200。

## 升级方式

1Panel / Docker 拉取新镜像并重建容器：

```bash
docker pull ghcr.io/lahuolou/lnh-musictag:latest
docker compose up -d
```

更新后到「设置 → 插件 → 协议插件」查看 Subsonic 连接地址，在播放器（音流/Symfonium/Ultrasonic/DSub…）中添加服务器即可。
