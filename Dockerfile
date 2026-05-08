# 提取 ca-certificates 和时区数据
FROM debian:bookworm-slim AS base-files

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata && \
    rm -rf /var/lib/apt/lists/*

# 最终镜像
# distroless/cc-debian12:
#   - 包含 glibc、libstdc++、libgcc（node 二进制运行必需）
#   - 无 shell / 无包管理器
FROM gcr.io/distroless/cc-debian12

ARG TARGETARCH

WORKDIR /app

# 复制证书和时区
COPY --from=base-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=base-files /usr/share/zoneinfo/Asia/Shanghai   /usr/share/zoneinfo/Asia/Shanghai
COPY --from=base-files /usr/share/zoneinfo/Asia/Shanghai   /etc/localtime

ENV TZ=Asia/Shanghai
ENV RUNNING_IN_DOCKER=true

LABEL org.opencontainers.image.description="高性能[测活、测速、媒体检测]代理检测筛选工具，支持100-1000高并发低占用运行，大幅减少数倍检测时间。"
LABEL org.opencontainers.image.source="https://github.com/sinspired/subs-check-pro"

# TARGETARCH 由 buildx 自动注入: amd64 / arm64 / arm
COPY bin/subs-check-pro-linux-${TARGETARCH} /app/subs-check-pro

CMD ["/app/subs-check-pro"]
EXPOSE 8199
EXPOSE 8299