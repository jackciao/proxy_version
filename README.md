# Proxy Version

现代化代理节点一键部署与管理平台

## 特性

- 🎨 现代化 Web 界面
- 🔐 JWT 用户认证
- 🔍 智能检测 Nginx/OpenResty/1Panel/aapanel
- 📡 支持 VLESS/VMess/Trojan/Hysteria2/TUIC
- 📜 acme.sh 证书管理
- 🐳 Docker 一键部署
  

## 一键脚本安装

‘’‘bash
curl -fsSL https://raw.githubusercontent.com/jackciao/proxy_version/main/deploy/install.sh | bash
、、、

这个命令会自动：

安装 Docker（如果没有）
安装 Git（如果没有）
克隆您的项目
创建必要目录
构建并启动容器

## 快速开始

```bash
cd /opt/proxy_version
bash install.sh
```

安装完成后输入 `proxy_version` 进入管理菜单创建用户。

访问: `http://服务器IP:8080`

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| PORT | 服务端口 | 8080 |
| JWT_SECRET | JWT 密钥 | 自动生成 |
| DATABASE_PATH | 数据库路径 | ./data/proxy_version.db |

## 安全建议

1. 修改默认 JWT_SECRET
2. 配置 HTTPS
3. 限制 8080 端口访问

## 许可证

MIT License
