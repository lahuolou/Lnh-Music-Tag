# Lnh-Music-Tag · 音乐标签管理

> 自托管 · 开源 · 全功能音乐库标签管理工具
>
> 版本：1.0.7 ｜ 语言：中文 / [English](./README_EN.md)

Lnh-Music-Tag 是一款面向个人音乐库的**自托管标签管理与流媒体网关**工具。它帮助你批量整理本地音乐文件的元数据（标题、艺术家、专辑、封面、歌词等），并通过内置的「音乐统一网关」把整理好的曲库以 **Subsonic / Jellyfin / Emby / Navidrome** 四种协议对外提供播放服务，兼容音流、Archer、Jellyfin、Emby、Navidrome 等数十款主流播放器。

---

## 免责声明（必读）

> **本软件只是一个音乐库管理工具，本身不包含、不捆绑、不提供任何音源内容。**
>
> - 软件只读写你**自己提供**的本地音频文件（标签、封面、歌词、文件名等元数据）；
> - 软件内置的「刮削」功能仅从公开站点抓取**标签/封面/歌词等元数据**，不下载、不存储、不传播任何音频内容；
> - 请确保你拥有所管理音频文件的合法使用权与分享权；
> - 本软件**仅供学习参考使用**，作者不对使用本软件产生的任何后果（包括但不限于数据丢失、文件损坏、内容合规问题、第三方服务条款违约）承担任何责任；
> - 使用即表示你已阅读并同意以上条款。

---

## 功能特性

### 曲库管理
- **目录扫描**：扫描本地音乐目录并读取音频标签，重启不丢列表（SQLite 持久化）
- **标签读写**：网页端编辑标题、艺术家、专辑、专辑艺术家、风格、年代、曲目号、歌词等 30+ 标签字段
- **封面管理**：查看、替换、删除内嵌封面；无海报筛选一键补全
- **歌词管理**：内嵌歌词与同目录 `.lrc` 文件双支持，一键在线获取歌词
- **智能筛选**：乱码 / 繁体 / 无歌词 / 无海报 / 待识别，快速定位需要整理的曲目

### 智能整理
- **多源刮削**：内置 17 个元数据源（网易云音乐、QQ 音乐、酷狗、酷我、咪咕、B 站、汽水、波点、5sing、MusicBrainz、iTunes、ListenBrainz、Deezer、Last.fm、Spotify、Discogs、Jamendo），自动降级与去重
- **一键刮削流水线**：乱码修复 → 繁体转简体 → 文件名补艺术家 → 刮削全部元素，一键全库整理
- **批量补全**：按需勾选字段批量补全，已有标签智能跳过，节省硬件开销
- **智能去重**：按音频内容（SHA-256 哈希）识别重复文件，每组保留音质最好的一首
- **音频内容识别**：无标签、无文件名信息的曲目（如 `track01.mp3`），按音频指纹与库内已标注曲目匹配并补全
- **乱码修复**：自动识别并还原 GBK/UTF-8 错读的中文标签
- **简繁转换**：简体 ↔ 繁体批量转换，适合整理港台唱片
- **修正文件后缀**：`发如雪.mp3.flac → 发如雪.flac`，探测真实编码后重命名

### 格式转换
- 基于 FFmpeg 的批量转码：目标格式（MP3/FLAC/AAC/OGG/WAV…）、比特率、采样率均可配置
- 转码结果保存在同目录，**不删除源文件**
- 未安装 FFmpeg 时可一键在线安装，或填写本机可执行文件路径

### 音乐统一网关（流媒体服务）
- 一个网关，四种协议：**Subsonic / OpenSubsonic、Jellyfin、Emby、Navidrome**
- 兼容音流、Archer、Jellyfin、Emby、Navidrome 官方客户端及第三方播放器
- 支持标准协议端点：索引、艺术家/专辑/曲目浏览、搜索、歌词、播放列表、收藏、Now Playing、播放统计、直传/转码流媒体
- **强制设置密码**：开启网关前必须配置流媒体密码，未配置时无法开启，防止未授权访问

