# Proxy Version

现代化代理节点一键部署与管理平台

## ✨ 特性

- 🎨 **现代化 Web 界面** - 简洁美观的用户界面
- 🔐 **JWT 用户认证** - 安全的身份验证系统
- 🔍 **智能环境检测** - 自动识别 Nginx/OpenResty/1Panel/宝塔面板
- 📡 **多协议支持** - VLESS Reality/Vision、VMess、Trojan、Hysteria2、TUIC
- 📜 **证书管理** - 集成 acme.sh 自动申请和续签 SSL 证书
- 🌐 **WARP 集成** - 支持 Cloudflare WARP 流量代理
- 🐳 **Docker 部署** - 容器化部署，环境隔离
- ⚡ **快速部署** - 预构建镜像，30 秒内完成部署（1核1G VPS）

## 🚀 一键安装

### 快速部署（推荐）

使用预构建 Docker 镜像，**30 秒内完成部署**，适合低配 VPS（1核512MB+）：

```bash
curl -fsSL https://raw.githubusercontent.com/jackciao/proxy_version/main/deploy/install-optimized.sh | bash
```

### 标准安装

从源码构建（需要 5-15 分钟，适合开发环境）：

```bash
curl -fsSL https://raw.githubusercontent.com/jackciao/proxy_version/main/deploy/install.sh | bash
```

或强制本地构建：

```bash
curl -fsSL https://raw.githubusercontent.com/jackciao/proxy_version/main/deploy/install-optimized.sh | bash -s -- --build
```

## 📋 安装后操作

安装完成后，脚本会自动启动管理命令行工具。你也可以随时运行：

```bash
proxy_version
```

进入管理菜单，选择 **"创建用户"** 来创建管理员账号。

然后访问：`http://服务器IP:8080`

## 🛠️ 手动部署（开发者）

### 克隆仓库

```bash
git clone https://github.com/jackciao/proxy_version.git
cd proxy_version
```

### 使用预构建镜像

```bash
docker compose -f docker-compose.prebuilt.yml up -d
```

### 从源码构建

```bash
docker compose up -d --build
```

### 本地运行（不使用 Docker）

```bash
# 安装依赖
go mod tidy

# 运行
go run main.go
```

## 📦 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PORT` | Web 服务端口 | `8080` |
| `JWT_SECRET` | JWT 密钥（自动生成） | `随机生成` |
| `DATABASE_PATH` | SQLite 数据库路径 | `/app/data/proxy_version.db` |
| `ENVIRONMENT` | 运行环境 | `production` |

## 🔧 常用命令

```bash
# 启动服务
docker compose up -d

# 停止服务
docker compose down

# 查看日志
docker compose logs -f

# 更新到最新版本
cd /opt/proxy_version
git pull
docker compose pull  # 如果使用预构建镜像
docker compose up -d

# 重启服务
docker compose restart

# 进入容器
docker exec -it proxy_version sh
```

## 🔐 安全建议

1. **修改默认密钥**：首次安装后修改 `.env` 中的 `JWT_SECRET`
2. **配置防火墙**：限制 8080 端口仅允许可信 IP 访问
3. **使用 HTTPS**：配置反向代理（Nginx/Caddy）并启用 SSL
4. **定期备份**：备份 `/opt/proxy_version/data` 目录
5. **及时更新**：定期执行 `git pull && docker compose up -d` 获取安全更新

## 📊 性能对比

| VPS 配置 | 标准安装 | 快速部署（预构建） | 改善 |
|----------|---------|-------------------|------|
| 1核1G | 10-15 分钟 | **20-30 秒** | 20-30x ⚡ |
| 2核2G | 5-8 分钟 | **15-25 秒** | 15-25x |
| 4核4G | 3-5 分钟 | **10-20 秒** | 10-20x |

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📄 许可证

MIT License

---

**项目地址**：https://github.com/jackciao/proxy_version  
**问题反馈**：https://github.com/jackciao/proxy_version/issues
