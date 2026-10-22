# 后端 API 参考

本文档列出浏览器端使用的 HTTP API（前缀 `/api`）。流媒体网关协议端点（Subsonic/Jellyfin/Emby/Navidrome）见 [docs/STREAMING_GATEWAY.md](./STREAMING_GATEWAY.md)。

---

## 1. 认证与会话

所有 `/api` 请求（除登录）需要有效会话：

- 登录成功后服务端下发 Cookie 会话
- 所有 `POST` 请求必须携带 CSRF 头：`X-Requested-With: Lnh-Music-Tag`，缺失返回 `403 missing CSRF header`
- 响应均为 JSON；失败形如 `{"error": "..."}`

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/login` | 登录。Body：`{"user":"admin","pass":"..."}` |
| POST | `/api/logout` | 登出 |
| POST | `/api/change-password` | 修改密码。Body：`{"old":"...","new":"..."}` |

---

## 2. 曲目

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/tracks` | 曲目列表（含标签、封面/歌词状态） |
| GET | `/api/tracks/refresh` | 重新扫描库内已注册文件并刷新列表 |
| GET | `/api/track/{id}` | 单曲详情 |
| GET | `/api/tracks/{id}/cover` | 封面图片 |
| GET | `/api/tracks/{id}/lrcfile` | 同目录 `.lrc` 歌词文件 |
| POST | `/api/tracks/{id}/tags` | 写回标签。Body：`{"tags": {"TITLE":"...","ARTIST":"...", ...}}` |
| POST | `/api/tracks/{id}/cover` | 替换封面。Body：`{"cover": "<base64>"}` |
| POST | `/api/tracks/delete` | 删除（先进回收站）。Body：`{"ids":["..."]}`，返回 `{"removed":n,"failed":[...]}` |
| GET | `/api/duplicates` | 重复曲目分组（按音频哈希） |
| POST | `/api/duplicates/remove` | 删除重复组中保留之外的文件（先进回收站）。Body：`{"del":[...]}` |

---

## 3. 扫描

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/scan` | 启动扫描。Body：`{"dir":"/music"}` |
| GET | `/api/scan/status` | 扫描状态（运行中/进度/暂停/错误） |
| POST | `/api/scan/pause` | 暂停/继续。Body：`{"paused":true\|false}` |

---

## 4. 后台任务

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/scrape/batch` | 批量刮削。Body：`{"ids":[...],"source":"auto","fields":[...],"skipFilled":true}`，返回 `{"jobId":"...","total":n}` |
| POST | `/api/scrape/onekey` | 一键刮削流水线。Body：`{"ids":[...],"dedup":false}` |
| GET | `/api/scrape/jobs/{id}` | 批量任务进度（含增量 `updated` 曲目） |
| GET | `/api/jobs/{id}` | 工具任务进度（转换/识别/乱码/简繁） |
| GET | `/api/jobs` | 进行中的任务列表（刷新/换设备后恢复进度） |
| POST | `/api/jobs/{id}/cancel` | 取消任务。Body：`{}` |

任务状态：`running / done / cancelled / failed / interrupted`。`failed` 时 `message` 字段携带后端 panic 详情。

任务响应结构（示例）：

```json
{
  "id": "9f8d...", "total": 12, "done": 12, "current": "",
  "status": "done", "stage": "scrape", "stageLabel": "刮削",
  "results": [{"id":"...","fileName":"...","ok":true,"skip":false,"message":""}],
  "summary": {"ok": 10, "fail": 1, "skip": 1},
  "createdAt": "..."
}
```

---

## 5. 刮削辅助

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/scrape/sources` | 可用刮削源列表（含启停状态） |
| GET | `/api/scrape/search` | 按关键词搜索（`q` 参数），返回候选 |
| GET | `/api/lyrics` | 获取歌词（`song`/`artist` 参数） |
| POST | `/api/tracks/{id}/scrape` | 单曲刮削。Body：`{"source":"auto","fields":[...]}` |

---

## 6. 格式转换与工具

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/convert` | 批量转码。Body：`{"ids":[...],"target":"mp3","inputFormats":["flac"],"bitrate":320,"sampleRate":44100,"removeSource":false}` |
| GET | `/api/ffmpeg/status` | FFmpeg 探测状态 |
| POST | `/api/ffmpeg/install` | 一键安装 FFmpeg |
| POST | `/api/ffmpeg/uninstall` | 卸载 FFmpeg |
| POST | `/api/fix-encoding` | 乱码标签修复。Body：`{"ids":[...]}` |
| POST | `/api/convert-script` | 简繁转换。Body：`{"ids":[...],"to":"simp"\|"trad"}` |
| POST | `/api/rename-clean` | 修正文件后缀。Body：`{"ids":[...]}`（空=全部） |
| POST | `/api/identify` | 音频内容识别。Body：`{"ids":[...]}` |

---

## 7. 回收站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/trash` | 回收站条目（`name`/`originalPath`/`size`/`modTime`） |
| POST | `/api/trash/restore` | 恢复。Body：`{"name":"file.wav"}`，返回恢复后的绝对路径 |
| POST | `/api/trash/delete` | 永久删除。Body：`{"name":"file.wav"}` |
| POST | `/api/trash/clear` | 清空回收站。Body：`{}` |

> 回收站条目的 `name` 校验防路径穿越；恢复时原目录不存在会明确报错。

---

## 8. 插件

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/plugins` | 插件列表（类型/启停/配置/可用性 `available` + `unavailable` 原因） |
| POST | `/api/plugins` | 批量更新。Body：`{"scrape.qq":{"enabled":true},"tool.ffmpeg":{"config":{"path":"..."}}}` |
| POST | `/api/plugins/import` | 导入第三方插件 ZIP |
| DELETE | `/api/plugins/{name}` | 删除第三方插件（内置插件禁止删除） |
| POST | `/api/plugins/{name}/run` | 运行插件入口 |
| GET | `/api/plugins/{name}/panel` | 插件配置面板 HTML |
| GET | `/api/plugins/{name}/panel/{path...}` | 面板静态资源 |

插件更新门控示例：

- `tool.ffmpeg`：未探测到二进制 → 拒绝开启
- `tool.acoustid`：未配置 Key → 拒绝开启
- `tool.streaming_gateway`：未配置流媒体密码 → 拒绝开启（返回 400 并提示先设置密码）

---

## 9. 设置与系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/settings` | 设置项（管理员信息、音乐目录、开关） |
| POST | `/api/settings` | 更新设置 |
| GET | `/api/version/check` | GitHub Release 更新检查 |
| GET | `/api/metrics` | 轻量文本指标（需登录）：曲目总数、后台任务活跃/容量、封面缓存条目、FFmpeg 可用性 |
| GET | `/api/logs` | 操作日志列表 |
| POST | `/api/logs/delete` | 删除指定日志。Body：`{"ids":[...]}` |
| POST | `/api/logs/clear` | 清空日志。Body：`{}` |

---

## 10. 通用约定

- 所有时间字段为本地时间字符串（`2006-01-02 15:04:05`）
- 封面/歌词相关的二进制资源直接以图片/文本返回
- 错误响应：HTTP 状态码 + `{"error":"..."}`；CSRF 缺失返回 `403 missing CSRF header`
- 批量操作接口均返回任务 `jobId`，进度通过任务轮询获取，避免同步等待大任务
