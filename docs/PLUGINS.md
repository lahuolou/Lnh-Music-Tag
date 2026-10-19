# 插件机制开发指南

Lnh-Music-Tag 的刮削源、FFmpeg、AcoustID、音乐网关都以「插件」形式注册在统一注册表中；用户可在 Web「插件」页启停、配置、导入第三方 ZIP 插件包。

## 1. 插件模型

每个插件由 `Plugin` 描述（`plugin.go`）：

| 字段 | 含义 |
|---|---|
| `name` | 唯一标识，内置形如 `scrape.qq`、`tool.ffmpeg`；第三方自定义 |
| `label` | 界面展示名 |
| `kind` | `scrape`（刮削源）或 `tool`（工具） |
| `enabled` | 用户开关意愿（持久化到数据库） |
| `builtin` | 内置插件，不可删除、不可被第三方覆盖 |
| `needsKey` / `keyField` | 是否依赖外部 API Key、对应设置库键名 |
| `available` | **依赖是否就绪**（由 API 层按当前环境实时计算，不持久化） |
| `fields` | 配置输入项：`text` / `password` / `bool` |
| `config` | 已填配置（Key 类字段下发前端时脱敏为 `********`） |

### 开关与就绪分离（门控）

`enabled`（用户想不想开）与 `available`（依赖是否真的就绪）是两个状态：

- `tool.ffmpeg`：探测不到二进制时，`available=false`，GET 快照强制回显关闭、前端禁用开关，POST 强行开启被拒绝（400）。
- `tool.acoustid`：未配置 API Key 时 `available=false`，开关禁用。
- 需 Key 的刮削源（lastfm / spotify / discogs / jamendo）默认关闭，配好 Key 才参与自动刮削。

这样避免「开关开着却用不了」的死状态；未就绪时功能自动回退，不报错中断主流程。

## 2. 内置插件

注册见 `plugins_api.go` 的 `registerBuiltinPlugins`：

- **刮削源 `scrape.*`**（来自 `internal/scrape`，`auto` 为始终启用的聚合伪源）：netease、qq、kugou、kuwo、migu、bilibili、qishui、bodian、fivesing、musicbrainz、itunes、listenbrainz、deezer、lastfm、spotify、discogs、jamendo。其中 lastfm/spotify/discogs/jamendo 需 Key。
- **`tool.ffmpeg`**：格式转换与网关按需转码；支持配置路径、容器内一键安装、配置卷持久化。
- **`tool.acoustid`**：音频指纹识别，需 API Key。
- **`tool.streaming_gateway`**：四协议音乐网关，`stream_pass` 为播放器流媒体密码。

## 3. 第三方 ZIP 插件包

在「插件」页上传 ZIP，导入即解压到 `<config>/plugins/<name>/`，注册后重启不丢失。

### 3.1 包结构

```
my-plugin.zip
├── plugin.json          # 声明（必需）
├── bin/linux/mytool     # 可执行入口（按平台）
├── bin/windows/mytool.exe
└── ui/index.html        # 可选 Web 面板
```

### 3.2 plugin.json

```json
{
  "name": "tool.mytool",
  "label": "我的工具",
  "kind": "tool",
  "version": "1.0.0",
  "hint": "一句话说明",
  "keyField": "my_api_key",
  "fields": [
    { "key": "my_api_key", "label": "API Key", "type": "password", "placeholder": "..." }
  ],
  "entries": {
    "linux": "bin/linux/mytool",
    "windows": "bin/windows/mytool.exe",
    "darwin": "bin/darwin/mytool"
  },
  "panel": "ui/index.html"
}
```

- `name` / `label` / `kind`（`scrape`|`tool`）/ `entries` 为必需；`entries` 键为 GoOS（`linux`/`windows`/`darwin`），值为包内相对入口路径。
- `panel` 可选；声明了就必须存在该文件，主界面会出现对应插件 tab，由 `/api/plugins/{name}/panel` 与其静态资源提供。
- 覆盖导入同名第三方插件时保留用户已填配置；内置插件不可覆盖。

### 3.3 执行协议

宿主通过固定 JSON 协议调用入口（`plugin_runner.go`）：

```
<entry> run --input in.json --output out.json
```

- `in.json` 为 `{op, args...}` 透传；`out.json` 为插件返回的任意 JSON 对象。
- 超时 120 秒；仅当前平台 `entries[GOOS]` 存在时可调用。

### 3.4 安全约束

- 包上限 256 MiB，解压总量上限 512 MiB（防压缩炸弹）。
- 解压限定在 `<config>/plugins/<name>/` 内，拒绝 `..` 穿越（zip-slip）、符号链接、设备/管道等非普通文件。
- 入口必须是包内相对路径，禁止绝对路径。

## 4. 相关 HTTP 接口（均需后台登录会话）

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/plugins` | 列出全部插件快照（含就绪/Key 状态） |
| POST | `/api/plugins` | 启停 / 保存配置 |
| POST | `/api/plugins/import` | 导入 JSON 声明或 ZIP 包 |
| DELETE | `/api/plugins/{name}` | 删除第三方插件（内置拒绝） |
| POST | `/api/plugins/{name}/run` | 调用 ZIP 插件入口 |
| GET | `/api/plugins/{name}/panel`、`/panel/{path...}` | Web 面板与静态资源 |
