# Lnh-Music-Tag

> Self-hosted · Open Source · Music library smart scraper + streaming gateway

Lnh-Music-Tag is a self-hosted music library tool whose **core highlights are scraping and automatic scraping**:

- **Multi-source auto-scrape**: 17 built-in metadata sources (Netease, QQ Music, KuGou, KuWo, Migu, Bilibili, Qishui, Bodian, 5sing, MusicBrainz, iTunes, ListenBrainz, Deezer, Last.fm, Spotify, Discogs, Jamendo) with automatic fallback and dedup — one click fills title, artist, album, cover art and lyrics for the whole library;
- **Scheduled auto-scrape**: pick a frequency and time, the backend runs "scan + scrape" on schedule with zero manual work — no re-entry, every run logged;
- **Watch & silent scrape**: newly added songs are auto-imported and silently scraped (cover/lyrics/all metadata) — no manual action needed;
- **Library management & playback**: batch-edit 30+ tag fields in a web UI, cover/lyrics management, encoding repair, simplified/traditional conversion, content-based dedup; the curated library is also served by a built-in **unified streaming gateway** over **Subsonic / Jellyfin / Emby / Navidrome** protocols — Yingliu, Archer, Jellyfin and dozens of other players connect and play directly;
- **Local-first**: your data lives in a local SQLite database, tags are written back to the files, no cloud dependency, one-command Docker deployment.

**Version**: 1.0.15 ｜ Language: English / [中文](./README.md)

---

## Security Boundary (Read Before Deploying)

