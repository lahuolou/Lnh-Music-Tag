# LNH-MusicTag 插件开发指南

版本：v1.5.0（与主程序同步）｜ 仓库：github.com/lahuolou/lnh-musictag

本文档说明 LNH-MusicTag 的插件框架：插件分**内置**与**第三方**两组，
第三方插件以 **ZIP 插件包**形式导入（解压即用），也兼容纯 JSON 声明。
内置插件只有开关、不可删除；第三方插件可导入、启停、删除。

## 一、插件是什么

插件注册进统一插件注册表后，前端「插件」页自动按类型分组渲染卡片
（刮削源 / 工具 / 下载器 / 协议），启停与配置持久化到 SQLite
（settings 表 `plugins` 键），重启后自动恢复。

插件分为四类（`kind`）：

| kind       | 说明                                    | 示例                        |
|------------|-----------------------------------------|-----------------------------|
| `scrape`   | 刮削源 / 音源（元数据、歌词、封面）       | `scrape.qq`、`scrape.netease` |
| `tool`     | 工具                                    | `tool.ffmpeg` |
| `download` | 下载器（预留）                           | —                           |
| `protocol` | 服务端协议（Subsonic 等，预留）           | —                           |

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

### 2.1 包结构

```
myplugin.zip
├── plugin.json
└── bin/
    ├── myplugin-linux-amd64
    ├── myplugin-linux-arm64
    └── myplugin-windows-amd64.exe
```

### 2.2 plugin.json 声明格式

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
| `name`    | ✅   | 全局唯一标识，如 `tool.xxx`。不得与内置/已导入插件重名。       |
| `label`   | ✅   | 展示名（可含中文）                                            |
| `kind`    | ✅   | `scrape` / `tool` / `download` / `protocol`                  |
| `version` |      | 插件版本号                                                    |
| `hint`    |      | 展示说明（i18n key 或纯文本均可）                              |
| `entries` | ✅   | 平台 → 包内相对入口路径；宿主按运行平台选择入口执行。          |
| `fields`  |      | 配置字段数组（同 JSON 插件），前端据此渲染配置表单。            |

### 2.3 宿主调用协议（run）

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

### 2.4 导入与卸载

- **导入**：插件页「上传导入」选择 `.zip` 包（可多选）。宿主校验
  `plugin.json`、`entries` 平台入口、防 zip-slip 解压到
  `/config/plugins/<name>/`，导入即注册（默认开启）并持久化，重启保留。
- **卸载**：插件页「删除插件」（两次点击确认），删除注册记录并清理
  解压目录，重启不会复活。
- 内置插件只有开关、不可删除；删除接口对内置插件返回明确错误。

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
导入校验规则：`name` / `label` 非空、`kind` 合法、`name` 不冲突。

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

## 五、API 一览

| 方法   | 路径                          | 说明                                    |
|--------|-------------------------------|-----------------------------------------|
| GET    | `/api/plugins`                | 列出全部插件（含启停 / 配置 / Key 状态）  |
| POST   | `/api/plugins`                | 批量启停 / 保存配置，持久化并即时生效      |
| POST   | `/api/plugins/import`         | 导入第三方插件（ZIP 包或 JSON 声明）       |
| DELETE | `/api/plugins/{name}`         | 删除第三方导入插件（内置不可删）           |
| POST   | `/api/plugins/{name}/run`     | 调用 ZIP 插件入口（op + 参数）            |
| GET    | `/api/ffmpeg/status`          | FFmpeg 可用状态与版本                     |
| POST   | `/api/ffmpeg/install`         | 容器内一键安装 FFmpeg                     |
