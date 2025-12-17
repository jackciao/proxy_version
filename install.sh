#!/bin/bash
set -e
RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; PURPLE='\033[0;35m'; NC='\033[0m'
INSTALL_DIR="/opt/proxy_version"

echo -e "${PURPLE}\n╔═══════════════════════════════════════════════════════╗\n║            🌐 Proxy Version 安装程序                  ║\n╚═══════════════════════════════════════════════════════╝${NC}\n"

[[ $EUID -ne 0 ]] && { echo -e "${RED}请使用 root 用户运行${NC}"; exit 1; }
! command -v docker &>/dev/null && { echo -e "${RED}Docker 未安装${NC}"; exit 1; }

echo -e "${CYAN}[1/4] 创建目录...${NC}"
mkdir -p "${INSTALL_DIR}/data"

echo -e "${CYAN}[2/4] 设置 CLI...${NC}"
chmod +x "${INSTALL_DIR}/scripts/proxy_version.sh"
ln -sf "${INSTALL_DIR}/scripts/proxy_version.sh" /usr/local/bin/proxy_version
chmod +x /usr/local/bin/proxy_version

echo -e "${CYAN}[3/4] 生成密钥...${NC}"
export JWT_SECRET=$(openssl rand -hex 32)

echo -e "${CYAN}[4/4] 启动容器...${NC}"
cd "${INSTALL_DIR}"
docker-compose up -d --build

echo -e "\n${GREEN}╔═══════════════════════════════════════════════════════╗\n║              ✓ 安装完成！                             ║\n╚═══════════════════════════════════════════════════════╝${NC}"
echo -e "\n  ${CYAN}Web 地址:${NC} http://服务器IP:8080"
echo -e "  ${CYAN}下一步:${NC} 运行 proxy_version 创建用户\n"

sleep 2
/usr/local/bin/proxy_version
