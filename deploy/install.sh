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

# 6. 设置 CLI 命令
echo -e "${CYAN}[6/6] 设置命令行工具...${NC}"
chmod +x "${INSTALL_DIR}/scripts/proxy_version.sh"
ln -sf "${INSTALL_DIR}/scripts/proxy_version.sh" /usr/local/bin/proxy_version

# 获取真实公网 IP（避免 WARP IP）
get_real_public_ip() {
    local public_ip=""
    
    # 方法 1: 从网络接口直接读取
    if command -v ip &>/dev/null; then
        # 使用 ip 命令（Linux）
        public_ip=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | \
            grep -v '^127\.' | \
            grep -v '^10\.' | \
            grep -v '^192\.168\.' | \
            grep -Ev '^172\.(1[6-9]|2[0-9]|3[01])\.' | \
            grep -v '^104\.' | \
            head -n 1)
    fi
    
    # 方法 2: 使用 hostname -I
    if [ -z "$public_ip" ] && command -v hostname &>/dev/null; then
        public_ip=$(hostname -I 2>/dev/null | tr ' ' '\n' | \
            grep -v '^127\.' | \
            grep -v '^10\.' | \
            grep -v '^192\.168\.' | \
            grep -Ev '^172\.(1[6-9]|2[0-9]|3[01])\.' | \
            grep -v '^104\.' | \
            grep -v '^$' | \
            head -n 1)
    fi
    
    # 方法 3: 回退到外部 API（通过主网卡）
    if [ -z "$public_ip" ]; then
        # 尝试通过主网卡接口查询
        local main_iface=$(ip route 2>/dev/null | grep default | awk '{print $5}' | head -n 1)
        if [ -n "$main_iface" ] && [ "$main_iface" != "wg0" ] && [[ ! "$main_iface" =~ ^tun ]] && [[ ! "$main_iface" =~ ^tap ]]; then
            public_ip=$(curl -s --interface "$main_iface" --connect-timeout 3 ifconfig.me 2>/dev/null || echo "")
        fi
    fi
    
    # 最后的回退
    if [ -z "$public_ip" ]; then
        public_ip="服务器IP"
    fi
    
    echo "$public_ip"
}

# 完成
PUBLIC_IP=$(get_real_public_ip)
echo -e "
${GREEN}╔═══════════════════════════════════════════════════════╗
║              ✅ 安装完成！                             ║
╚═══════════════════════════════════════════════════════╝${NC}

  ${CYAN}访问地址:${NC} http://${PUBLIC_IP}:8080
  ${CYAN}管理命令:${NC} proxy_version
"

# 自动运行 CLI（使用 exec 启动新的交互式 shell 以避免 stdin 被管道占用）
sleep 2
exec bash -c '/usr/local/bin/proxy_version'
