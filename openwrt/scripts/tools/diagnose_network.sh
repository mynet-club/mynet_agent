#!/usr/bin/env bash
# MyNet - 网络连接诊断脚本
# 用于诊断节点间的连接问题和防火墙masq配置

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

# 检查参数
if [ $# -lt 1 ]; then
    echo "用法: $0 <target_host> [target_port]"
    echo "示例: $0 192.168.0.2"
    echo "示例: $0 192.168.0.2 22"
    exit 1
fi

TARGET_HOST="$1"
TARGET_PORT="${2:-}"

print_header "网络连接诊断 - $TARGET_HOST"

# 1. 基本连通性测试
print_info "测试 ICMP 连通性..."
if ping -c 3 -W 2 "$TARGET_HOST" >/dev/null 2>&1; then
    print_success "ICMP ping 成功"
else
    print_error "ICMP ping 失败"
fi

# 2. 如果指定了端口，测试TCP连接
if [ -n "$TARGET_PORT" ]; then
    print_info "测试 TCP 端口 $TARGET_PORT..."
    if command -v nc >/dev/null 2>&1; then
        if nc -zv -w 3 "$TARGET_HOST" "$TARGET_PORT" 2>&1 | grep -q succeeded; then
            print_success "TCP 端口 $TARGET_PORT 可达"
        else
            print_error "TCP 端口 $TARGET_PORT 不可达"
        fi
    else
        print_warning "nc 命令不可用，跳过端口测试"
    fi
fi

# 3. 路由追踪
print_info "路由追踪..."
if command -v traceroute >/dev/null 2>&1; then
    traceroute -m 10 -w 2 "$TARGET_HOST" 2>&1 | head -n 15
elif command -v tracepath >/dev/null 2>&1; then
    tracepath "$TARGET_HOST" 2>&1 | head -n 15
else
    print_warning "traceroute/tracepath 命令不可用"
fi

echo ""
print_header "完成"
