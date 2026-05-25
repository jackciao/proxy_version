# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
RUN apk add --no-cache gcc musl-dev sqlite-dev
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=1 GOOS=linux go build -a -ldflags '-linkmode external -extldflags "-static"' -o proxy_version .

# Runtime stage
FROM alpine:3.19
WORKDIR /app

# Install dependencies
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    bash \
    openssl \
    docker-cli \
    socat \
    coreutils \
    net-tools \
    wireguard-tools \
    util-linux \
    iproute2

# Create symlink for bash compatibility (acme.sh uses #!/usr/bin/bash)
RUN ln -sf /bin/bash /usr/bin/bash

# Install GNU tar for arm64 compatibility (BusyBox tar fails under QEMU emulation)
RUN apk add --no-cache tar && \
    ln -sf /usr/bin/tar /usr/bin/gtar

# Install acme.sh without email (account will be registered on first use with user's email)
RUN curl https://get.acme.sh | sh && \
    /root/.acme.sh/acme.sh --set-default-ca --server letsencrypt

# Pre-create all volume mount points (Docker can't create them on read-only rootfs layers)
RUN mkdir -p /app/data /etc/v2ray-agent/tls /etc/v2ray-agent/nodes /etc/v2ray-agent/sing-box \
    /etc/nginx /etc/systemd/system /opt/1panel/apps/openresty /www/server /host/proc

COPY --from=builder /app/proxy_version .
COPY --from=builder /app/web ./web
COPY --from=builder /app/image.png ./image.png

ENV TZ=Asia/Shanghai
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/ || exit 1

CMD ["./proxy_version"]
