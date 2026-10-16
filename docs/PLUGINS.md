# LNH-MusicTag 插件开发指南

版本：v1.6.2（与主程序同步）｜ 仓库：github.com/lahuolou/lnh-musictag

本文档说明 LNH-MusicTag 的插件框架：插件分**内置**与**第三方**两组，
第三方插件以 **ZIP 插件包**形式导入（解压即用），也兼容纯 JSON 声明。
内置插件只有开关、不可删除；第三方插件可导入、启停、删除，
**重复导入同名插件 = 覆盖升级（保留已填配置）**。

## 一、插件是什么

插件注册进统一插件注册表后，前端「插件」页自动按类型分组渲染卡片
（刮削源 / 工具 / 第三方），启停与配置持久化到 SQLite
（settings 表 `plugins` 键），重启后自动恢复。

插件分为四类（`kind`）：

| kind       | 说明                                    | 示例                        |
|------------|-----------------------------------------|-----------------------------|
| `scrape`   | 刮削源 / 音源（元数据、歌词、封面）       | `scrape.qq`、`scrape.netease` |
| `tool`     | 工具                                    | `tool.ffmpeg`、`tool.acoustid` |
| `download` | 下载器（预留）                           | —                           |
| `protocol` | 服务端协议（预留，暂无内置实例）           | —                           |

> v1.6.2 起已移除内置协议输出插件（Subsonic / Navidrome / Jellyfin / Emby），
> 但插件框架仍保留 `protocol` 类型，后续如需接入可在源码中新增内置协议插件，
> 或由第三方插件包以该类型注册（仅需开关/配置管理，宿主不代为实现协议）。

## 二、内置刮削源与并发调度

内置刮削源在 `internal/scrape/` 实现统一的 `Source` 接口，注册进
`registerBuiltinPlugins`（`plugins_api.go`）联动启停。**所有启用的刮削源
会同时被查询（v1.5.0 起 `SearchWithFallback` 全并发），结果合并后由
`pickBest` 跨源挑出最佳匹配再写入标签**。

新增一个刮削源只需：

1. 在 `internal/scrape/` 实现 `Source` 接口（Search 返回标准命中结构）；
2. 在 `main.go` 的源注册表追加实例（含语言分组：国内/国际）；
3. 在 `plugins_api.go` 的 `registerBuiltinPlugins` 注册插件（`scrape.xxx`），
   启停回调调用 `SetEnabled("xxx", on)`。

接入后自动获得以下能力，无需额外工作：

- 批量刮削 / 一键刮削 / 歌词 / 海报 / 编辑面板搜索全部走并发合并；
- 源请求带全局超时与限速（网易云歌词另设 3s 短超时）；
- 插件页开关、Key 配置（NeedsKey / KeyField）与持久化。

## 三、ZIP 插件包（推荐，导入即用）

ZIP 插件包是**自包含**的插件：一个 `plugin.json` 声明 + 各平台可执行入口，
导入后宿主自动解压并注册，无需重新编译镜像。

### 3.1 包结构

```
myplugin.zip
├── plugin.json
└── bin/
    ├── myplugin-linux-amd64
    ├── myplugin-linux-arm64
    └── myplugin-windows-amd64.exe
```

### 3.2 plugin.json 声明格式

```json
{
  "name": "tool.myplugin",
  "label": "我的插件",
  "kind": "tool",
  "version": "1.0.0",
  "hint": "一句话说明",
  "entries": {
    "linux": "bin/myplugin-linux-amd64",
    "darwin": "bin/myplugin-linux-amd64",
    "windows": "bin/myplugin-windows-amd64.exe"
  }
}
```

| 字段      | 必填 | 说明                                                        |
|-----------|------|-------------------------------------------------------------|
| `name`    | ✅   | 全局唯一标识，如 `tool.xxx`。**重复导入同名插件 = 覆盖升级，已填配置保留**；内置插件（`scrape.*`/`tool.ffmpeg` 等）不可覆盖。 |
| `label`   | ✅   | 展示名（可含中文）                                            |
| `kind`    | ✅   | `scrape` / `tool` / `download` / `protocol`                  |
| `version` |      | 插件版本号                                                    |
| `hint`    |      | 展示说明（i18n key 或纯文本均可）                              |
| `entries` | ✅   | 平台 → 包内相对入口路径；宿主按运行平台选择入口执行。          |
| `fields`  |      | 配置字段数组（同 JSON 插件），前端据此渲染配置表单。            |
| `panel`   |      | 主界面 Web 面板入口：声明后「编辑/批量/去重」之后自动出现该插件 tab，以 iframe 嵌入插件自带 UI（单文件入口或多资源目录）。 |

### 3.3 宿主调用协议（run）

宿主以固定 JSON 协议调用插件入口，无 shell、无环境变量注入：

```bash
<entry> run --input <in.json> --output <out.json>
```

