# 插件系统开发指南

Lnh-Music-Tag 的刮削源、FFmpeg、AcoustID、音乐统一网关都以「插件」形式注册在统一注册表中；用户可在 Web「插件」页启停、配置、导入第三方插件包。

---

## 1. 插件模型

每个插件在注册表中是一个 `Plugin` 结构：

| 字段 | 说明 |
|---|---|
| `name` | 唯一标识，如 `scrape.qq`、`tool.ffmpeg` |
| `label` | 展示名称 |
| `kind` | 类型：`scrape`（刮削源）/ `tool`（工具） |
| `enabled` | 是否启用 |
| `config` | 插件配置（键值对，Key 类字段脱敏展示） |
| `needsKey` | 是否需要 API Key 才能工作 |
| `available` | 当前是否可用（如 FFmpeg 未探测到二进制 = 不可用） |
| `unavailable` | 不可用原因（前端展示为红色警告） |

内置插件一览：

| name | label | 说明 |
|---|---|---|
| `scrape.auto` | 自动 | 内置兜底源，按源顺序自动降级（不可停用） |
| `scrape.qq` / `scrape.netease` / `scrape.kugou` / `scrape.kuwo` / `scrape.migu` / `scrape.bilibili` / `scrape.qishui` / `scrape.bodian` / `scrape.5sing` / `scrape.musicbrainz` / `scrape.itunes` / `scrape.listenbrainz` / `scrape.deezer` / `scrape.lastfm` / `scrape.spotify` / `scrape.discogs` / `scrape.jamendo` | 各源名 | 17 个元数据源 |
| `tool.ffmpeg` | FFmpeg | 格式转换 / 流媒体转码 / 音频指纹 |
| `tool.acoustid` | AcoustID | 音频识别 API Key |
| `tool.streaming_gateway` | 音乐统一网关 | Subsonic / Jellyfin / Emby / Navidrome 四协议 |

---

## 2. 启停与配置门控

插件更新接口 `POST /api/plugins`（Body 见 [docs/API.md](./API.md#8-插件)）带以下门控：

- `scrape.auto`：内置兜底源，强制保持启用
- `tool.ffmpeg`：开启时未探测到 FFmpeg 二进制 → 拒绝开启（提示安装或填写路径）
- `tool.acoustid`：开启时未配置 API Key → 自动停用
- `tool.streaming_gateway`：**开启时未配置流媒体密码（`stream_pass`）→ 拒绝开启**（返回 400：请先设置流媒体密码）

配置 Key 类字段（API Key、流媒体密码）同步写入配置库（SQLite），日志中脱敏为 `***`，前端用脱敏占位符回显，未改动时不回写。

---

## 3. 第三方插件包（ZIP）

第三方插件以 ZIP 包导入（`POST /api/plugins/import`，Body 为原始 ZIP 字节，可选 `X-File-Name` 头），解压到配置目录 `plugins/<name>/`。

### 3.1 包结构

```
my-plugin.zip
├── plugin.json        # 必需
├── bin/
│   └── <平台入口>      # 可执行入口（见 entries）
└── ui/
    └── index.html     # 可选：Web 配置面板
```

### 3.2 plugin.json 声明

```json
{
  "name": "my.scraper",
  "label": "我的刮削源",
  "kind": "scrape",
  "version": "1.0.0",
  "hint": "插件说明文案",
  "fields": [
    { "key": "api_key", "label": "API Key", "type": "password" }
  ],
  "keyField": "api_key",
  "entries": {
    "windows-amd64": "bin/plugin.exe",
    "linux-amd64": "bin/plugin",
    "linux-arm64": "bin/plugin-arm"
  },
  "panel": "ui/index.html"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | 唯一标识（`scrape.` / `tool.` 前缀建议保留） |
| `label` | 是 | 展示名称 |
| `kind` | 否 | `scrape` / `tool` |
| `entries` | 是 | 平台 → 入口文件的映射（当前平台缺失则拒绝导入） |
| `panel` | 否 | Web 面板入口（zip 内相对路径） |
| `fields` / `keyField` | 否 | 配置表单字段定义 |

### 3.3 安全约束

- 解压目标限定在 `plugins/<name>/` 内（防 zip-slip 路径穿越）
- `entry` / `panel` 必须是包内相对路径，禁止绝对路径与 `..`
- 包大小上限 256 MiB

---

## 4. 插件运行协议

宿主以固定 JSON 协议调用插件入口。`POST /api/plugins/{name}/run` 触发运行，入口程序按宿主约定读取输入并返回结构化结果（实现细节以 `plugin_runner.go` 为准）。

第三方插件面板通过 `GET /api/plugins/{name}/panel` 与 `/panel/{path...}` 提供静态页面；面板内可使用 `POST /api/fs/scan-dir`（Body：`{"dir":"..."}`）探测目录结构。

---

## 5. 开发内置插件（以刮削源为例）

1. 在 `internal/scrape` 实现 `Source` 接口：
   - 搜索候选（标题/艺术家）
   - 拉取详情（标签、封面、歌词）
2. 在 `registerBuiltinPlugins`（`main.go`）注册为 `scrape.<name>` 插件，配置 `NeedsKey` / `Config` 字段
3. 若需 API Key：在 `pluginKeyFields`（`plugins_api.go`）登记 key 字段，启用门控见第 2 节
4. 前端：`i18n.js` 添加源名，`BatchPanel.vue` 源下拉框自动读取插件列表（`enabled` 为 false 的源不显示）
5. 测试：`go test ./...`，参考现有 `gateway_features_test.go` / `fixencoding_test.go` 写法
