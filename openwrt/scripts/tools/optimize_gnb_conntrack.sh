#!/usr/bin/env bash
# MyNet - GNB VPN Conntrack 优化脚本
# 用于优化 OpenWrt 路由器上 GNB VPN 的连接追踪策略
#
# 核心策略:
#   1. VPN 隧道流量 (UDP) → 绕过 conntrack (NOTRACK)
#   2. 应用层 TCP 连接 → 优化超时参数
#   3. 应用层 keepalive → 不依赖内核超时
#
# 使用场景:
#   - OpenWrt 路由器运行 GNB VPN 客户端
#   - NAT 后的设备通过 VPN 访问外部服务
#   - 需要解决连接状态冲突导致的访问超时
#
# 参考: WireGuard PersistentKeepalive 机制

set -euo pipefail

# ============================================
# 颜色定义
# ============================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# ============================================
# 工具函数
# ============================================

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

# ============================================
# 检测设备角色
# ============================================

detect_device_role() {
    # 检测是否为路由器（有 NAT 功能）
    # 检查是否有 iptables nat 表或 nftables NAT 规则

    local has_nat=0

    # 检查 iptables nat 表
    if command -v iptables >/dev/null 2>&1; then
        if iptables -t nat -L -n 2>/dev/null | grep -q "MASQUERADE\|SNAT"; then
            has_nat=1
        fi
    fi

    # 检查 nftables NAT
    if command -v nft >/dev/null 2>&1; then
        if nft list tables 2>/dev/null | grep -q "nat"; then
            if nft list table inet fw4 2>/dev/null | grep -qE "masquerade|snat"; then
                has_nat=1
            fi
        fi
    fi

    if [ $has_nat -eq 1 ]; then
        echo "router"  # 路由器（主路由/旁路由）
    else
        echo "server"  # 服务器（代理服务器）
    fi
}

# ============================================
# 检测 GNB 端口
# ============================================

detect_gnb_ports() {
    local ports=$(netstat -ulnp 2>/dev/null | grep gnb | awk '{print $4}' | awk -F: '{print $NF}' | sort -u)
    if [ -z "$ports" ]; then
        print_warning "未检测到 GNB 进程，使用默认端口 9001"
        echo "9001"
    else
        echo "$ports"
    fi
}

# ============================================
# 添加 NOTRACK 规则
# ============================================

add_notrack_rules() {
    local port=$1

    print_info "为 UDP 端口 $port 添加 NOTRACK 规则"

    # 检查 nftables 是否可用
    if ! command -v nft >/dev/null 2>&1; then
        print_error "未找到 nft 命令，请确认 OpenWrt 版本 >= 23.05"
        return 1
    fi

    # 检查 raw table 是否存在
    if ! nft list tables 2>/dev/null | grep -q "inet fw4"; then
        print_error "未找到 inet fw4 表，请检查防火墙配置"
        return 1
    fi

    # 入站规则
    if ! nft list chain inet fw4 raw_prerouting 2>/dev/null | grep -q "udp.*$port.*notrack"; then
        nft add rule inet fw4 raw_prerouting udp dport $port notrack
        nft add rule inet fw4 raw_prerouting udp sport $port notrack
        print_success "已添加入站 NOTRACK 规则"
    else
        print_info "入站 NOTRACK 规则已存在"
    fi

    # 出站规则
    if ! nft list chain inet fw4 raw_output 2>/dev/null | grep -q "udp.*$port.*notrack"; then
        nft add rule inet fw4 raw_output udp dport $port notrack
        nft add rule inet fw4 raw_output udp sport $port notrack
        print_success "已添加出站 NOTRACK 规则"
    else
        print_info "出站 NOTRACK 规则已存在"
    fi
}

# ============================================
# 优化 UDP Conntrack 超时
# ============================================

