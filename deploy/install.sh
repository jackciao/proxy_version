#!/bin/bash
set -e

# ========== 配置 ==========
REPO_URL="https://github.com/jackciao/proxy_version.git"
INSTALL_DIR="/opt/proxy_version"
# ==========================

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'

echo -e "${CYAN}
╔═══════════════════════════════════════════════════════╗
║        🚀 Proxy Version 一键安装脚本                  ║
╚═══════════════════════════════════════════════════════╝
${NC}"

# 检查 root
[[ $EUID -ne 0 ]] && { echo -e "${RED}请使用 root 用户运行${NC}"; exit 1; }

# 1. 安装 Docker
if ! command -v docker &>/dev/null; then
    echo -e "${CYAN}[1/5] 安装 Docker...${NC}"
    curl -fsSL https://get.docker.com | bash
    systemctl enable --now docker
else
    echo -e "${GREEN}[1/5] Docker 已安装${NC}"
fi

# 2. 安装 Git
if ! command -v git &>/dev/null; then
    echo -e "${CYAN}[2/5] 安装 Git...${NC}"
    apt update && apt install -y git || yum install -y git
else
    echo -e "${GREEN}[2/5] Git 已安装${NC}"
fi

# 3. 克隆项目
echo -e "${CYAN}[3/5] 下载项目...${NC}"
if [ -d "$INSTALL_DIR" ]; then
    cd "$INSTALL_DIR"
    git pull
else
    git clone "$REPO_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# 4. 创建必要目录
echo -e "${CYAN}[4/5] 创建目录...${NC}"
mkdir -p /etc/v2ray-agent/{nodes,tls,sing-box}
mkdir -p /root/.acme.sh
mkdir -p "${INSTALL_DIR}/data"

# 5. 生成密钥并启动
echo -e "${CYAN}[5/5] 启动服务...${NC}"
export JWT_SECRET=$(openssl rand -hex 32)
docker compose down 2>/dev/null || true
docker compose up -d --build

# 完成
PUBLIC_IP=$(curl -s --connect-timeout 3 ip.sb || echo "服务器IP")
echo -e "
${GREEN}╔═══════════════════════════════════════════════════════╗
║              ✅ 安装完成！                             ║
╚═══════════════════════════════════════════════════════╝${NC}

  ${CYAN}访问地址:${NC} http://${PUBLIC_IP}:8080
  ${CYAN}管理命令:${NC} cd ${INSTALL_DIR} && docker compose logs -f
"