> The default configuration targets **trusted intranet use** (home LAN / NAS / personal server). If you expose the service to the **public internet**, you MUST enable HTTPS through a reverse proxy:
>
> - Recommended: Caddy (automatic certificates), Nginx, or Traefik. Proxy port `10248` to 443 and forward the real client IP plus `X-Forwarded-Proto` (examples in [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md#6-安全边界与公网部署必读));
> - The app itself does not terminate TLS; the session cookie can only reliably honor `Secure` behind an HTTPS proxy;
> - Login and streaming-gateway authentication are rate-limited (5 consecutive failures per IP locks for 5 minutes), but do not rely on rate limiting instead of a strong password;
> - Recommended: intranet-only access + a strong random password + regular backups of `/config` and your music library.

---

## Disclaimer (Please Read)

> **This project is only a music library management tool. It does NOT contain, bundle, or provide any audio sources.**
>
> - It only reads and writes metadata (tags, cover art, lyrics, file names, etc.) of audio files **you legally own**;
> - The built-in "scraping" feature fetches **metadata only** (tags/cover/lyrics) from public sites. It never downloads, stores, or distributes any audio content;
> - Make sure you have the legal right to use and share the audio files you manage;
> - This project is **provided for learning and reference purposes only**. The author assumes **no responsibility** for any consequences arising from its use (including but not limited to data loss, file corruption, content compliance issues, or violation of third-party terms of service);
> - By using this project you acknowledge that you have read and agreed to the terms above.

---

## Core Features

### Intelligent Scraping (Core Highlight)
- **Multi-source auto-scrape**: 17 built-in metadata sources (Netease, QQ Music, KuGou, KuWo, Migu, Bilibili, Qishui, Bodian, 5sing, MusicBrainz, iTunes, ListenBrainz, Deezer, Last.fm, Spotify, Discogs, Jamendo) with automatic fallback, dedup and candidate merging
- **One-key pipeline**: encoding fix → traditional-to-simplified → artist-from-filename → full scrape, one click for the whole library
- **Scheduled auto-scrape**: pick a schedule with dropdowns (daily / weekly / custom + time) in Settings — the cron is generated for you; modes: one-key scrape or dedup-then-one-key; no re-entry while running, every run logged, next/last run shown in Settings
- **Watch & silent scrape**: periodically checks the music directory, auto-imports newly added audio files and silently scrapes them (cover/lyrics/all metadata) — no manual action needed;
- **Organize by album/artist (configurable)**: the Settings page offers an "Organize mode" — Off (default, keep original folders) / By album (`AlbumName/`) / By artist (`ArtistName/`, first of multiple artists); after scan/scrape tracks are automatically reorganized into the matching folder under the library root (filename kept, sibling `.lrc` follows; idempotent, never overwrites an existing file);
- **Source circuit-breaking & adaptive backoff**: per-source health tracking with automatic circuit-breaking (skip failing sources), exponential cooldown and instant recovery on success — rate-limited or dead sources no longer slow down batch pipelines;
- **Match confidence & field-level fusion**: candidates scored 0-100 by title/artist/duration weighting; every ID3 field is merged independently from the best-confidence candidate, with an artist gate blocking same-title different-artist impostors;
- **Low-confidence "To review" queue**: low-confidence results are queued automatically; the "To review" filter in the library shows original tags vs. fused candidates with confidence, one-tap confirm/ignore;
- **Batch completion**: scrape only the fields you check; already-filled tags are skipped
- **Audio identification**: untagged files like `track01.mp3` are matched against known tracks by audio fingerprint

### Library Management
- **Directory scan**: scan local music directories and read audio tags; the track list persists across restarts (SQLite)
- **Tag editing**: edit 30+ fields in the web UI — title, artist, album, album artist, genre, year, track number, lyrics and more
- **Cover art**: view, replace, remove embedded covers; one-click fill for tracks without artwork
- **Lyrics**: embedded lyrics and sidecar `.lrc` files both supported; one-click online lyric fetch
- **Smart filters**: garbled / traditional-Chinese / no-lyrics / no-cover / unidentified — quickly find tracks that need attention

### Assisted Organizing
- **Smart dedup**: identify duplicate files by audio content (SHA-256), keep the best-quality copy per group
- **Encoding repair / script conversion / filename cleanup**: restore GBK/UTF-8 misread tags, bulk simplified ↔ traditional conversion, `song.mp3.flac → song.flac`

### Format Conversion
- FFmpeg-based batch transcoding: target format (MP3/FLAC/AAC/OGG/WAV…), bitrate, sample rate
- Outputs are saved beside the originals; **source files are never deleted**
- **FFmpeg is installed automatically at container startup** by default; plugin-page one-click install or a host path also work

### Streaming Gateway
- One gateway, four protocols: **Subsonic / OpenSubsonic, Jellyfin, Emby, Navidrome**
- Standard endpoints: indexes, artist/album/song browsing, search, lyrics, playlists, favorites, Now Playing, play counts, direct/transcoded streaming
- **Auto-import .m3u/.m3u8 playlists**: playlists found under the music library during a scan are imported as gateway playlists (shared across all three protocols); removing the playlist file drops the gateway playlist on the next scan
- **Unified base URL**: point any client at `http://IP:10248` — the gateway auto-selects the protocol from the request (legacy `/rest`, `/jellyfin`, `/emby` suffixes still work)
- **Password required**: a streaming password must be configured before the gateway can be enabled — unauthorized access is blocked
- Works with official and third-party clients: Yingliu, Archer, Jellyfin, Emby, Navidrome and more

### Engineering & Security
- **Plugin system**: scrape sources, FFmpeg, AcoustID and the gateway are all toggleable/configurable plugins; third-party plugins import as ZIP — validated before extraction (name/path checks, zip-slip protection, file-count and size limits); official example plugins: [unlock-plugin](https://github.com/lahuolou/unlock-plugin) (encrypted-music unlock), [musicdl-plugin](https://github.com/lahuolou/musicdl-plugin) (multi-platform music downloader)
- **Trash can**: deleted/deduped files move to a trash can first — restore or permanently delete anytime
- **Activity log**: scan/scrape/convert/scheduled operations are fully traced; reviewable and clearable
- **Robustness**: bounded-concurrency jobs (semaphore), panic recovery to a `failed` state, timeouts on all external process calls, batched DB transactions, single-instance startup lock, graceful shutdown

---

## Quick Start

### Option 1: Docker Compose (Recommended)

Adjust this `docker-compose.yml` directly, then start:

```yaml
services:
  lnh-music-tag:
    # 服务器/1Panel 部署直接用 GHCR 公开镜像（无需本地构建）
    image: ghcr.io/lahuolou/lnh-music-tag:latest
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

Then adjust and start:

1. **Set the music directory**: in `volumes`, replace the host path with your music directory. Tags/covers are **written back** to the files, so the mount must be writable — do not use `:ro`;
2. **Set the initial password**: `LNH_ADMIN_PASS` for first login (leave empty for a random one printed to the log);
3. Start:

```bash
docker compose up -d
```

Open `http://server-ip:10248` in your browser. Config / database / trash / plugin data persist in the named volume `lnh_config`. Upgrading:

```bash
docker compose pull && docker compose up -d
```

### Option 2: Docker run (equivalent, alternative)

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /your/music/dir:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=your_password \
  -e LNH_INSTALL_FFMPEG=true \
  ghcr.io/lahuolou/lnh-music-tag:latest
```

### Option 3: Build & Run Locally

Requirements: Go 1.27+, Node.js 18+ (for the frontend)

```bash
# 1. Build the frontend
cd web
npm install
npm run build
cd ..

# 2. Build the backend (frontend assets are embedded)
go build -o lnh-music-tag .

# 3. Run
LNH_CONFIG_DIR=./config LNH_PORT=:10248 ./lnh-music-tag
```

> If `LNH_ADMIN_PASS` is not set on first start, a random initial password is printed to the log. Change it in Settings after login.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP listen address |
| `LNH_CONFIG_DIR` | `/config` | Config directory (DB, trash can, plugin data) |
| `LNH_ADMIN_USER` | `admin` | Initial admin username |
| `LNH_ADMIN_PASS` | random | Initial admin password (printed to log if empty) |
| `LNH_STREAM_PASS` | empty | Streaming password (legacy compat; prefer `stream_pass` in the plugin page) |
| `LNH_INSTALL_FFMPEG` | `true` | Installs FFmpeg automatically at container startup (needs root); set `false/0` to disable and use plugin-page install or a host path instead |

> Credentials and API keys are stored in the config DB (`config/lnh.db`). Env vars are only used as **first-boot** seeds.

---

## Connecting Music Players

**Set a streaming password first** (Settings → Plugins → Unified Streaming Gateway).

| Protocol | Base URL (example) | Credentials |
|---|---|---|
| Subsonic / OpenSubsonic | `http://IP:10248/rest` | admin user + streaming password |
| Navidrome-compatible | `http://IP:10248` (`/auth/login`, `/api/*`) | same |
| Jellyfin | `http://IP:10248/jellyfin` | same |
| Emby | `http://IP:10248/emby` | same |

Pick the matching protocol when adding the server in your client — common clients (Yingliu, Archer, Jellyfin official App, Emby official App, Navidrome official App, etc.) all work this way. Full endpoint reference: [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md).

---

## Documentation

| Document | Description |
|---|---|
| [README.md](./README.md) | 中文介绍 |
| [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md) | Development: architecture, modules, build, testing, code style |
| [docs/API.md](./docs/API.md) | Backend API reference |
| [docs/PLUGINS.md](./docs/PLUGINS.md) | Plugin system & third-party plugin development |
| [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md) | Streaming gateway protocols & integration |
| [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) | Deployment & operations |
| [CHANGELOG.md](./CHANGELOG.md) | Version history |

---

## Having Issues?

- First check the troubleshooting sections in [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) and [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md);
- Still stuck? Report on [GitHub Issues](https://github.com/lahuolou/Lnh-Music-Tag/issues). Please include:
  - Version (`cat VERSION`, or the Settings page)
  - Deployment method (Docker / local), OS and architecture
  - Reproduction steps, relevant logs, and screenshots

---

## License

Licensed under the **GNU General Public License v3.0 (GPL-3.0)**. See [LICENSE](./LICENSE).

> Reminder: this project is a management tool for learning/reference purposes only. It contains no audio content. Use at your own risk.
