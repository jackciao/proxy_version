#!/bin/bash
set -e

# ========== 配置 ==========
REPO_URL="https://github.com/jackciao/proxy_version.git"
INSTALL_DIR="/opt/proxy_version"
DOCKER_IMAGE="jackciao/proxy_version:latest"
USE_PREBUILT=true  # 默认使用预构建镜像
# ==========================

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'

echo -e "${CYAN}
╔═══════════════════════════════════════════════════════╗
║        🚀 Proxy Version 一键安装脚本 (优化版)         ║
╚═══════════════════════════════════════════════════════╝
${NC}"

# 检测参数
while [[ $# -gt 0 ]]; do
    case $1 in
        --build)
            USE_PREBUILT=false
            echo -e "${YELLOW}使用本地构建模式${NC}"
            shift
            ;;
        *)
            shift
            ;;
    esac
done

# 检查 root
[[ $EUID -ne 0 ]] && { echo -e "${RED}请使用 root 用户运行${NC}"; exit 1; }

# 1. 安装 Docker
if ! command -v docker &>/dev/null; then
    echo -e "${CYAN}[1/6] 安装 Docker...${NC}"
    curl -fsSL https://get.docker.com | bash
    systemctl enable --now docker
else
    echo -e "${GREEN}[1/6] Docker 已安装${NC}"
fi

# 2. 安装 Git
if ! command -v git &>/dev/null; then
    echo -e "${CYAN}[2/6] 安装 Git...${NC}"
    apt update && apt install -y git || yum install -y git
else
    echo -e "${GREEN}[2/6] Git 已安装${NC}"
fi

# 3. 克隆项目
echo -e "${CYAN}[3/6] 下载项目...${NC}"
if [ -d "$INSTALL_DIR" ]; then
    cd "$INSTALL_DIR"
    git pull
else
    git clone "$REPO_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# 4. 创建必要目录
echo -e "${CYAN}[4/6] 创建目录...${NC}"
mkdir -p /etc/v2ray-agent/{nodes,tls,sing-box}
mkdir -p /root/.acme.sh
mkdir -p "${INSTALL_DIR}/data"

# 5. 生成密钥
echo -e "${CYAN}[5/6] 配置环境...${NC}"
if [ ! -f "${INSTALL_DIR}/.env" ]; then
    export JWT_SECRET=$(openssl rand -hex 32)
    echo "JWT_SECRET=$JWT_SECRET" > "${INSTALL_DIR}/.env"
    echo -e "${GREEN}已生成 JWT 密钥${NC}"
fi

# 6. 启动服务
echo -e "${CYAN}[6/6] 启动服务...${NC}"
docker compose down 2>/dev/null || true

if [ "$USE_PREBUILT" = true ]; then
    echo -e "${GREEN}使用预构建镜像（快速部署模式）${NC}"
    # 拉取最新镜像
    docker pull $DOCKER_IMAGE
    # 使用预构建镜像启动
    docker compose -f docker-compose.prebuilt.yml up -d
else
    echo -e "${YELLOW}使用本地构建模式（首次部署或开发环境）${NC}"
    # 检测内存大小
    MEM_MB=$(free -m | awk '/^Mem:/{print $2}')
    if [ $MEM_MB -lt 1500 ]; then
        echo -e "${YELLOW}⚠️  检测到低内存环境 (${MEM_MB}MB)${NC}"
        echo -e "${CYAN}正在配置 swap 以加速构建...${NC}"
        
        # 创建 2GB swap（如果不存在）
        if ! swapon --show | grep -q '/swapfile'; then
            fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048
            chmod 600 /swapfile
            mkswap /swapfile
            swapon /swapfile
            echo -e "${GREEN}Swap 已激活${NC}"
        fi
    fi
    
    docker compose up -d --build
fi

# 7. 设置 CLI 命令
echo -e "${CYAN}设置命令行工具...${NC}"
chmod +x "${INSTALL_DIR}/scripts/proxy_version.sh"
ln -sf "${INSTALL_DIR}/scripts/proxy_version.sh" /usr/local/bin/proxy_version

# 完成
PUBLIC_IP=$(curl -s --connect-timeout 3 ip.sb || echo "服务器IP")
echo -e "
${GREEN}╔═══════════════════════════════════════════════════════╗
║              ✅ 安装完成！                             ║
╚═══════════════════════════════════════════════════════╝${NC}

  ${CYAN}访问地址:${NC} http://${PUBLIC_IP}:8080
  ${CYAN}管理命令:${NC} proxy_version

${YELLOW}💡 提示：${NC}
- 默认使用预构建镜像（快速部署，30秒内完成）
- 如需本地构建，运行: curl -fsSL ... | bash -s -- --build
"

# 自动运行 CLI
sleep 2
exec bash -c '/usr/local/bin/proxy_version'
