## v1.4.8 更新内容

### 新增：第三方插件 Web 面板机制
- 插件包 `plugin.json` 增加 `panel` 字段：声明后主界面「编辑/批量/去重」之后自动出现该插件 tab，以 iframe 嵌入插件自带 UI
- 面板静态服务：`GET /api/plugins/{name}/panel`（单文件入口）与 `GET /api/plugins/{name}/panel/{path...}`（多资源：js/css/图片），带路径穿越防护与 MIME 识别
- 通用目录扫描接口 `POST /api/fs/scan-dir`（按后缀列目录文件），供解锁/下载类插件面板复用
- 插件导入/删除后，主界面插件 tab **实时刷新**（无需手动刷新页面；删除后自动切回编辑页）
- CSP 调整：放行同源 iframe 嵌入（frame-ancestors 'self'），跨站嵌入仍禁止
- 插件页内置「📖 插件开发说明」（中英双语）：JSON 运行协议 `run --input in.json --output out.json`、panel 字段、宿主接口与请求头约定

### 说明
- 音乐解锁插件（tool.unlock）以第三方插件形式独立维护：导入 zip 即用，面板含目录扫描、全选/反选、批量解锁与进度/日志
- 密码/Key 管理：插件 Key 在插件页内配置，删除 Key 保存后自动停用该插件
