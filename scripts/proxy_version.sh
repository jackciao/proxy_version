#!/bin/bash
set -e
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; PURPLE='\033[0;35m'; NC='\033[0m'
INSTALL_DIR="/opt/proxy_version"; DATA_DIR="${INSTALL_DIR}/data"; DB_PATH="${DATA_DIR}/proxy_version.db"
API_URL="http://127.0.0.1:8080/api"

print_banner() { echo -e "${PURPLE}\n╔═══════════════════════════════════════════════════════╗\n║            🌐 Proxy Version 管理工具                  ║\n╚═══════════════════════════════════════════════════════╝${NC}"; }

check_container() { docker ps --format '{{.Names}}' | grep -q "^proxy_version$"; }

ensure_sqlite3() { command -v sqlite3 &>/dev/null || { apt update && apt install -y sqlite3 2>/dev/null || yum install -y sqlite 2>/dev/null; }; }

create_user() {
    echo -e "\n${CYAN}━━━ 创建用户 ━━━${NC}\n"
    
    # 检查容器是否运行
    if ! check_container; then
        echo -e "${RED}容器未运行，请先启动容器${NC}"
        return 1
    fi
    
    # 用户名
    while true; do
        read -p "用户名 (3-32字符): " username
        [[ -z "$username" ]] && { echo -e "${RED}用户名不能为空${NC}"; continue; }
        [[ ${#username} -lt 3 || ${#username} -gt 32 ]] && { echo -e "${RED}长度必须3-32字符${NC}"; continue; }
        break
    done
    
    # 密码
    while true; do
        read -sp "密码 (至少6位): " password
        echo
        [[ -z "$password" ]] && { echo -e "${RED}密码不能为空${NC}"; continue; }
        [[ ${#password} -lt 6 ]] && { echo -e "${RED}密码至少6位${NC}"; continue; }
        read -sp "确认密码: " password2
        echo
        [[ "$password" != "$password2" ]] && { echo -e "${RED}密码不一致${NC}"; continue; }
        break
    done
    
    echo -e "${YELLOW}正在创建用户...${NC}"
    
    # 调用 API 创建用户
    response=$(curl -s -X POST "${API_URL}/auth/register" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"${username}\",\"password\":\"${password}\",\"email\":\"\"}" 2>/dev/null)
    
    if echo "$response" | grep -q "user_id\|created"; then
        echo -e "\n${GREEN}✓ 用户 '$username' 创建成功！${NC}"
        echo -e "\n请访问 ${CYAN}http://服务器IP:8080${NC} 登录\n"
    elif echo "$response" | grep -q "already exists\|已存在"; then
        echo -e "\n${RED}✗ 用户名已存在${NC}\n"
    else
        echo -e "\n${RED}✗ 创建失败: $response${NC}\n"
    fi
}

list_users() {
    ensure_sqlite3
    [[ ! -f "$DB_PATH" ]] && { echo -e "${YELLOW}暂无用户${NC}"; return; }
    echo -e "\n${CYAN}━━━ 用户列表 ━━━${NC}\n"
    printf "${YELLOW}%-4s %-20s %-20s${NC}\n" "ID" "用户名" "创建时间"
    echo "────────────────────────────────────────────"
    sqlite3 "$DB_PATH" "SELECT id, username, datetime(created_at,'localtime') FROM users ORDER BY id;" 2>/dev/null | while IFS='|' read id uname ctime; do
        printf "%-4s %-20s %-20s\n" "$id" "$uname" "$ctime"
    done
    echo ""
}

manage_users() {
    ensure_sqlite3
    list_users
    echo -e "${YELLOW}1.${NC} 删除用户  ${YELLOW}0.${NC} 返回"
    read -p "选择: " choice
    
    case $choice in
        1)
            read -p "用户ID: " uid
            u=$(sqlite3 "$DB_PATH" "SELECT username FROM users WHERE id=$uid;" 2>/dev/null)
            [[ -z "$u" ]] && { echo -e "${RED}用户不存在${NC}"; return; }
            read -p "确定删除 '$u'? (y/N): " cf
            if [[ "$cf" =~ ^[Yy]$ ]]; then
                sqlite3 "$DB_PATH" "DELETE FROM users WHERE id=$uid;"
                echo -e "${GREEN}✓ 用户 '$u' 已删除${NC}"
            fi
            ;;
    esac
}

reinstall() {
    echo -e "\n${YELLOW}⚠️ 将停止并重新安装容器${NC}"
    read -p "继续? (y/N): " cf
    if [[ "$cf" =~ ^[Yy]$ ]]; then
        cd "$INSTALL_DIR"
        docker-compose down 2>/dev/null || docker compose down 2>/dev/null || true
        docker compose up -d --build
        echo -e "\n${GREEN}✓ 重新安装完成！${NC}\n"
    fi
}

update_system() {
    echo -e "\n${CYAN}🔄 更新系统${NC}"
    echo -e "${YELLOW}将拉取最新代码、删除旧镜像并重建容器${NC}"
    read -p "确定要更新到最新版本? (y/N): " cf
    if [[ "$cf" =~ ^[Yy]$ ]]; then
        cd "$INSTALL_DIR"
        
        # 拉取最新代码
        echo -e "\n${YELLOW}[1/4] 拉取最新代码...${NC}"
        git pull
        
        # 停止现有容器
        echo -e "${YELLOW}[2/4] 停止现有容器...${NC}"
        docker-compose down 2>/dev/null || docker compose down 2>/dev/null || true
        
        # 删除旧镜像
        echo -e "${YELLOW}[3/4] 删除旧镜像...${NC}"
        docker rmi proxy_version-proxy_version 2>/dev/null || true
        docker rmi proxy_version_proxy_version 2>/dev/null || true
        
        # 重新构建并启动
        echo -e "${YELLOW}[4/4] 构建新镜像并启动...${NC}"
        docker compose up -d --build
        
        echo -e "\n${GREEN}✓ 更新完成！${NC}\n"
    fi
}

uninstall() {
    echo -e "\n${RED}⚠️ 卸载 Proxy Version${NC}"
    echo -e "${YELLOW}将删除容器、镜像和所有相关数据${NC}"
    read -p "确定要完全卸载? (y/N): " cf
    if [[ "$cf" =~ ^[Yy]$ ]]; then
        cd "$INSTALL_DIR"
        
        # 停止并删除容器
        echo -e "\n${YELLOW}[1/4] 停止并删除容器...${NC}"
        docker-compose down 2>/dev/null || docker compose down 2>/dev/null || true
        
        # 删除 Docker 镜像
        echo -e "${YELLOW}[2/4] 删除 Docker 镜像...${NC}"
        docker rmi proxy_version-proxy_version 2>/dev/null || true
        docker rmi proxy_version_proxy_version 2>/dev/null || true
        
        # 询问是否删除数据
        read -p "删除所有数据（节点配置、证书、数据库等）? (y/N): " dd
        if [[ "$dd" =~ ^[Yy]$ ]]; then
            echo -e "${YELLOW}[3/4] 删除数据目录...${NC}"
            rm -rf "$DATA_DIR"
            rm -rf /etc/v2ray-agent
            echo -e "${GREEN}✓ 数据已删除${NC}"
        else
            echo -e "${YELLOW}[3/4] 跳过删除数据${NC}"
        fi
        
        # 删除 CLI 符号链接
        echo -e "${YELLOW}[4/4] 删除命令行工具...${NC}"
        rm -f /usr/local/bin/proxy_version
        
        echo -e "\n${GREEN}✓ 卸载完成！${NC}"
        echo -e "${YELLOW}如需完全清理项目文件，可手动删除: $INSTALL_DIR${NC}\n"
        exit 0
    fi
}

show_status() {
    echo -e "\n${CYAN}━━━ 状态 ━━━${NC}"
    check_container && echo -e "容器: ${GREEN}● 运行中${NC}" || echo -e "容器: ${RED}● 未运行${NC}"
    if [[ -f "$DB_PATH" ]]; then
        count=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM users;" 2>/dev/null || echo "0")
        echo -e "用户数: $count"
    else
        echo -e "用户数: 0"
    fi
    echo ""
}

main_menu() {
    while true; do
        print_banner
        show_status
        echo -e "${CYAN}━━━ 菜单 ━━━${NC}"
        echo -e "  ${YELLOW}1.${NC} 创建用户"
        echo -e "  ${YELLOW}2.${NC} 管理用户"
        echo -e "  ${YELLOW}3.${NC} 重新安装"
        echo -e "  ${YELLOW}4.${NC} 更新系统"
        echo -e "  ${YELLOW}5.${NC} 卸载"
        echo -e "  ${YELLOW}0.${NC} 退出\n"
        read -p "选择 [0-5]: " choice
        case $choice in
            1) create_user ;;
            2) manage_users ;;
            3) reinstall ;;
            4) update_system ;;
            5) uninstall ;;
            0) echo -e "\n${GREEN}再见！${NC}\n"; exit 0 ;;
            *) echo -e "${RED}无效选择${NC}" ;;
        esac
        read -p "按回车继续..."
    done
}

main_menu