optimize_udp_timeouts() {
    print_info "优化 UDP conntrack 超时（短超时 + GNB 应用层 keepalive）"

    # UDP 普通超时: 15 秒（GNB 有 keepalive，conntrack 只需短期记忆）
    sysctl -w net.netfilter.nf_conntrack_udp_timeout=15 >/dev/null
    print_success "UDP 超时: 15s"

    # UDP stream 超时: 60 秒
    sysctl -w net.netfilter.nf_conntrack_udp_timeout_stream=60 >/dev/null
    print_success "UDP stream 超时: 60s"
}

# ============================================
# 优化 TCP Conntrack 超时（仅限路由器）
# ============================================

optimize_tcp_timeouts() {
    local role=$1

    if [ "$role" = "server" ]; then
        print_info "检测到代理服务器角色，跳过 TCP conntrack 优化"
        print_warning "代理服务器应监控应用层连接数（netstat），而非 conntrack"
        return 0
    fi

    print_info "优化应用层 TCP 连接的 conntrack 超时（仅针对 NAT 后流量）"
    # ESTABLISHED: 缩短到 1 小时（避免旧状态干扰新连接）
    sysctl -w net.netfilter.nf_conntrack_tcp_timeout_established=3600 >/dev/null
    print_success "TCP ESTABLISHED: 3600s (1小时)"

    # TIME_WAIT: 缩短到 30 秒（快速回收）
    sysctl -w net.netfilter.nf_conntrack_tcp_timeout_time_wait=30 >/dev/null
    print_success "TCP TIME_WAIT: 30s"

    # CLOSE_WAIT: 缩短到 30 秒
    sysctl -w net.netfilter.nf_conntrack_tcp_timeout_close_wait=30 >/dev/null
    print_success "TCP CLOSE_WAIT: 30s"

    # 启用 TIME_WAIT 重用
    if [ -e /proc/sys/net/ipv4/tcp_tw_reuse ]; then
        sysctl -w net.ipv4.tcp_tw_reuse=1 >/dev/null
        print_success "TCP TIME_WAIT 重用已启用"
    fi
}

# ============================================
# 保存配置到 sysctl.conf
# ============================================

save_to_sysctl() {
    local role=$1

    if grep -q "GNB VPN Conntrack" /etc/sysctl.conf 2>/dev/null; then
        print_info "配置已存在于 /etc/sysctl.conf"
        return
    fi

    cat >> /etc/sysctl.conf << EOF

# ============================================
# GNB VPN Conntrack 优化
# ============================================
# 角色: $role
# 策略:
#   1. VPN 隧道 (UDP) → NOTRACK 绕过
#   2. UDP conntrack 短超时（15~60秒）
#   3. 应用层 keepalive → 不依赖内核
#
# 注意: NOTRACK 规则在防火墙重启后会丢失，
#       需要配合 /etc/firewall.user 持久化
# ============================================

# UDP 连接超时优化（所有角色）
net.netfilter.nf_conntrack_udp_timeout=15
net.netfilter.nf_conntrack_udp_timeout_stream=60
EOF

    # 路由器角色需要 TCP 优化
    if [ "$role" = "router" ]; then
        cat >> /etc/sysctl.conf << 'EOF'

# TCP 连接超时优化（仅路由器角色，用于 NAT 后应用流量）
net.netfilter.nf_conntrack_tcp_timeout_established=3600
net.netfilter.nf_conntrack_tcp_timeout_time_wait=30
net.netfilter.nf_conntrack_tcp_timeout_close_wait=30
net.ipv4.tcp_tw_reuse=1
EOF
    else
        cat >> /etc/sysctl.conf << 'EOF'

# TCP 连接超时（服务器角色，使用默认值）
# 代理服务器不做 NAT，应监控应用层连接数（netstat）
EOF
    fi
    print_success "已保存到 /etc/sysctl.conf"
}

# ============================================
# 生成防火墙持久化脚本
# ============================================

