# Lnh-Music-Tag

> 自托管 · 开源 · 以「刮削」为核心的音乐库智能整理 + 流媒体网关

Lnh-Music-Tag 是一款自托管音乐库工具，**核心亮点是刮削与自动刮削**：

- **多源自动刮削**：内置 17 个元数据源（网易云、QQ 音乐、酷狗、酷我、咪咕、B 站、汽水、波点、5sing、MusicBrainz、iTunes、ListenBrainz、Deezer、Last.fm、Spotify、Discogs、Jamendo），自动降级、自动去重，一键为全库补全标题、艺术家、专辑、封面、歌词；
- **任务计划自动刮削**：设置频率与时间，后台定时「先扫描 + 刮削」，全程无人值守，防重入、留日志；
- **后台监控静默刮削**：新增歌曲自动入库并静默刮削（封面/歌词/全部元数据），无需手动点击；
- **曲库管理与播放**：网页批量编辑 30+ 标签字段、封面歌词管理、乱码修复、简繁转换、内容识别去重；整理好的曲库还内置**音乐统一网关**，以 **Subsonic / Jellyfin / Emby / Navidrome** 四种协议对外提供播放服务，兼容音流、Archer、Jellyfin 等数十款播放器；
- **本地优先**：数据存在你自己的 SQLite 里，标签写回文件，不依赖任何云服务，支持 Docker 一键部署。

**版本**：1.0.14 ｜ 语言：中文 / [English](./README_EN.md)

---

## 安全边界（部署前必读）