### 工程与安全
- **插件系统**：刮削源、FFmpeg、AcoustID、网关均为可启停、可配置的插件；支持第三方插件 ZIP 导入与独立配置面板
- **回收站**：删除/去重先移入回收站，可恢复或永久删除，误删可找回
- **操作日志**：扫描、刮削、转换等操作全程留痕，可追溯可清理
- **更新检查**：内置 GitHub Release 检查，新版本一键提示
- **健壮性**：后台任务并发有界（信号量）、任务异常自动兜底（panic 恢复为 failed 终态）、外部进程调用全部超时保护、数据库批量事务、启动单实例锁

---

## 快速开始

### 方式一：Docker（推荐）

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /你的/音乐/目录:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=你的密码 \
  ghcr.io/lahuolou/lnh-music-tag:latest
```

或使用仓库自带的 `docker-compose.yml`：

```bash
docker compose up -d
```

启动后浏览器访问 `http://服务器IP:10248`。

### 方式二：本地构建运行

要求：Go 1.27+、Node.js 18+（构建前端）

```bash
# 1. 构建前端
cd web
npm install
npm run build
cd ..

# 2. 构建后端（前端产物已嵌入二进制）
go build -o lnh-music-tag .

# 3. 运行
LNH_CONFIG_DIR=./config LNH_PORT=:10248 ./lnh-music-tag
```

> 首次启动若无 `LNH_ADMIN_PASS`，会在日志中打印随机初始密码，登录后请在「设置」页修改。

---

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP 监听地址 |
| `LNH_CONFIG_DIR` | `/config` | 配置目录（数据库、回收站、插件数据持久化于此） |
| `LNH_ADMIN_USER` | `admin` | 首次启动的管理员账号 |
| `LNH_ADMIN_PASS` | 随机生成 | 首次启动的管理员密码（留空则打印到日志） |
| `LNH_STREAM_PASS` | 空 | 流媒体密码（兼容旧配置；优先使用插件页配置的 `stream_pass`） |
| `LNH_INSTALL_FFMPEG` | `false` | `true/1` 时容器启动自动安装 FFmpeg（需 root） |

> 账号 / 密码 / API Key 均保存在配置数据库（`config/lnh.db`），环境变量仅在**首次启动**时作为初始化种子。

---

## 音乐统一网关接入

开启网关前**必须先设置流媒体密码**（设置 → 插件 → 音乐统一网关）。

| 协议 | 接入地址（示例） | 账号/密码 |
|---|---|---|
| Subsonic / OpenSubsonic | `http://IP:10248/rest` | 管理员账号 + 流媒体密码 |
| Navidrome 兼容 | `http://IP:10248`（`/auth/login`、`/api/*`） | 同上 |
| Jellyfin | `http://IP:10248/jellyfin`（或根路径） | 同上 |
| Emby | `http://IP:10248/emby` | 同上 |

常见客户端（音流、Archer、Jellyfin 官方 App、Emby 官方 App、Navidrome 官方 App 等）添加服务器时选择对应协议即可。详细端点清单见 [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md)。

---

## 项目文档

| 文档 | 说明 |
|---|---|
| [README_EN.md](./README_EN.md) | English introduction |
| [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md) | 开发文档：架构、模块、构建、测试、代码规范 |
| [docs/API.md](./docs/API.md) | 后端 API 参考 |
| [docs/PLUGINS.md](./docs/PLUGINS.md) | 插件系统与第三方插件开发 |
| [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md) | 流媒体网关协议与接入 |
| [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) | 部署与运维（Docker / 1Panel / 本地） |
| [CHANGELOG.md](./CHANGELOG.md) | 版本历史 |

---

## 遇到问题？

- 先阅读 [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) 与 [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md) 排查常见问题
- 在 [GitHub Issues](https://github.com/lahuolou/Lnh-Music-Tag/issues) 提交问题，请附带：
  - 版本号（`设置` 页可见，或 `cat VERSION`）
  - 部署方式（Docker / 本地）、系统与架构
  - 复现步骤、相关日志、报错截图

---

## 开源许可

本项目基于 **GNU General Public License v3.0（GPL-3.0）** 开源，详见 [LICENSE](./LICENSE)。

> 再次提醒：本项目为学习参考用途的管理工具，不含任何音源内容，使用风险自负。
