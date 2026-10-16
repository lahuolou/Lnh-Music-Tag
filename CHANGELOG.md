# LNH-MusicTag 更新日志

版本号与 `VERSION` 文件、GitHub Release tag（`v<版本>`）保持一致。本文档汇总各版本主要变更；发布说明细节见各 Release。

---

## v1.6.2（当前）

**移除全部协议输出功能 + 质量轮**

- **移除 MusicBridge 内置插件**（`internal/musicbridge/` 全目录、`musicbridge.go` / `mb_restapi.go` / `mb_ssapi.go`、`/api/musicbridge/v1` 统一 REST、`/mb/rest/*` Subsonic 兼容入口），并清理前端管理面板、i18n 文案与数据库表。
- **移除本地四协议输出**（`subsonic.go` / `navidrome.go` / `jellyfin.go` / `protodebug.go` 与 `/rest/*`、`/emby/*`、`/jellyfin/*`、Navidrome `/api/*` 路由及 `protocol.subsonic` / `protocol.media` 内置插件注册），项目恢复为不含协议输出的纯标签管理工具。
- **清理**：数据库协议插件记录、`subsonic_pass`/`media_pass` 凭证键、`dataRev` 死代码、协议相关注释；插件页与 README / 开发文档 / 插件开发指南同步重写（删除协议章节）。
- **移除 download / protocol 预留类型**：插件框架只保留 `scrape` / `tool` 两类（`plugin.go` 枚举、导入校验、错误文案、i18n 注释同步删除）；删除从未使用的 `registerBuiltinPlugins` `cfg` 参数；删除前端旧 `web/src/ext-unlock` 内置面板残留（解锁已由第三方插件承载）；开发文档与插件开发指南同步更新。
- 交互/渲染冒烟、`go vet`、`go test ./...`、`npm run build`、双实例回归全部通过；协议入口确认返回 404；无封面曲目的 cover 404 确认为正常业务行为。

## v1.6.1

- 交互与页面渲染冒烟验证（登录 → 列表 → 插件页 → 设置页 → 批量页全流程）。
- 开发文档补 Subsonic 章节与版本记录；清理冗余产物。

## v1.6.0

- **内置 Subsonic API 协议输出插件**（`protocol.subsonic`，`/rest/*`，XML/JSON）：浏览 / 搜索 / 在线播放（Range）/ 下载 / 封面 / 歌词 / 收藏 / 评分 / 播放记录；token / 明文 / HTTP Basic 三种认证。
- 插件安全加固：Key 配置保存脱敏（`********` 不覆盖真实值）、列表接口不再回显真实 Key。
- CI 发布流程：tag `v<版本>` 触发 GitHub Actions 自动构建镜像并推送 ghcr。

## v1.5.4

- **FFmpeg 持久化安装**：一键安装后二进制与动态库复制到配置卷 `/config/ffmpeg/`，镜像更新重建容器自动认领，无需重装；新增卸载接口。
- 文档精简重写；`LNH_INSTALL_FFMPEG=true` 启动安装保持幂等。

## v1.5.3

- 乱码修复 / 繁体检测 / 简繁转换覆盖全部 11 个文本元素（新增作词 / 语言 / 专辑类型）。
- 修复 GBK 字节恰好构成合法 UTF-8 时的误改（UTF-8 重组与 GBK 解码双候选取优）。

## v1.5.2

- 一键刮削进度条改为「当前阶段进度」（去重 / 乱码修复 / 简繁转换 / 刮削各自独立计数）。
- 插件页体验：第三方插件卡片可折叠；重复导入同名插件 = 覆盖升级并保留已填配置。

## v1.5.1

- 修复一键刮削「自动去重」在慢盘/网络盘卡死（0/N 假死）且取消无效：哈希循环改为并发 + 可取消 + 单文件超时兜底，取消即时生效。

## v1.5.0

- **多源并发刮削**：全部启用源同时查询、结果合并，`pickBest` 跨源挑最佳匹配后写入；批量 / 一键 / 歌词 / 海报 / 搜索共用此链路。
- 编辑面板补全 **29 个元素**；「设置 → 编辑面板显示字段」可自定义勾选并持久化。

## v1.4.10

- 代码精简与维护：gofmt 全量规范化、删除死代码与冗余、修复过时单测。

## v1.4.9

- 封面命中率优化：防盗链 Referer、失败自动重试、响应校验、专辑级封面缓存、纯标题兜底、`hasCover` 持久化修复。
- 手机端适配（900px / 600px 两档媒体查询）。

## v1.4.8

- **第三方插件 Web 面板机制**：`plugin.json` 支持 `panel` 字段，主界面动态 tab 以 iframe 嵌入插件 UI；面板静态服务带路径穿越防护。
- 通用目录扫描接口 `POST /api/fs/scan-dir`；插件导入/删除后主界面 tab 实时刷新；插件页内置「插件开发说明」。