> 默认配置面向**可信内网**使用（同一局域网 / NAS / 个人服务器）。若要将服务暴露到**公网**，必须通过反向代理启用 HTTPS：
>
> - 推荐 Caddy（自动证书）、Nginx 或 Traefik，将 `10248` 端口代理到 443，并传递真实客户端 IP 与 `X-Forwarded-Proto`（示例见 [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md#6-安全边界与公网部署必读)）；
> - 应用自身不终止 TLS；会话 Cookie 在代理 HTTPS 下才能可靠判定 `Secure`；
> - 登录与流媒体网关认证均有失败限速（同一 IP 连续 5 次失败锁定 5 分钟），但请勿依赖它替代强密码；
> - 建议：仅内网访问 + 强随机密码 + 定期备份 `/config` 与音乐库。

---

## 免责声明（必读）

> **本项目只是一个音乐库管理工具，本身不包含、不捆绑、不提供任何音源内容。**
>
> - 只读写你**自己拥有合法权利**的本地音频文件（标签、封面、歌词、文件名等元数据）；
> - 内置「刮削」功能仅从公开站点抓取**元数据**（标签/封面/歌词），不下载、不存储、不传播任何音频内容；
> - 请确保你管理的音频文件拥有合法使用权与分享权；
> - 本项目**仅供学习参考使用**，作者不对使用本项目产生的任何后果承担责任（包括但不限于数据丢失、文件损坏、内容合规问题、第三方服务条款违约）；
> - 使用即表示你已阅读并同意以上条款。

---

## 核心能力

### 智能刮削（核心亮点）

- **多源自动刮削**：内置 17 个元数据源（网易云、QQ 音乐、酷狗、酷我、咪咕、B 站、汽水、波点、5sing、MusicBrainz、iTunes、ListenBrainz、Deezer、Last.fm、Spotify、Discogs、Jamendo），自动降级与去重，智能合并候选；
- **一键刮削流水线**：乱码修复 → 繁体转简体 → 文件名补艺术家 → 刮削全部元素，一次点击整理全库；
- **任务计划自动刮削**：设置页下拉选择「每天/每周/自定义 + 时间」自动生成 cron，后台定时执行「先扫描 + 刮削」；模式可选「一键刮削」或「先去重再一键刮削」，防重入、留日志、实时显示下次运行时间；
- **后台监控静默刮削**：定期检查音乐目录，新增音频自动入库并静默刮削（封面/歌词/全部元数据），无需手动点击；
- **按专辑/歌手名存放（可配置）**：设置页「整理方式」可选不整理（默认）/ 按专辑存放 / 按歌手名存放，扫描或刮削完成后自动把曲目整理到库根下的对应目录（文件名不变，外挂 .lrc 歌词跟随移动，幂等且不覆盖同名文件）；
- **来源熔断与自适应退避**：每个刮削源独立健康统计，连续失败自动熔断（跳过该源），冷却指数退避、成功即恢复，限流/故障源不再拖慢批量流水线；
- **匹配置信度 + 字段级融合**：候选按标题/艺人/时长加权评分（0-100），每个 ID3 字段独立从多源候选按置信度择优合并，同名异艺人候选被艺人门控拦截；
- **低置信度「待确认」队列**：置信度偏低的结果自动入队，曲库「待确认」标签一屏核对（原标签 vs 融合候选 + 置信度），确认/忽略一键处理；
- **批量补全**：按需勾选字段，已有标签智能跳过；
- **音频识别**：`track01.mp3` 这类无标签文件，按音频指纹匹配库内已知曲目补全。

### 曲库管理

- **目录扫描**：扫描本地音乐目录并读取标签，曲目列表持久化（SQLite），重启不丢；
- **标签读写**：网页端编辑标题、艺术家、专辑、专辑艺术家、风格、年代、曲目号、歌词等 30+ 字段；
- **封面管理**：查看/替换/删除内嵌封面，一键为缺封面的曲目补全；
- **歌词管理**：内嵌歌词与同目录 `.lrc` 歌词双支持，可一键在线获取；
- **智能筛选**：按「乱码 / 繁体 / 无歌词 / 无海报 / 待识别」快速定位需要整理的曲目。

### 辅助整理

- **智能去重**：按音频内容（SHA-256）识别重复文件，每组保留音质最好的一首；
- **乱码修复 / 简繁转换 / 后缀修正**：GBK/UTF-8 错读还原、简体↔繁体批量转换、`song.mp3.flac → song.flac`。

### 格式转换

- FFmpeg 批量转码：目标格式（MP3/FLAC/AAC/OGG/WAV…）、比特率、采样率可配；
- 转码结果与源文件同目录保存，**不删除源文件**；
- 容器启动**默认自动安装 FFmpeg**，也支持插件页一键安装或填写本机路径。

### 流媒体网关

- 一个网关四协议：**Subsonic / OpenSubsonic、Jellyfin、Emby、Navidrome**；
- 支持索引、艺术家/专辑/曲目浏览、搜索、歌词、播放列表、收藏、Now Playing、播放统计、直传/转码播放；
- **强制密码**：必须配置流媒体密码才能启用网关，防止未授权访问；
- 兼容音流、Archer、Jellyfin、Emby、Navidrome 官方 App 及第三方播放器。

### 工程与安全

- **插件系统**：刮削源、FFmpeg、AcoustID、网关均为可启停可配置插件；第三方插件 ZIP 导入，导入前校验路径（防穿越、zip-slip、文件数与大小上限）；官方示例插件：[unlock-plugin](https://github.com/lahuolou/unlock-plugin)（加密音乐解锁）、[musicdl-plugin](https://github.com/lahuolou/musicdl-plugin)（多平台音乐下载）；
- **回收站**：删除/去重先入回收站，可恢复或彻底删除；
- **操作日志**：扫描/刮削/转换/任务计划全程留痕，可追溯可清理；
- **健壮性**：并发有界（信号量）、任务 panic 自动兜底、外部进程调用全部超时保护、数据库批量事务、单实例启动锁、优雅停机。

---

## 快速上手

### 方式一：Docker Compose（推荐）

按需修改下面这份 `docker-compose.yml` 后一键启动：

```yaml
services:
  lnh-music-tag:
    # 服务器/1Panel 部署直接用 GHCR 公开镜像（无需本地构建）
    image: ghcr.io/lahuolou/lnh-music-tag:latest
    container_name: lnh-music-tag
    ports:
      - "10248:10248"        # 宿主机 10248 -> 容器 10248（改用非常用端口）
    volumes:
      # 默认音乐目录（NAS 路径）。应用会把标签/封面写回文件，所以必须可读写，别改成 :ro
      - /mnt/sata3-1/mp3:/music
      # 配置卷：持久化后台初始/修改后的密码（admin.json）、数据库配置、第三方插件包
      # （插件 ZIP 导入后解压到 /config/plugins/，重启不丢失）
      - lnh_config:/config
    environment:
      # 账号/密码/Key 默认存数据库（/config/lnh.db），以下环境变量仅作【首次启动】的一次性初始化种子
      - LNH_ADMIN_USER=admin
      - LNH_ADMIN_PASS=          # 留空则首次生成随机初始密码打印到日志，登录后在“设置”页修改（存数据库）
      # 可选：AcoustID 录音识别 Key（在「插件 → AcoustID」页填写，存数据库）
      # - ACOUSTID_API_KEY=        # 已弃用：改为插件页配置，环境变量不再读取
      # FFmpeg（格式转换）安装开关：默认开启，容器启动时自动 apk 安装 ffmpeg（需 root 运行）；
      # 设 false/留空 则不自动装，可改用：插件页“一键安装”，或挂载宿主机 /usr/bin 并在插件页填路径。
      - LNH_INSTALL_FFMPEG=true
    restart: unless-stopped
    # 健康检查：/health 存活探针（容器内 wget 由 busybox 提供）。
    # 安全边界：默认配置仅适合可信内网；公网暴露务必通过 Caddy/Nginx/Traefik
    # 反向代理启用 HTTPS，并配置可信代理头（详见 docs/DEPLOYMENT.md）。
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:10248/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s

volumes:
  lnh_config:
```

然后按步骤修改并启动：

1. **改音乐目录**：`volumes` 里把宿主机路径换成你的音乐目录。应用会把标签/封面**写回文件**，挂载必须可读写（不要用 `:ro`）；
2. **设置初始密码**：`LNH_ADMIN_PASS` 填首次登录的管理员密码（留空则随机生成并打印到日志）；
3. 启动：

```bash
docker compose up -d
```

浏览器访问 `http://服务器IP:10248`。配置、数据库、回收站、插件数据持久化在命名卷 `lnh_config`。升级：

```bash
docker compose pull && docker compose up -d
```

### 方式二：Docker run（等效，备选）

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /你的/音乐/目录:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=你的密码 \
  -e LNH_INSTALL_FFMPEG=true \
  ghcr.io/lahuolou/lnh-music-tag:latest
```

### 方式三：本地构建运行

要求：Go 1.27+，Node.js 18+（构建前端）。

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

> 首次启动未设置 `LNH_ADMIN_PASS` 时，随机初始密码会打印在日志中，登录后请在「设置」页修改。

---

## 运行配置

| 环境变量                 | 默认值       | 说明                                                  |
| -------------------- | --------- | --------------------------------------------------- |
| `LNH_PORT`           | `:10248`  | HTTP 监听地址                                           |
| `LNH_CONFIG_DIR`     | `/config` | 配置目录（数据库、回收站、插件数据）                                  |
| `LNH_ADMIN_USER`     | `admin`   | 首次启动的管理员账号                                          |
| `LNH_ADMIN_PASS`     | 随机生成      | 首次启动的管理员密码（留空则打印到日志）                                |
| `LNH_STREAM_PASS`    | 空         | 流媒体密码（兼容旧配置；优先使用插件页配置的 `stream_pass`）               |
| `LNH_INSTALL_FFMPEG` | `true`    | 容器启动时自动安装 FFmpeg；设为 `false/0` 关闭，可改用插件页一键安装或挂载宿主机路径 |

> 账号、密码、API Key 均保存在配置数据库（`config/lnh.db`），环境变量仅在**首次启动**时作为初始化种子。

---

## 接入音乐播放器

开启网关前**必须先设置流媒体密码**（设置 → 插件 → 音乐统一网关）。

| 协议                      | 接入地址（示例）                                  | 账号/密码         |
| ----------------------- | ----------------------------------------- | ------------- |
| Subsonic / OpenSubsonic | `http://IP:10248/rest`                    | 管理员账号 + 流媒体密码 |
| Navidrome 兼容            | `http://IP:10248`（`/auth/login`、`/api/*`） | 同上            |
| Jellyfin                | `http://IP:10248/jellyfin`                | 同上            |
| Emby                    | `http://IP:10248/emby`                    | 同上            |

音流、Archer、Jellyfin 官方 App、Emby 官方 App、Navidrome 官方 App 等客户端添加服务器时选择对应协议即可。详细端点清单见 [docs/STREAMING\_GATEWAY.md](./docs/STREAMING_GATEWAY.md)。

---

## 文档索引

| 文档                                                                                 | 说明                           |
| ---------------------------------------------------------------------------------- | ---------------------------- |
| [README\_EN.md](./README_EN.md)                                                    | English introduction         |
| [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md)                                       | 开发文档：架构、模块、构建、测试、代码规范        |
| [docs/API.md](./docs/API.md)                                                       | 后端 API 参考                    |
| [docs/PLUGINS.md](./docs/PLUGINS.md)                                               | 插件系统与第三方插件开发                 |
| [docs/PLUGIN\_API.md](./docs/PLUGIN_API.md)                                        | 插件清单与宿主 API 版本协商（apiVersion） |
| [docs/PROTOCOL\_COMPATIBILITY\_MATRIX.md](./docs/PROTOCOL_COMPATIBILITY_MATRIX.md) | 四协议兼容验收矩阵                    |
| [docs/STREAMING\_GATEWAY.md](./docs/STREAMING_GATEWAY.md)                          | 流媒体网关协议与接入                   |
| [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md)                                         | 部署与运维                        |
| [docs/ROADMAP.md](./docs/ROADMAP.md)                                               | 后续发展路线                       |
| [CHANGELOG.md](./CHANGELOG.md)                                                     | 版本历史                         |

---

## 问题反馈

- 先查阅 [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) 与 [docs/STREAMING\_GATEWAY.md](./docs/STREAMING_GATEWAY.md) 的常见问题章节；
- 仍无法解决，请在 [GitHub Issues](https://github.com/lahuolou/Lnh-Music-Tag/issues) 提交，请附带：
  - 版本号（「设置」页可见，或 `cat VERSION`）
  - 部署方式（Docker / 本地）、操作系统与架构
  - 复现步骤、相关日志、报错截图

---

## 开源许可

本项目基于 **GNU General Public License v3.0（GPL-3.0）** 开源，详见 [LICENSE](./LICENSE)。

> 再次提醒：本项目为学习参考用途的管理工具，不含任何音源内容，使用风险自负。