generate_firewall_script() {
    local ports="$1"

    print_info "生成 /etc/firewall.user 持久化规则"

    # 检查是否已有规则
    if [ -f /etc/firewall.user ] && grep -q "GNB VPN NOTRACK" /etc/firewall.user; then
        print_info "防火墙规则已存在"
        return
    fi

    # 备份现有文件
    if [ -f /etc/firewall.user ]; then
        cp /etc/firewall.user /etc/firewall.user.bak
        print_info "已备份 /etc/firewall.user -> /etc/firewall.user.bak"
    fi

    # 追加规则
    cat >> /etc/firewall.user << 'FIREWALL_EOF'

# ============================================
# GNB VPN NOTRACK 规则 (防火墙重启后自动应用)
# ============================================
# 让 GNB VPN 隧道流量绕过 conntrack，降低延迟
FIREWALL_EOF

    for port in $ports; do
        cat >> /etc/firewall.user << FIREWALL_EOF

# GNB 端口 $port
nft add rule inet fw4 raw_prerouting udp dport $port notrack
nft add rule inet fw4 raw_prerouting udp sport $port notrack
nft add rule inet fw4 raw_output udp dport $port notrack
nft add rule inet fw4 raw_output udp sport $port notrack
FIREWALL_EOF
    done

    print_success "已生成防火墙持久化规则"
}

# ============================================
# 检查 GNB Keepalive 配置
# ============================================

check_gnb_keepalive() {
    print_info "检查 GNB keepalive 配置"

    local gnb_conf=$(find /etc/mynet /etc -name "*.conf" -path "*/gnb/*" 2>/dev/null | head -1)

    if [ -n "$gnb_conf" ]; then
        print_success "找到 GNB 配置: $gnb_conf"

        # 检查是否有 keepalive 相关配置
        if grep -qE "(pf_route|keepalive|heartbeat)" "$gnb_conf" 2>/dev/null; then
            print_info "配置文件包含路由或心跳相关设置"
        else
            print_warning "建议在 GNB 配置中启用 keepalive/heartbeat"
            print_info "参考: WireGuard PersistentKeepalive=25"
        fi
    else
        print_warning "未找到 GNB 配置文件"
    fi
}

# ============================================
# 显示当前状态
# ============================================

show_status() {
    local role=$1

    print_header "当前状态"

    if [ "$role" = "router" ]; then
        echo ""
        echo "Conntrack 容量:"
        if [ -e /proc/sys/net/nf_conntrack_max ]; then
            cat /proc/sys/net/nf_conntrack_max
        else
            cat /proc/sys/net/netfilter/nf_conntrack_max
        fi

        echo ""
        echo "当前使用:"
        if [ -e /proc/sys/net/nf_conntrack_count ]; then
            cat /proc/sys/net/nf_conntrack_count
        else
            cat /proc/sys/net/netfilter/nf_conntrack_count
        fi
    else
        echo ""
        print_info "代理服务器角色，conntrack 统计不重要"
        print_info "应监控的指标:"
        echo "  - 应用层连接数: netstat -an | grep :443 | grep ESTABLISHED | wc -l"
        echo "  - TCP 连接统计: ss -s | grep estab"
        echo "  - 应用日志: tail -f /var/log/sniproxy/access.log"
    fi

    echo ""
    echo "UDP 超时配置:"
    echo "  普通 UDP: $(cat /proc/sys/net/netfilter/nf_conntrack_udp_timeout)s"
    echo "  UDP stream: $(cat /proc/sys/net/netfilter/nf_conntrack_udp_timeout_stream)s"

    if [ "$role" = "router" ]; then
        echo ""
        echo "TCP 超时配置:"
        echo "  ESTABLISHED: $(cat /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_established)s"
        echo "  TIME_WAIT: $(cat /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_time_wait)s"
        echo "  CLOSE_WAIT: $(cat /proc/sys/net/netfilter/nf_conntrack_tcp_timeout_close_wait)s"
    fi
    echo ""
    echo "NOTRACK 规则:"
    if nft list chain inet fw4 raw_prerouting 2>/dev/null | grep -q "notrack"; then
        nft list chain inet fw4 raw_prerouting 2>/dev/null | grep "notrack"
    else
        echo "  无 NOTRACK 规则"
    fi
}

