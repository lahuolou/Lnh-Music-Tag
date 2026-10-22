# 部署与运维

本文介绍 Lnh-Music-Tag 的部署方式、目录约定、环境变量与常见运维操作。

---

## 1. 运行形态

Lnh-Music-Tag 是**单二进制**应用：前端静态资源（Vue 构建产物）经 `go:embed` 内嵌进后端，一个进程、一个端口（默认 `10248`）同时提供 Web 管理界面与流媒体网关。

依赖（可选）：FFmpeg（格式转换 / 流媒体转码 / 音频指纹）。未安装时除「格式转换」与「转码流媒体」外其余功能全部可用（流媒体自动回退直传）。

---

## 2. 目录与数据约定

| 路径 | 内容 | 说明 |
|---|---|---|
| `/music`（挂载卷） | 你的音乐库 | 只读也可，但写回标签需要可写，**勿挂 `:ro`** |
| `/config`（持久卷） | 配置数据库 `lnh.db` | 管理员账号、密码、插件状态与配置、API Key |
| `/config/plugins/` | 第三方插件包 | ZIP 导入后解压于此，重启不丢失 |
| `/config/trash/` | 回收站 | 删除/去重的文件先移入；`_manifest.jsonl` 记录原路径用于恢复 |

---

## 3. 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP 监听地址（如 `:8080`、`0.0.0.0:10248`） |
| `LNH_CONFIG_DIR` | `/config` | 配置目录（建议持久化） |
| `LNH_ADMIN_USER` | `admin` | 首次启动管理员账号 |
| `LNH_ADMIN_PASS` | 随机 | 首次启动管理员密码；留空则打印到日志 |
| `LNH_STREAM_PASS` | 空 | 流媒体密码兜底（推荐在插件页配置 `stream_pass`） |
| `LNH_INSTALL_FFMPEG` | `false` | `true/1/on` 时容器启动自动 `apk add ffmpeg`（需 root） |

> 账号/密码/Key 写入 `config/lnh.db` 后，环境变量不再生效（仅首次启动种子）。

---

## 4. Docker 部署

### 4.1 直接运行

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /mnt/music:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=change-me \
  ghcr.io/lahuolou/lnh-music-tag:latest
```

### 4.2 docker-compose

仓库自带 `docker-compose.yml`：

```yaml
services:
  lnh-music-tag:
    image: ghcr.io/lahuolou/lnh-music-tag:latest
    container_name: lnh-music-tag
    ports:
      - "10248:10248"
    volumes:
      - /mnt/sata3-1/mp3:/music      # 改成你的音乐目录（可写）
      - lnh_config:/config
    environment:
      - LNH_ADMIN_USER=admin
      - LNH_ADMIN_PASS=              # 留空首次启动随机生成并打印日志
      - LNH_INSTALL_FFMPEG=false
    restart: unless-stopped

volumes:
  lnh_config:
```

```bash
docker compose up -d
```

### 4.3 1Panel 部署

1. 镜像仓库填 `ghcr.io/lahuolou/lnh-music-tag`，标签 `latest`
2. 端口映射：宿主机端口（如 `10248`）→ 容器 `10248`
3. 存储卷：容器目录 `/music` 挂载到音乐库；`/config` 挂载持久卷
4. 环境变量按上表配置
5. 容器须能访问外网（刮削与更新检查用）

---

## 5. 本地部署（Windows / Linux / macOS）

要求：Go 1.27+、Node.js 18+（仅构建前端时需要）

```bash
# 构建
cd web && npm install && npm run build && cd ..
go build -o lnh-music-tag .

# 运行（示例）
LNH_CONFIG_DIR=./config LNH_PORT=:10248 ./lnh-music-tag
```

首次启动：无 `LNH_ADMIN_PASS` 时日志会打印随机初始密码；浏览器打开 `http://localhost:10248`，用管理员账号登录后在「设置」修改密码。

---

## 6. FFmpeg 安装

三种方式：

1. **容器启动自动安装**：`LNH_INSTALL_FFMPEG=true`（需 root 运行容器）
2. **Web 一键安装**：设置 → 插件 → FFmpeg → 一键安装（下载官方静态构建）
3. **本机路径**：设置 → 插件 → FFmpeg → 填写可执行文件绝对路径（如 `/usr/bin/ffmpeg`、`C:\ffmpeg\bin\ffmpeg.exe`）

安装/卸载均有 5 分钟超时保护；版本探测带 10 分钟缓存，避免频繁探测。

---

## 7. 备份与恢复

- 备份 `/config` 整个目录即可（含 `lnh.db`、插件配置、回收站清单）
- 恢复：停容器 → 还原目录 → 启动。曲目列表、账号密码、插件状态全部恢复；音乐库文件本身无需备份（数据都在音乐文件里）

---

## 8. 升级

- 镜像部署：`docker compose pull && docker compose up -d`
- 二进制部署：替换二进制重启即可（曲目表结构兼容，启动自动迁移）
- 版本检查：Web「关于」页或 `GET /api/version/check` 会比对 GitHub Release

---

## 9. 常见问题排查

| 问题 | 排查 |
|---|---|
| 打不开管理页 | 端口是否放行（防火墙/安全组）；`LNH_PORT` 是否冲突 |
| 扫描后列表为空 | 音乐目录挂载是否正确、权限可读；容器内路径与宿主机路径别混淆 |
| 标签写不进去 | `/music` 是否可写（勿挂 `:ro`）；文件是否被其他程序占用 |
| 网关连不上 | 见 [docs/STREAMING_GATEWAY.md](./STREAMING_GATEWAY.md) 第 6 节 |
| 转码失败 | FFmpeg 是否安装；路径是否正确 |
| 忘记密码 | 删除 `/config/lnh.db` 中的管理员记录或重置配置库（会同时清掉插件配置，先备份） |

遇到未列出的问题，请到 [GitHub Issues](https://github.com/lahuolou/Lnh-Music-Tag/issues) 提交，附版本号、部署方式、复现步骤与日志。
