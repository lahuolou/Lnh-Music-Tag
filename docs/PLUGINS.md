# 插件系统

Lnh-Music-Tag 的能力由插件组成：刮削源、FFmpeg、AcoustID、流媒体网关都是插件，可在「设置 → 插件管理」启停与配置；也支持导入**第三方插件**（ZIP 包 + 配置面板）。

---

## 1. 插件模型

每个插件是一个注册单元，包含：

| 字段 | 说明 |
|---|---|
| `name` | 唯一 ID，内置插件形如 `scrape.qq` / `tool.ffmpeg`；第三方插件自定义 |
| `label` | 显示名称 |
| `kind` | 类型：`scrape`（刮削源）或 `tool`（工具） |
| `enabled` | 启停状态（持久化于数据库） |
| `needsKey / keyField` | 是否需要 API Key（需 Key 的插件默认关闭，配置后启用） |
| `fields` | 配置字段（文本/密码等） |
| `version / hint` | 版本号 / 提示文案 |

- **内置插件**：只有开关与配置，不可删除；
- **第三方插件**：ZIP 导入，可删除；覆盖升级保留已填配置。

---

## 2. 内置插件

### 2.1 刮削源（`scrape.*`，17 个 + 自动）

网易云、QQ 音乐、酷狗、酷我、咪咕、B 站、汽水、波点、5sing、MusicBrainz、iTunes、ListenBrainz、Deezer、Last.fm、Spotify、Discogs、Jamendo。

- `scrape.auto`（自动源）**始终可用**，按源优先级自动降级，只调用已开启的源；
- **需 API Key 的源默认关闭**（如 Spotify、Discogs、Jamendo 等），在插件页填 Key 后自动启用；
- 开启的源会出现在批量页的「源选择」里。

### 2.2 工具插件（`tool.*`）

| 插件 | 说明 |
|---|---|
| `tool.ffmpeg` | 格式转换/转码。默认按环境探测结果决定开关；可填 `path` 指向本机 FFmpeg，或一键安装/卸载 |
| `tool.acoustid` | 音频识别。需先配置 AcoustID API Key；存疑曲目（Unknown/乱码/缺艺术家）自动走音频识别补全 |
| `tool.streaming_gateway` | 音乐统一网关（Subsonic / Jellyfin / Emby / Navidrome）。**必须配置流媒体密码**才能启用；四协议统一用该密码认证（用户名任意） |

---

## 3. 第三方插件打包

### 3.1 目录结构

```
my-plugin.zip
├── plugin.json          # 插件声明（必须）
├── bin/                 # 平台入口（可选）
│   ├── linux   / main   # Linux 可执行入口
│   ├── windows / main.exe
│   └── darwin  / main
└── ui/                  # Web 配置面板（可选）
    └── index.html
```

### 3.2 plugin.json 格式

```json
{
  "name": "my-plugin",
  "label": "我的插件",
  "kind": "tool",
  "version": "1.0.0",
  "hint": "可选提示文字",
  "fields": [
    { "key": "token", "label": "API Token", "type": "password" }
  ],
  "entries": {
    "linux": "bin/linux/main",
    "windows": "bin/windows/main.exe",
    "darwin": "bin/darwin/main"
  },
  "panel": "ui/index.html"
}
```

字段说明：

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | 唯一 ID。只允许字母、数字、`.`、`_`、`-`（白名单校验，防路径穿越）；不得与内置插件冲突 |
| `label` | 是 | 显示名 |
| `kind` | 是 | `scrape` 或 `tool` |
| `entries` | 是 | 平台 → 相对入口（相对于 ZIP 根，`bin/xxx`）；运行时按当前平台选择 |
| `panel` | 否 | 配置面板相对路径（如 `ui/index.html`），导入后插件页可打开 |
| `fields` | 否 | 配置字段（`key/label/type=text|password`） |
| `keyField` | 否 | 声明某字段为 API Key（需 Key 逻辑） |
| `version / hint` | 否 | 版本号 / 提示 |

### 3.3 入口协议

- `entries` 指向的可执行文件接收 JSON 配置（`fields` 的值 + 已填 Key）作为 stdin 输入，执行后在 stdout 输出结果（`{"ok":true,...}` 或 `{"ok":false,"error":"..."}`）；
- 入口文件在导入时会被设置为可执行（`chmod +x` 等价处理）。

---

## 4. 导入与管理

1. **导入**：插件管理页「导入插件」选择 `my-plugin.zip`；
2. **校验**：导入时立即执行安全检查（见下），不通过则拒绝并提示原因；
3. **启停**：导入后出现在插件列表，可开关、填配置、查看面板；
4. **运行**：`tool` 插件可点「运行」（挂到后台任务，进度可查）；`scrape` 插件参与批量刮削的源选择；
5. **删除**：第三方插件可删除（`DELETE /api/plugins/{name}`），删除会移除解压目录。

---

## 5. 安全约束（导入校验）

| 约束 | 限制 |
|---|---|
| 包大小 | 整个 ZIP ≤ 256 MiB |
| 文件数 | ≤ 10000 |
| 单文件解压大小 | ≤ 256 MiB |
| 插件名 | 白名单字符（字母/数字/`.`/`_`/`-`），禁止路径分隔符 |
| 路径安全 | `entries` / `panel` 必须是 ZIP 内相对路径；解压全程防 zip-slip（目标必须仍在插件目录内） |
| 类型 | `kind` 仅允许 `scrape` / `tool` |

任何一项不满足都会**拒绝导入**，不写入磁盘。

---

## 6. 开发指引（快速开始）

1. 编写入口程序（任意语言，读 stdin JSON、写 stdout JSON），编译出三个平台的可执行文件；
2. 编写 `plugin.json`（见 3.2）与可选 `ui/index.html` 面板；
3. 打包：`zip -r my-plugin.zip plugin.json bin ui`；
4. 在插件管理页导入测试；
5. 面板可通过 `GET /api/plugins/{name}/panel` 访问，静态资源走 `GET /api/plugins/{name}/panel/{path...}`；面板内如需读配置用浏览器会话调用 `/api/plugins` 即可。

### 6.1 示例插件（可直接导入）

以下两个官方示例插件演示第三方插件打包与面板开发，可在「插件管理 → 导入 ZIP 插件」直接导入（ZIP 包从各仓库 Releases 下载）：

| 插件 | 仓库 | 说明 |
|---|---|---|
| 音乐解锁 `tool.unlock` | [lahuolou/unlock-plugin](https://github.com/lahuolou/unlock-plugin) | 将 QQ 音乐（mflac/mgg/qmc）、网易云（ncm）、酷狗（kgm/vpr）、酷我（kwm）、虾米（xm）等加密音乐解密为可播放格式；纯 Go、零外部依赖，Windows/Linux 双平台，内置网页操作面板 |
| 音乐下载器 `musicdl.downloader` | [lahuolou/musicdl-plugin](https://github.com/lahuolou/musicdl-plugin) | 多平台聚合音乐搜索与下载（网易云/QQ 音乐/酷狗/酷我/咪咕/汽水/B 站），多音质（无损 FLAC/320k/192k/标准），默认附带封面与歌词；Windows/macOS/Linux 三平台，内置 Web 控制面板 |

两者均为 `tool` 类第三方插件：`plugin.json` 声明入口与面板、`bin/` 平台可执行文件、`ui/` 面板 HTML，结构与第 3 节打包规范一致，可作为开发参考模板。

---

## 7. 相关文档

- [DEVELOPMENT.md](./DEVELOPMENT.md)：扩展指引（新增刮削源）
- [API.md](./API.md)：插件 API
