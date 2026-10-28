# 部署与运维

本文介绍 Lnh-Music-Tag 的部署方式、配置、数据管理与升级运维。

---

## 1. 部署方式

### 1.1 Docker Compose（推荐）

镜像发布在 Docker Hub（无需本地构建），GitHub GHCR 作为备用源。下面这份 `docker-compose.yml` 与仓库一致，按需修改即可：

```yaml
services:
  lnh-music-tag:
    # 服务器/1Panel 部署直接用 Docker Hub 公开镜像（无需本地构建）；GHCR 备用源：ghcr.io/lahuolou/lnh-music-tag:latest
    image: lahuolou/lnh-music-tag:latest
    container_name: lnh-music-tag
    ports:
      - "10248:10248"        # 宿主机 10248 -> 容器 10248（改用非常用端口）
    volumes:
      # 默认音乐目录（NAS 路径）。应用会把标签/封面写回文件，所以必须可读写，别改成 :ro
      - /mnt/sata3-1/mp3:/music
      # 配置卷：持久化后台初始/修改后的密码（admin.json）、数据库配置、第三方插件包
      # （插件 ZIP 导入后解压到 /config/plugins/，重启不丢失）
      - lnh_config:/config
    environment:
      # 账号/密码/Key 默认存数据库（/config/lnh.db），以下环境变量仅作【首次启动】的一次性初始化种子
      - LNH_ADMIN_USER=admin
      - LNH_ADMIN_PASS=          # 留空则首次生成随机初始密码打印到日志，登录后在“设置”页修改（存数据库）
      # 可选：AcoustID 录音识别 Key（在「插件 → AcoustID」页填写，存数据库）
      # - ACOUSTID_API_KEY=        # 已弃用：改为插件页配置，环境变量不再读取
      # FFmpeg（格式转换）安装开关：默认开启，容器启动时自动 apk 安装 ffmpeg（需 root 运行）；
      # 设 false/留空 则不自动装，可改用：插件页“一键安装”，或挂载宿主机 /usr/bin 并在插件页填路径。
      - LNH_INSTALL_FFMPEG=true
    restart: unless-stopped
    # 健康检查：/health 存活探针（容器内 wget 由 busybox 提供）。
    # 安全边界：默认配置仅适合可信内网；公网暴露务必通过 Caddy/Nginx/Traefik
    # 反向代理启用 HTTPS，并配置可信代理头（详见 docs/DEPLOYMENT.md）。
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:10248/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s

volumes:
  lnh_config:
```

```bash
# 1. 拉取项目（或仅复制 docker-compose.yml）
git clone https://github.com/lahuolou/Lnh-Music-Tag.git
cd Lnh-Music-Tag

# 2. 编辑 docker-compose.yml：
#    - volumes 下把 /mnt/sata3-1/mp3 换成你的音乐目录（必须可读写，标签会写回文件）
#    - LNH_ADMIN_PASS 填入首次登录密码（留空则随机生成并打印到日志）
#    - LNH_INSTALL_FFMPEG=true 保持默认（自动安装 FFmpeg）

# 3. 启动
docker compose up -d
```

- 访问：`http://服务器IP:10248`
- 持久化：配置/数据库/回收站/插件数据保存在命名卷 `lnh_config`

### 1.2 Docker run（等效，备选）

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /你的/音乐/目录:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=你的密码 \
  -e LNH_INSTALL_FFMPEG=true \
  lahuolou/lnh-music-tag:latest
```

### 1.3 本地构建运行

要求：Go 1.27+、Node.js 18+（构建前端）。

```bash
# 前端（产物嵌入二进制）
cd web && npm install && npm run build && cd ..

# 后端
go build -o lnh-music-tag .

