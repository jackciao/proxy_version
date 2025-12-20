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

# Install acme.sh without email (account will be registered on first use with user's email)
RUN curl https://get.acme.sh | sh && \
    /root/.acme.sh/acme.sh --set-default-ca --server letsencrypt

RUN mkdir -p /app/data /etc/v2ray-agent/tls

COPY --from=builder /app/proxy_version .
COPY --from=builder /app/web ./web

ENV TZ=Asia/Shanghai
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/ || exit 1

CMD ["./proxy_version"]
