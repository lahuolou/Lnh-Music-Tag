# 四协议兼容矩阵

此表描述源码中存在的路由/适配能力，不等于已通过所有客户端的实机验收。状态含义：**实现子集** = 代码存在适配；**待验证** = 尚未在本次环境完成端到端测试；**不承诺完整兼容** = 需逐接口按官方协议补齐。

| 能力 | Subsonic / OpenSubsonic | Navidrome 风格 REST | Jellyfin | Emby |
|---|---|---|---|---|
| 登录/认证 | Subsonic `u/p` 或 `t/s` 认证路径 | `/auth/login`、`/auth/token`，Bearer 与兼容 token 参数 | MediaBrowser 风格认证头/登录路由子集 | Emby 风格认证头/登录路由子集 |
| 曲库列表/搜索 | 实现常用歌曲、专辑、艺术家及搜索接口子集 | `/api/song`、`album`、`artist`、`search`、`genres`、列表接口 | Items/Artists/Genres 等媒体查询子集 | Items/Artists/Genres 等媒体查询子集 |
| 封面/图片 | `getCoverArt` 等常见路径需逐接口验证 | `/api/coverart`、`coverArt`、实体封面路径 | 图片/封面代理子集 | 图片/封面代理子集 |
| 音频流与 Range | 流媒体端点需验证 HEAD、Range、416 和断点续播 | `/api/song/{id}/stream`、`/api/stream/{id}` | Stream 路由子集 | Stream 路由子集 |
| 播放列表 | 查询/变更覆盖度需逐接口确认 | `/api/playlist` 与 songs/tracks 子集 | 不保证完整播放列表 API | 不保证完整播放列表 API |
| 播放进度/会话 | 客户端上报接口需逐项验证 | nowplaying 等部分接口 | Sessions/PlaybackInfo 等需逐接口验证 | Sessions/PlaybackInfo 等需逐接口验证 |
| 歌词/其他扩展 | OpenSubsonic 扩展需逐项验证 | `/api/lyrics/{songId}` | 非标准能力，不承诺 | 非标准能力，不承诺 |

## 发布前必测用例

对每个协议单独运行，不允许以一个协议的结果推断另一个协议：

1. 正确/错误密码、缺少认证头、过期/错误 token。
2. 空曲库与空数组 JSON；分页负数、0、超总数、超大 limit。
3. 中文、特殊字符、缺失标签、无封面、无歌词。
4. 音频 GET、HEAD、`Range: bytes=0-1`、无效 Range、断点拖动、客户端中途断开。
5. 搜索、排序、分页边界和播放列表变更。
6. 播放进度、正在播放状态和错误响应 JSON 结构。
7. 基地址统一填根（后端自动路由协议）与旧后缀（`/rest`、`/jellyfin`、`/emby`）兼容、反向代理子路径、HTTP/HTTPS。

## 测试记录模板

每次记录：客户端名称/版本、协议模式、操作系统、服务端 commit、测试日期、通过/失败用例、脱敏日志、复现步骤。没有测试记录的项目标记为“未验证”，不标记为“兼容”。
