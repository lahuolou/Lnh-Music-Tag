#!/bin/sh
# Lnh-Music-Tag 容器入口
# 环境变量 LNH_INSTALL_FFMPEG=true|1|yes|on 时，启动前在容器内安装 FFmpeg
#（apk add --no-cache ffmpeg，供“格式转换”功能使用；需要容器以 root 运行）。
# 关闭或留空则不安装——此时可在「插件 → FFmpeg」填写宿主机路径或点“一键安装”。
set -e

case "$LNH_INSTALL_FFMPEG" in
  true|1|yes|on|TRUE|YES|ON)
    if ! command -v ffmpeg >/dev/null 2>&1; then
      echo "[LNH] LNH_INSTALL_FFMPEG=on: installing ffmpeg ..."
      if ! apk add --no-cache ffmpeg >/dev/null 2>&1; then
        echo "[LNH] ffmpeg install failed (need root). Fall back to Settings -> Plugins -> FFmpeg."
      else
        echo "[LNH] ffmpeg installed: $(ffmpeg -version 2>/dev/null | head -n1)"
      fi
    fi
    ;;
esac

exec /lnh-music-tag "$@"
