# ---- 前端构建：Vue3 + Vite，产出静态产物 dist ----
FROM node:20-alpine AS fe
WORKDIR /fe
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Go 构建阶段 ----
# go-taglib 是 WASM 封装，无 CGo，可产出纯静态二进制
FROM golang:1.27-alpine AS build
WORKDIR /src

# 先复制依赖清单以利用缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 把前端静态产物拷入 web/dist，供 go:embed 编译进二进制
COPY --from=fe /fe/dist ./web/dist
# CGO_ENABLED=0 保证静态链接；-s -w 精简符号表
RUN apk add --no-cache ca-certificates \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/lnh-music-tag .

# ---- 运行阶段：alpine（FFmpeg 改为可选插件，见下方说明） ----
FROM alpine:3.20
# 默认仅装运行必需：ca-certificates（HTTPS 刮削源）+ chromaprint（fpcalc，
# 供“音频内容识别”）。
# FFmpeg 已从镜像移除，改为可选，三种启用方式（任选其一）：
#   ① compose 里设 LNH_INSTALL_FFMPEG=true，容器启动时自动安装（见 entrypoint.sh）；
#   ② 在「插件 → FFmpeg」点“一键安装”（容器内 apk add，需 root 用户）；
#   ③ 把宿主机 /usr/bin 挂载进容器，并在插件页填写宿主机 ffmpeg 绝对路径。
# 如希望构建时内置 ffmpeg，把下面这行取消注释后自行构建：
# RUN apk add --no-cache ffmpeg
RUN apk add --no-cache ca-certificates chromaprint
COPY --from=build /out/lnh-music-tag /lnh-music-tag
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 10248
# 音乐目录挂载点（宿主目录映射到此，需读写，应用会把标签/封面/歌词写回文件）
VOLUME ["/music"]
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/lnh-music-tag"]
