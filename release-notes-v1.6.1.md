# LNH-MusicTag v1.6.1 发布说明

**质量与文档轮：交互/渲染冒烟验证通过，开发文档补 Subsonic 章节与版本记录，清理冗余产物**

## 新增与改进

1. **交互与页面渲染检查**
   - 实测（浏览器自动化）登录 → 列表 → 插件页 → 设置页 → 批量页全流程：列表列序（封面/标题/艺术家/专辑/歌词）、插件页「📡 协议插件」分组与 Subsonic 卡片（连接地址 / 认证说明 / 专用密码配置）、设置页 29 元素勾选、批量页一键刮削/字段/17 源选择均渲染正常；
   - 前端重新构建（`npm run build`），`web/dist` 产物更新。

2. **开发文档补全（docs/DEVELOPMENT.md）**
   - 第 9 节故障排查新增 3 条 Subsonic 条目（播放器连不上 / token 认证失败 40 / 中文搜索）；
   - 第 10 节版本记录新增 v1.6.0 完整条目（Subsonic 协议、Key 脱敏、CI tag 触发、随附清理轮），README 功能特性表与 docs/PLUGINS.md 插件开发指南已在 v1.6.0 同步 Subsonic 章节。

3. **清理冗余**
   - 移除 v1.5.3 测试遗留可执行文件（lnh-musictag-v153-test.exe 及备份）与空 cmd/ 目录；
   - 仓库根目录历史调试残留（tmp_pdf_*.png、tmp_probe2.py、tmp_zipinfo.py）已随 v1.6.0 删除。

## 验证

- `go vet ./...`、`go test ./...`、前端 `npm run build` 全部通过；
- 浏览器自动化冒烟：首页/登录/列表/插件页/设置页/批量页渲染与交互正常，无控制台报错。

## 升级方式

1Panel / Docker 拉取新镜像并重建容器：

```bash
docker pull ghcr.io/lahuolou/lnh-musictag:latest
docker compose up -d
```
