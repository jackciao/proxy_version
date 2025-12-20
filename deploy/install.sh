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

# 获取真实公网 IP（避免 WARP IP，优先 IPv4）
get_real_public_ip() {
    local public_ipv4=""
    local public_ipv6=""
    
    # 方法 1: 从网络接口读取 IPv4
    if command -v ip &>/dev/null; then
        public_ipv4=$(ip -4 addr show 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | \
            grep -v '^127\.' | \
            grep -v '^10\.' | \
            grep -v '^192\.168\.' | \
            grep -Ev '^172\.(1[6-9]|2[0-9]|3[01])\.' | \
            grep -v '^104\.' | \
            head -n 1)
    fi
    
    # 方法 2: hostname -I 获取 IPv4（回退）
    if [ -z "$public_ipv4" ] && command -v hostname &>/dev/null; then
        public_ipv4=$(hostname -I 2>/dev/null | tr ' ' '\n' | \
            grep -E '^\d+(\.\d+){3}$' | \
            grep -v '^127\.' | \
            grep -v '^10\.' | \
            grep -v '^192\.168\.' | \
            grep -Ev '^172\.(1[6-9]|2[0-9]|3[01])\.' | \
            grep -v '^104\.' | \
            head -n 1)
    fi
    
    # 方法 3: 获取 IPv6（仅当没有 IPv4 时）
    if [ -z "$public_ipv4" ] && command -v ip &>/dev/null; then
        public_ipv6=$(ip -6 addr show 2>/dev/null | grep -oP '(?<=inet6\s)[0-9a-f:]+' | \
            grep -v '^::1' | \
            grep -v '^fe80:' | \
            grep -v '^fd' | \
            grep -v '^fc' | \
            grep -v '^2606:4700:' | \
            head -n 1)
    fi
    
    # 方法 4: 通过主网卡查询外部 API（最后回退）
    if [ -z "$public_ipv4" ] && [ -z "$public_ipv6" ]; then
        local main_iface=$(ip route 2>/dev/null | grep default | awk '{print $5}' | head -n 1)
        if [ -n "$main_iface" ] && [ "$main_iface" != "wg0" ] && [[ ! "$main_iface" =~ ^tun ]] && [[ ! "$main_iface" =~ ^tap ]]; then
            public_ipv4=$(curl -s --interface "$main_iface" --connect-timeout 3 -4 ifconfig.me 2>/dev/null || echo "")
        fi
    fi
    
    # 优先返回 IPv4，否则返回 IPv6
    if [ -n "$public_ipv4" ]; then
        echo "$public_ipv4"
    elif [ -n "$public_ipv6" ]; then
        echo "[$public_ipv6]"  # IPv6 用方括号包裹
    else
        echo "服务器IP"
    fi
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

# 自动运行 CLI（通过 /dev/tty 保持交互）
sleep 2
if [ -t 0 ]; then
    # 直接在终端运行，正常启动
    exec /usr/local/bin/proxy_version
else
    # 通过管道运行（curl | bash），需要重定向到终端
    /usr/local/bin/proxy_version </dev/tty
fi
