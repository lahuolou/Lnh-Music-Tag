# LNH-MusicTag v1.5.4 发布说明

**FFmpeg 一键安装后持久化到配置卷——镜像更新重建容器无需再装；删除冗余渲染文档；文档全面重写**

## 新增与改进

1. **FFmpeg 持久化安装（解决"每次更新都要重装 FFmpeg"）**
   - 「插件 → FFmpeg」一键安装成功后，自动把 ffmpeg 二进制与动态库复制到配置卷 `$LNH_CONFIG_DIR/ffmpeg/`（默认 `/config/ffmpeg`），并生成带 `LD_LIBRARY_PATH` 的 wrapper 脚本、把插件路径指向它；
   - 探测链新增持久化回退：插件配置路径 → 旧设置键 → `PATH` → 配置卷 wrapper。镜像更新重建容器后系统层安装虽会丢失，但 `/config` 卷保留，**启动即自动认领，无需重新安装**；
   - 卸载（`POST /api/ffmpeg/uninstall`）同步清理配置卷中的持久化产物；
   - `LNH_INSTALL_FFMPEG=true` 的启动安装保持幂等，且即使 apk 安装失败也不影响持久化认领。

2. **文档精简与重写**
   - 删除中间渲染产物 `docs/DEVELOPMENT.html`（权威源为 `docs/DEVELOPMENT.md`）；
   - `README.md`（中/英文）补充 FFmpeg 持久化说明；`docs/DEVELOPMENT.md` 同步更新部署与 FFmpeg 章节；`docs/PLUGINS.md` 更新内置插件与导入说明。

## 验证

- 新增回归测试：
  - `TestPersistFFmpegTo`：二进制与依赖库复制、wrapper 生成、插件 path 写入；
  - `TestFFmpegBinPersistedFallback`：模拟"系统层 ffmpeg 丢失、仅剩配置卷"场景，自动认领持久化安装；
  - `TestShDQ`：wrapper 路径 shell 转义安全；
- `go vet` / `go test ./...` 全绿；本机服务重建重启后 `/api/ffmpeg/status`、登录页与主界面冒烟通过。

## 升级方式

1Panel / Docker 拉取新镜像并重建容器即可（已持久化过 FFmpeg 的不需要再点一键安装）：

```bash
docker pull ghcr.io/lahuolou/lnh-musictag:latest
docker compose up -d
```
