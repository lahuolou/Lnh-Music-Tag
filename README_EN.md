# Lnh-Music-Tag · Music Library Tag Manager

> Self-hosted · Open Source · Full-featured music library tag management
>
> Version: 1.0.7 ｜ Language: English / [中文](./README.md)

Lnh-Music-Tag is a **self-hosted tag manager and streaming gateway** for your personal music library. It helps you batch-organize metadata (title, artist, album, cover art, lyrics, etc.) of local audio files, and serves the curated library to external players through a built-in **unified streaming gateway** speaking **Subsonic / Jellyfin / Emby / Navidrome** protocols — compatible with dozens of players such as Yingliu (SoundFlow), Archer, Jellyfin, Emby, Navidrome and others.

---

## Disclaimer (Please Read)

> **This software is only a music library management tool. It does NOT contain, bundle, or provide any audio sources.**
>
> - The software only reads and writes metadata (tags, cover art, lyrics, file names, etc.) of **your own** local audio files;
> - The built-in "scraping" feature fetches **metadata only** (tags/cover/lyrics) from public sites. It never downloads, stores, or distributes any audio content;
> - Please ensure you have the legal right to use and share the audio files you manage;
> - This software is **provided for learning and reference purposes only**. The author assumes **no responsibility** for any consequences arising from its use (including but not limited to data loss, file corruption, content compliance issues, or violation of third-party terms of service);
> - By using this software you acknowledge that you have read and agreed to the terms above.

---

## Features

### Library Management
- **Directory scan**: scan local music directories and read audio tags; the track list persists across restarts (SQLite)
- **Tag editing**: edit 30+ fields in the web UI — title, artist, album, album artist, genre, year, track number, lyrics, and more
- **Cover art**: view, replace, remove embedded covers; one-click fill for tracks without artwork
- **Lyrics**: embedded lyrics and sidecar `.lrc` files both supported; one-click online lyric fetch
- **Smart filters**: garbled / traditional-Chinese / no-lyrics / no-cover / unidentified — quickly find tracks that need attention

### Intelligent Organizing
- **Multi-source scraping**: 17 built-in metadata sources (Netease, QQ Music, KuGou, KuWo, Migu, Bilibili, Qishui, Bodian, 5sing, MusicBrainz, iTunes, ListenBrainz, Deezer, Last.fm, Spotify, Discogs, Jamendo) with automatic fallback and dedup
- **One-key pipeline**: encoding fix → traditional-to-simplified → artist-from-filename → full scrape, one click for the whole library
- **Batch completion**: scrape only the fields you check; already-filled tags are skipped to save resources
- **Smart dedup**: identify duplicate files by audio content (SHA-256), keep the best-quality copy per group
- **Audio identification**: untagged files like `track01.mp3` are matched against known tracks by audio fingerprint
- **Encoding fix**: detect and restore GBK/UTF-8 misread Chinese tags
- **Script conversion**: bulk simplified ↔ traditional conversion
- **Filename cleanup**: `song.mp3.flac → song.flac` — probe the real encoding and rename correctly

### Format Conversion
- FFmpeg-based batch transcoding: target format (MP3/FLAC/AAC/OGG/WAV…), bitrate, sample rate
- Outputs are saved beside the originals; **source files are never deleted**
- One-click online FFmpeg install, or point to a local executable

### Unified Streaming Gateway
- One gateway, four protocols: **Subsonic / OpenSubsonic, Jellyfin, Emby, Navidrome**
- Works with official and third-party clients: Yingliu (SoundFlow), Archer, Jellyfin, Emby, Navidrome, and more
- Standard endpoints: indexes, artist/album/song browsing, search, lyrics, playlists, favorites, Now Playing, play counts, direct/transcoded streaming
- **Password required**: a streaming password must be configured before the gateway can be enabled — unauthorized access is blocked

### Engineering & Security
- **Plugin system**: scrape sources, FFmpeg, AcoustID, and the gateway are all toggleable/configurable plugins; third-party plugins can be imported as ZIP with their own config panels
- **Trash can**: deleted/deduped files move to a trash can first — restore or permanently delete anytime
- **Activity log**: scan/scrape/convert operations are fully traced; reviewable and clearable
- **Update checker**: built-in GitHub Release check with one-click upgrade hint
- **Robustness**: bounded-concurrency background jobs (semaphore), panic recovery to a `failed` terminal state, timeouts on all external process calls, batched DB transactions, single-instance startup lock

---

## Quick Start

### Option 1: Docker (Recommended)

```bash
docker run -d \
  --name lnh-music-tag \
  -p 10248:10248 \
  -v /your/music/dir:/music \
  -v lnh_config:/config \
  -e LNH_ADMIN_USER=admin \
  -e LNH_ADMIN_PASS=your_password \
  ghcr.io/lahuolou/lnh-music-tag:latest
```

Or use the bundled `docker-compose.yml`:

```bash
docker compose up -d
```

Open `http://server-ip:10248` in your browser.

### Option 2: Build & Run Locally

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

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `LNH_PORT` | `:10248` | HTTP listen address |
| `LNH_CONFIG_DIR` | `/config` | Config directory (DB, trash can, plugin data) |
| `LNH_ADMIN_USER` | `admin` | Initial admin username |
| `LNH_ADMIN_PASS` | random | Initial admin password (printed to log if empty) |
| `LNH_STREAM_PASS` | empty | Streaming password (legacy compat; prefer `stream_pass` in the plugin page) |
| `LNH_INSTALL_FFMPEG` | `false` | `true/1` installs FFmpeg automatically at container startup (needs root) |

> Credentials and API keys are stored in the config DB (`config/lnh.db`). Env vars are only used as **first-boot** seeds.

---

## Connecting the Streaming Gateway

**Set a streaming password first** (Settings → Plugins → Unified Streaming Gateway).

| Protocol | Base URL (example) | Credentials |
|---|---|---|
| Subsonic / OpenSubsonic | `http://IP:10248/rest` | admin user + streaming password |
| Navidrome-compatible | `http://IP:10248` (`/auth/login`, `/api/*`) | same |
| Jellyfin | `http://IP:10248/jellyfin` (or root) | same |
| Emby | `http://IP:10248/emby` | same |

Pick the matching protocol when adding the server in your client. Full endpoint reference: [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md).

---

## Documentation

| Document | Description |
|---|---|
| [README.md](./README.md) | 中文介绍 |
| [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md) | Development: architecture, modules, build, testing, code style |
| [docs/API.md](./docs/API.md) | Backend API reference |
| [docs/PLUGINS.md](./docs/PLUGINS.md) | Plugin system & third-party plugin development |
| [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md) | Streaming gateway protocols & integration |
| [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) | Deployment & ops (Docker / 1Panel / local) |
| [CHANGELOG.md](./CHANGELOG.md) | Version history |

---

## Having Issues?

- First check [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) and [docs/STREAMING_GATEWAY.md](./docs/STREAMING_GATEWAY.md)
- Report issues on [GitHub Issues](https://github.com/lahuolou/Lnh-Music-Tag/issues). Please include:
  - Version (`cat VERSION`, or the Settings page)
  - Deployment method (Docker / local), OS and architecture
  - Reproduction steps, relevant logs, and screenshots

---

## License

Licensed under the **Apache License 2.0**. See [LICENSE](./LICENSE).

> Reminder: this project is a management tool for learning/reference purposes only. It contains no audio content. Use at your own risk.