- `in.json`：`{"op":"<操作名>", ...args}`，`op` 与其余参数由宿主透传。
- `out.json`：插件返回的任意 JSON 对象（宿主原样透传给前端）。

规范要求：

- 插件入口必须支持 `run --input <path> --output <path>` 两个参数；
- 出错时返回 `{"ok":false,"error":"<可读错误>"}`，退出码非 0 亦可；
- 输出文件写到 `outDir` 下（宿主不指定具体文件名，插件自定）；
- 处理尽量幂等、超时自控（宿主侧默认 120s 超时）。

### 3.4 导入、覆盖升级与卸载

- **导入**：插件页「上传导入」选择 `.zip` 包（可多选）。宿主校验
  `plugin.json`、`entries` 平台入口、防 zip-slip 解压到
  `/config/plugins/<name>/`，导入即注册（默认开启）并持久化，重启保留。
- **覆盖升级**：重复导入同名 ZIP 包时，宿主先删除旧解压目录再解压新包，
  **保留旧插件已填的 Config（Key/Cookie 等）**，仅更新定义（Label/Version/
  Fields/Hint/Entries/Panel）；内置插件命中时返回 400「内置插件不可覆盖」。
- **卸载**：插件页「删除插件」（两次点击确认），删除注册记录并清理
  解压目录，重启不会复活。
- 内置插件只有开关、不可删除；删除接口对内置插件返回明确错误。

### 3.5 前端卡片

第三方插件（ext 标签）在「插件」页以卡片展示：卡片头常驻
（名称 + 开关 + 删除），**配置区默认折叠**，点卡片头展开/收起（▲▼）。

## 四、纯 JSON 插件（轻量声明）

适合「只需要开关与配置管理、无需执行入口」的场景：

```json
{
  "name": "scrape.mysource",
  "label": "我的源",
  "kind": "scrape",
  "version": "1.0.0",
  "hint": "ffHint",
  "fields": [
    { "key": "api_url", "label": "API 地址", "type": "text", "placeholder": "https://..." },
    { "key": "token", "label": "Token", "type": "password" }
  ]
}
```

导入支持三种形态：单个对象 / `{"plugins":[...]}` / 裸数组。
导入校验规则：`name` / `label` 非空、`kind` 合法；`name` 命中已注册插件时，
内置插件拒绝（400），第三方插件**覆盖升级并保留原配置**。

对应接口：`POST /api/plugins/import`（ZIP 二进制或 JSON 文本，上限 256MB）、
`DELETE /api/plugins/{name}`、`POST /api/plugins/{name}/run`（调用 ZIP 插件入口）。

## 五、内置插件接入（源码扩展点）

内置插件在 `registerBuiltinPlugins`（`plugins_api.go`）注册，只允许开关：

1. **刮削源（scrape）**：在 `internal/scrape/` 实现 `Source` 接口，
   然后追加注册并联动启停：
   ```go
   registerPlugin(&Plugin{
       Name: "scrape.mysource", Label: "我的源", Kind: PluginScrape,
   }, func(n string, on bool, _ map[string]string) {
       ms.SetEnabled("mysource", on)
   })
   ```
2. **工具（tool）**：实现 `applyFn` 生效回调，读取 `plugin.Config` 执行。
3. **协议（protocol）**：如未来内置协议插件，同样在此注册（参考 v1.6.0~1.6.1
   曾内置的 `protocol.subsonic` / `protocol.media`，v1.6.2 已移除）。

## 六、API 一览

| 方法   | 路径                          | 说明                                    |
|--------|-------------------------------|-----------------------------------------|
| GET    | `/api/plugins`                | 列出全部插件（含启停 / 配置 / Key 状态）  |
| POST   | `/api/plugins`                | 批量启停 / 保存配置，持久化并即时生效      |
| POST   | `/api/plugins/import`         | 导入第三方插件（ZIP 包或 JSON 声明；同名覆盖保留配置） |
| DELETE | `/api/plugins/{name}`         | 删除第三方导入插件（内置不可删）           |
| POST   | `/api/plugins/{name}/run`     | 调用 ZIP 插件入口（op + 参数）            |
| GET    | `/api/plugins/{name}/panel` · `/api/plugins/{name}/panel/{path...}` | 插件 Web 面板静态服务（单文件/多资源，防路径穿越） |
| POST   | `/api/fs/scan-dir`            | 通用目录扫描（按后缀列文件），供插件面板复用 |
| GET    | `/api/ffmpeg/status`          | FFmpeg 可用状态与版本                     |
| POST   | `/api/ffmpeg/install`         | 容器内一键安装 FFmpeg（**自动持久化到配置卷** `$LNH_CONFIG_DIR/ffmpeg/`，镜像更新重建容器后自动认领，无需重装） |
| POST   | `/api/ffmpeg/uninstall`       | 卸载并清理配置卷持久化产物                 |