# 运行（Windows 示例；Linux/macOS 等价）
$env:LNH_CONFIG_DIR="D:\config"; $env:LNH_PORT=":10248"
.\lnh-music-tag
```

### 1.4 1Panel / NAS（群晖等）

- **1Panel**：应用商店或「容器 → 编排」粘贴 `docker-compose.yml` 内容创建；编辑卷路径为 NAS 音乐目录，端口映射按需；
- **群晖 Container Manager**：同样使用 compose 项目创建；
- 注意：**NAS 目录挂载必须可写**（默认 SMB/NFS 共享即可，勿加 `:ro`）。

---

## 2. 环境变量（首次启动种子）

| 变量 | 默认 | 说明 |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP 监听地址 |
| `LNH_CONFIG_DIR` | `/config` | 配置目录（数据库、回收站、插件数据） |
| `LNH_ADMIN_USER` | `admin` | 初始管理员账号 |
| `LNH_ADMIN_PASS` | 随机 | 初始管理员密码（留空打印到日志） |
| `LNH_STREAM_PASS` | 空 | 流媒体密码（网关兜底；优先插件页 `stream_pass`） |
| `LNH_INSTALL_FFMPEG` | `true` | 容器启动时自动安装 FFmpeg（需 root；`false/0` 关闭） |

> 账号、密码、API Key、插件配置默认保存在配置数据库 `config/lnh.db`，环境变量**仅在首次启动**初始化。首次登录后请在「设置」页修改密码。

---

## 3. 数据与备份

| 数据 | 位置 | 说明 |
|---|---|---|
| 配置数据库 | `<config>/lnh.db` | 账号/密码/设置/插件状态/任务历史/操作日志 |
| 回收站 | `<config>/trash/` + `_manifest.jsonl` | 删除/去重的文件先移入，可恢复 |
| 第三方插件 | `<config>/plugins/` | ZIP 导入后解压于此 |
| 曲目库 | 扫描时实时读取音乐文件 | 曲目列表持久化于 lnh.db（tracks 表） |

**备份**：只需备份 `lnh_config` 卷（或 `config` 目录）——`docker run --rm -v lnh_config:/data -v $(pwd):/backup alpine tar czf /backup/lnh_config.tar.gz /data`。音乐文件本身不在备份范围（标签已写回文件）。

---

## 4. 升级

镜像使用 `latest` 标签（GHCR 公开镜像），升级即**拉取新镜像并重建容器**：

```bash
# docker compose 方式（在 docker-compose.yml 所在目录）
docker compose pull && docker compose up -d
```

- **1Panel / NAS（群晖等）**：进入「容器」→ 找到 `lnh-music-tag` → 点击**重建**（会拉取 latest 新镜像）；或在「容器 → 编排」中对原有 compose 点**更新/重建**；
- **验证升级**：升级后进入应用「设置」页或访问 `http://<主机>:10248/api/version`，确认版本显示 `1.0.16`（应用内「检查更新」在容器环境仅作版本提示，实际升级以镜像重建为准）；
- 数据库自动兼容旧版本；升级不丢失配置/曲目列表/回收站；
- 版本与 Release 见 [GitHub Releases](https://github.com/lahuolou/Lnh-Music-Tag/releases)。

---

## 5. 常见问题

### 5.1 端口被占用 / 无法访问

- 检查 `10248` 是否被占用：`docker compose ps` / `netstat -ano | findstr 10248`；
- 云服务器需在安全组放行 TCP 10248；NAS 需确认防火墙与反代规则。

### 5.2 扫描不到文件 / 播放失败

- 确认容器内路径与扫描路径一致：compose 挂载 `/music`，网页里也填 `/music`；
- 确认音乐目录**可读写**（应用会写回标签/封面）。

### 5.3 FFmpeg 未就绪

- 容器：`LNH_INSTALL_FFMPEG=true` 需容器以 root 运行；若安装失败，可在「设置 → 插件 → FFmpeg」点「一键安装」或填写宿主机可执行文件路径（容器内可访问）；
- 本地：确认系统 PATH 中有 ffmpeg，或在插件页填绝对路径。

### 5.4 忘记登录密码

- 停止容器 → 挂载 `lnh_config` 卷手动删除/重置 `admin.json` 或数据库中的账号密码 → 重启后用环境变量重新初始化；或直接重建配置卷（会丢失设置与回收站）。

---

## 6. 安全边界与公网部署（必读）

默认配置面向**可信内网**（局域网 / NAS / 个人服务器）。将服务暴露到公网前必须启用 HTTPS。

### 6.1 默认安全属性

- 登录、流媒体网关认证（Subsonic / Navidrome / Jellyfin / Emby）均有**失败限速**：同一 IP 连续 5 次认证失败锁定 5 分钟，防止暴力破解；
- 会话 Cookie 为 `HttpOnly` + `SameSite=Lax`；代理 HTTPS 场景下 `Secure` 由代理侧判定；
- 管理 API 统一走会话认证 + CSRF 校验 + 请求体限制；
- 健康端点 `/health`（存活）与 `/ready`（SQLite 可读）不泄露路径/密钥/内部错误，可直接用于 Docker healthcheck（docker-compose.yml 已内置）。

### 6.2 公网反向代理（HTTPS）

应用自身不终止 TLS。公网访问请用反向代理启用 HTTPS，并传递真实客户端 IP 与协议头：

**Caddy（自动 HTTPS 证书）**

```caddyfile
music.example.com {
    reverse_proxy 127.0.0.1:10248
}
```

**Nginx**

```nginx
server {
    listen 443 ssl;
    server_name music.example.com;
    # 配置证书：ssl_certificate / ssl_certificate_key
    location / {
        proxy_pass http://127.0.0.1:10248;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

### 6.3 建议

- 使用强随机密码（不要依赖限速替代强密码）；
- 定期备份 `/config` 卷与音乐库；
- 无公网需求时保持仅内网访问即可。

## 7. 相关文档

- [README.md](../README.md)：快速上手
- [API.md](./API.md)：API 参考
- [STREAMING_GATEWAY.md](./STREAMING_GATEWAY.md)：播放器接入
- [PLUGINS.md](./PLUGINS.md)：插件系统
