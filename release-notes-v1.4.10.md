# LNH-MusicTag v1.4.10

代码精简与维护版本。无新增功能，专注代码质量与稳定性。

## 本次变更

### 代码精简 / 清理
- **gofmt 全量规范化**：23 个 Go 文件统一格式（activity / ffmpeg / fixencoding / identify / main / pipeline / plugins_api / plugin_runner / 各刮削源 / model / taglibx 等）
- **删除死代码**：`ensureHashes` 中未使用的类型声明、插件导入流程中只赋不用的变量
- **消除冗余**：一键刮削流水线重复的匿名结构体收拢为具名类型；删除无意义的命令包装层（`exec.Command` 本身不经过 shell，不存在注入面）；前端删除未使用的样式
- **移除已剥离的 unlock 宿主代码与文档**（解锁能力已由第三方插件独立承载）

### Bug 修复
- **修复单测 `TestApplySkipFilled`**：29 字段模型扩展后测试数据过时，导致智能跳过逻辑在测试中被误判；实现本身正确，已补全测试构造

### 质量验证
- `go vet` 干净、`go test ./...` 全量通过
- 前端 `npm run build` 无警告（32 模块）
- 服务重启后实测：登录 / 21 插件 / 1691 曲目 0 缺封面 / 页面 console 无错误

## 安装
- 源码包：`LNH-musictag.zip`（27.7MB，含 Dockerfile / compose.yml / 开发文档 PDF）
- 或拉取源码自行构建：`go build -ldflags "-s -w" -o lnh-musictag.exe .`