# ============================================
# 主函数
# ============================================

main() {
    print_header "GNB VPN Conntrack 优化"

    # 检查是否为 root
    if [ "$(id -u)" -ne 0 ]; then
        print_error "需要 root 权限"
        exit 1
    fi

    echo ""

    # 0. 检测设备角色
    print_header "步骤 0: 检测设备角色"
    echo ""
    local device_role=$(detect_device_role)
    if [ "$device_role" = "router" ]; then
        print_info "检测到角色: 路由器（主路由/旁路由，有 NAT）"
        print_info "将优化: NOTRACK + UDP 超时 + TCP 超时"
    else
        print_info "检测到角色: 服务器（代理服务器，无 NAT）"
        print_info "将优化: NOTRACK + UDP 超时"
        print_warning "⚠️  代理服务器应监控应用层连接数（netstat），而非 conntrack"
    fi
    echo ""

    # 1. 检测 GNB 端口
    print_header "步骤 1: 检测 GNB 端口"
    echo ""
    local gnb_ports=$(detect_gnb_ports)
    print_info "检测到端口: $gnb_ports"
    echo ""

    # 2. 添加 NOTRACK 规则
    print_header "步骤 2: 添加 NOTRACK 规则"
    echo ""
    for port in $gnb_ports; do
        add_notrack_rules "$port"
    done
    echo ""

    # 3. 优化 UDP 超时
    print_header "步骤 3: 优化 UDP Conntrack 超时"
    echo ""
    optimize_udp_timeouts
    echo ""

    # 4. 优化 TCP 超时（仅路由器）
    print_header "步骤 4: 优化 TCP Conntrack 超时"
    echo ""
    optimize_tcp_timeouts "$device_role"
    echo ""

    # 5. 保存到 sysctl.conf
    print_header "步骤 5: 保存配置"
    echo ""
    save_to_sysctl "$device_role"
    generate_firewall_script "$gnb_ports"
    echo ""

    # 6. 检查 GNB keepalive
    print_header "步骤 6: 检查 GNB Keepalive"
    echo ""
    check_gnb_keepalive
    echo ""

    # 7. 显示状态
    show_status "$device_role"
    echo ""

    # 完成
    print_header "优化完成"
    echo ""
    print_success "所有优化已应用"
    echo ""
    echo "📋 总结:"
    echo "  角色: $device_role"
    echo "  1. GNB VPN 流量 (UDP) → 绕过 conntrack (NOTRACK)"
    echo "  2. UDP conntrack 超时 → 15~60秒（短超时）"
    if [ "$device_role" = "router" ]; then
        echo "  3. TCP conntrack 超时 → 优化（仅 NAT 后应用流量）"
        echo "  4. 配置已保存到 /etc/sysctl.conf 和 /etc/firewall.user"
    else
        echo "  3. TCP conntrack → 使用默认值（无 NAT）"
        echo "  4. 配置已保存到 /etc/sysctl.conf 和 /etc/firewall.user"
        echo ""
        echo "⚠️  重要提示（代理服务器）:"
        echo "  - 不要监控 /proc/sys/net/netfilter/nf_conntrack_count"
        echo "  - 应监控应用层连接数: netstat -an | grep :443 | wc -l"
        echo "  - 应监控应用层日志: tail -f /var/log/sniproxy/access.log"
    fi
    echo ""
    echo "💡 为什么这样做？"
    echo "  - VPN 隧道不需要状态追踪，绕过 conntrack 降低延迟"
    echo "  - UDP conntrack 短超时 + GNB 应用层 keepalive（不依赖内核）"
    if [ "$device_role" = "router" ]; then
        echo "  - TCP 优化仅针对 NAT 后应用流量，避免旧状态干扰"
    fi
    echo ""
    echo "🔄 防火墙重启后规则会自动应用 (/etc/firewall.user)"
    echo ""
    echo "📖 完整规范: docs/GNB_CONNTRACK_SPECIFICATION.md"
