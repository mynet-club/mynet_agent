#!/usr/bin/env bash
# MyNet - OpenWrt 防火墙 MASQ 规则检查脚本
# 用于检查和修复 oifname masquerade 配置

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

# 检查是否在OpenWrt系统上
if [ ! -f /etc/openwrt_release ]; then
    print_error "此脚本需要在 OpenWrt 系统上运行"
    exit 1
fi

print_header "OpenWrt 防火墙 MASQ 配置检查"

# 1. 检查 nft 是否安装（OpenWrt 22+ 使用 nftables）
print_info "检查防火墙系统..."
if command -v nft >/dev/null 2>&1; then
    print_success "使用 nftables (nft)"
    FW_TYPE="nft"
elif command -v iptables >/dev/null 2>&1; then
    print_success "使用 iptables"
    FW_TYPE="iptables"
else
    print_error "未找到防火墙工具"
    exit 1
fi

# 2. 显示当前 NAT/MASQ 规则
print_header "当前 MASQUERADE 规则"
if [ "$FW_TYPE" = "nft" ]; then
    echo "=== nftables NAT 规则 ==="
    nft list table inet fw4 2>/dev/null | grep -A 10 "chain srcnat" || print_warning "未找到 srcnat 链"
    echo ""
    nft list table inet fw4 2>/dev/null | grep -i masquerade || print_warning "未找到 masquerade 规则"
else
    echo "=== iptables NAT 规则 ==="
    iptables -t nat -L POSTROUTING -n -v || print_warning "无法列出 POSTROUTING 规则"
    echo ""
    iptables -t nat -S | grep -i masquerade || print_warning "未找到 masquerade 规则"
fi

# 3. 检查接口状态
print_header "网络接口状态"
if command -v ip >/dev/null 2>&1; then
    echo "=== VPN 接口 (mynet*) ==="
    ip addr show | grep -A 5 "mynet" || print_warning "未找到 mynet 接口"
    echo ""
    echo "=== 路由表 ==="
    ip route show | grep mynet || print_warning "未找到 mynet 相关路由"
else
    print_warning "ip 命令不可用"
fi

# 4. 检查 firewall 配置
print_header "防火墙配置文件"
if [ -f /etc/config/firewall ]; then
    echo "=== /etc/config/firewall 中的 masq 配置 ==="
    grep -A 3 "option masq" /etc/config/firewall || print_warning "未找到 masq 配置"
    echo ""
    echo "=== zone 配置 ==="
    grep -A 5 "config zone" /etc/config/firewall | grep -A 5 "mynet\|lan\|wan" || true
fi

# 5. 检查转发规则
print_header "转发规则检查"
if [ "$FW_TYPE" = "nft" ]; then
    echo "=== nftables 转发链 ==="
    nft list table inet fw4 2>/dev/null | grep -A 10 "chain forward" || print_warning "未找到 forward 链"
else
    echo "=== iptables 转发链 ==="
    iptables -L FORWARD -n -v || print_warning "无法列出 FORWARD 规则"
fi

# 6. 建议修复方案
print_header "诊断建议"
echo ""
print_info "如果 masquerade 未生效，请检查："
echo "  1. 确认 mynet zone 的 masq 选项为 '1'"
echo "  2. 确认 mynet zone 包含正确的网络接口"
echo "  3. 检查是否有其他规则阻止转发"
echo "  4. 尝试重启防火墙: /etc/init.d/firewall restart"
echo ""
print_info "手动添加 masquerade 规则（临时）："
if [ "$FW_TYPE" = "nft" ]; then
    echo "  nft add rule inet fw4 srcnat oifname \"mynet0\" masquerade"
else
    echo "  iptables -t nat -A POSTROUTING -o mynet0 -j MASQUERADE"
fi
echo ""
print_info "查看详细日志："
echo "  logread | grep firewall"
echo "  dmesg | grep -i firewall"

print_header "检查完成"
