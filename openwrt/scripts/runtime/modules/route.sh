#!/bin/bash
# route.sh - OpenWrt路由管理模块 (基于UCI)
# 使用UCI系统管理网络配置，保持OpenWrt原生方式

# 检查UCI网络配置
check_uci_network() {
    if ! command -v uci >/dev/null 2>&1; then
        echo "❌ UCI工具未安装"
        return 1
    fi
    
    # 检查网络配置是否存在
    if ! uci show network >/dev/null 2>&1; then
        echo "❌ UCI网络配置不存在"
        return 1
    fi
    
    return 0
}

# 显示OpenWrt网络信息
show_openwrt_network_info() {
    echo "🔍 OpenWrt网络信息:"
    
    if check_uci_network; then
        echo "  UCI配置: ✅ 正常"
        
        # 显示interfaces
        local interfaces=$(uci show network | grep "network\..*=interface" | wc -l)
        echo "  网络接口: $interfaces 个"
        
        # 显示devices
        local devices=$(uci show network | grep "network\..*=device" | wc -l 2>/dev/null || echo "0")
        echo "  网络设备: $devices 个"
        
        # 显示routes
        local routes=$(uci show network | grep "network\..*=route" | wc -l 2>/dev/null || echo "0")
        echo "  静态路由: $routes 条"
    else
        echo "  UCI配置: ❌ 异常"
    fi
}

# 创建VPN网络接口
create_vpn_interface() {
    local interface_name="$1"
    local device_name="$2"
    local protocol="${3:-none}"
    local vpn_network="$4"
    
    if [ -z "$interface_name" ] || [ -z "$device_name" ]; then
        echo "❌ 错误: 缺少必要参数"
        return 1
    fi
    
    echo "🔧 创建VPN网络接口: $interface_name"
    
    # 检查接口是否已存在
    if uci show network | grep -q "network\.$interface_name=interface"; then
        echo "⚠️ 接口已存在，将更新配置"
        uci delete network."$interface_name" 2>/dev/null || true
    fi
    
    # 创建interface配置
    uci set network."$interface_name"=interface
    uci set network."$interface_name".device="$device_name"
    uci set network."$interface_name".proto="$protocol"
    
    # 如果指定了VPN网络，配置IP
    if [ -n "$vpn_network" ]; then
        local vpn_ip=$(echo "$vpn_network" | cut -d'/' -f1)
        local vpn_netmask=$(echo "$vpn_network" | cut -d'/' -f2)
        
        if [ "$protocol" = "static" ]; then
            uci set network."$interface_name".ipaddr="$vpn_ip"
            uci set network."$interface_name".netmask="$(cidr_to_netmask "$vpn_netmask")"
        fi
    fi
    
    echo "✅ VPN接口创建完成: $interface_name"
    return 0
}

# CIDR转netmask (简单实现)
cidr_to_netmask() {
    local cidr="$1"
    case "$cidr" in
        8) echo "255.0.0.0" ;;
        16) echo "255.255.0.0" ;;
        24) echo "255.255.255.0" ;;
        25) echo "255.255.255.128" ;;
        26) echo "255.255.255.192" ;;
        27) echo "255.255.255.224" ;;
        28) echo "255.255.255.240" ;;
        29) echo "255.255.255.248" ;;
        30) echo "255.255.255.252" ;;
        *) echo "255.255.255.0" ;;  # 默认/24
    esac
}

# 添加静态路由
add_static_route() {
    local route_name="$1"
    local target_network="$2"
    local gateway="$3"
    local interface="$4"
    local metric="${5:-0}"
    
    if [ -z "$route_name" ] || [ -z "$target_network" ]; then
        echo "❌ 错误: 缺少必要参数"
        return 1
    fi
    
    echo "🔧 添加静态路由: $route_name ($target_network)"
    
    # 检查路由是否已存在
    if uci show network | grep -q "network\.$route_name=route"; then
        echo "⚠️ 路由已存在，将更新配置"
        uci delete network."$route_name" 2>/dev/null || true
    fi
    
    # 创建route配置
    uci set network."$route_name"=route
    uci set network."$route_name".target="$target_network"
    
    if [ -n "$gateway" ]; then
        uci set network."$route_name".gateway="$gateway"
    fi
    
    if [ -n "$interface" ]; then
        uci set network."$route_name".interface="$interface"
    fi
    
    if [ "$metric" != "0" ]; then
        uci set network."$route_name".metric="$metric"
    fi
    
    echo "✅ 静态路由添加完成: $route_name"
    return 0
}

# 配置VPN网络
configure_vpn_network_openwrt() {
    local vpn_interface="$1"
    local vpn_network="$2"
    local remote_networks="$3"
    local interface_name="${4:-mynet}"
    
    if [ -z "$vpn_interface" ]; then
        echo "❌ 错误: 未指定VPN接口"
        return 1
    fi
    
    echo "🔧 配置OpenWrt VPN网络..."
    echo "  VPN接口: $vpn_interface"
    echo "  VPN网络: $vpn_network"
    echo "  远程网络: $remote_networks"
    echo "  接口名称: $interface_name"
    
    # 备份当前配置
    backup_file "/etc/config/network"
    
    # 创建VPN接口
    if [ -n "$vpn_network" ]; then
        create_vpn_interface "$interface_name" "$vpn_interface" "static" "$vpn_network"
    else
        create_vpn_interface "$interface_name" "$vpn_interface" "none"
    fi
    
    # 添加远程网络路由
    if [ -n "$remote_networks" ]; then
        local route_counter=1
        for network in $remote_networks; do
            local route_name="${interface_name}_route_${route_counter}"
            add_static_route "$route_name" "$network" "" "$interface_name"
            route_counter=$((route_counter + 1))
        done
    fi
    
    # 提交UCI配置
    if uci commit network; then
        echo "✅ UCI网络配置已提交"
    else
        echo "❌ UCI配置提交失败"
        return 1
    fi
    
    # 重启网络服务
    if /etc/init.d/network restart; then
        echo "✅ 网络服务已重启"
    else
        echo "❌ 网络服务重启失败"
        return 1
    fi
    
    echo "✅ OpenWrt VPN网络配置完成"
    return 0
}

# 移除VPN网络配置
remove_vpn_network_openwrt() {
    local interface_name="${1:-mynet}"
    
    echo "🧹 移除OpenWrt VPN网络配置..."
    
    # 备份当前配置
    backup_file "/etc/config/network"
    
    # 移除VPN接口
    if uci show network | grep -q "network\.$interface_name=interface"; then
        echo "  移除接口: $interface_name"
        uci delete network."$interface_name" 2>/dev/null || true
    fi
    
    # 移除相关路由
    local route_sections=$(uci show network | grep "=route" | grep "$interface_name" | cut -d'.' -f2 | cut -d'=' -f1)
    for section in $route_sections; do
        echo "  移除路由: $section"
        uci delete network."$section" 2>/dev/null || true
    done
    
    # 提交配置
    if uci commit network; then
        echo "✅ UCI网络配置已提交"
    else
        echo "❌ UCI配置提交失败"
        return 1
    fi
    
    # 重启网络服务
    if /etc/init.d/network restart; then
        echo "✅ 网络服务已重启"
    else
        echo "❌ 网络服务重启失败"
        return 1
    fi
    
    echo "✅ OpenWrt VPN网络配置移除完成"
    return 0
}

# 显示网络状态
show_network_status() {
    echo "📊 OpenWrt网络状态:"
    
    # 显示interfaces
    echo ""
    echo "🔹 网络接口:"
    uci show network | grep "=interface" | while read -r line; do
        local section=$(echo "$line" | cut -d'.' -f2 | cut -d'=' -f1)
        local device=$(uci get network."$section".device 2>/dev/null || echo "unknown")
        local proto=$(uci get network."$section".proto 2>/dev/null || echo "none")
        local ipaddr=$(uci get network."$section".ipaddr 2>/dev/null || echo "auto")
        
        echo "  $section: device=$device, proto=$proto, ip=$ipaddr"
    done
    
    # 显示静态路由
    echo ""
    echo "🔹 静态路由:"
    uci show network | grep "=route" | while read -r line; do
        local section=$(echo "$line" | cut -d'.' -f2 | cut -d'=' -f1)
        local target=$(uci get network."$section".target 2>/dev/null || echo "unknown")
        local gateway=$(uci get network."$section".gateway 2>/dev/null || echo "direct")
        local interface=$(uci get network."$section".interface 2>/dev/null || echo "auto")
        
        echo "  $section: $target via $gateway dev $interface"
    done
    
    # 显示服务状态
    echo ""
    echo "🔹 网络服务状态:"
    if /etc/init.d/network status >/dev/null 2>&1; then
        echo "  ✅ 网络服务正在运行"
    else
        echo "  ❌ 网络服务未运行"
    fi
}

# 验证网络配置
validate_network_config() {
    echo "🔍 验证网络配置..."
    
    # 检查UCI配置语法
    if ! uci show network >/dev/null 2>&1; then
        echo "❌ UCI网络配置语法错误"
        return 1
    fi
    
    # 检查必要的接口是否存在
    local required_interfaces="loopback lan"
    for interface in $required_interfaces; do
        if ! uci show network | grep -q "network\.$interface=interface"; then
            echo "⚠️ 缺少必要的网络接口: $interface"
        fi
    done
    
    echo "✅ 网络配置验证完成"
    return 0
}

# 显示路由表
show_route_table() {
    echo "📋 OpenWrt路由表:"
    
    # 显示内核路由表
    if command -v ip >/dev/null 2>&1; then
        echo ""
        echo "🔹 内核路由表:"
        ip route show
    elif command -v route >/dev/null 2>&1; then
        echo ""
        echo "🔹 内核路由表:"
        route -n
    fi
    
    # 显示UCI配置的路由
    echo ""
    echo "🔹 UCI静态路由:"
    uci show network | grep "=route" | while read -r line; do
        local section=$(echo "$line" | cut -d'.' -f2 | cut -d'=' -f1)
        local target=$(uci get network."$section".target 2>/dev/null)
        local gateway=$(uci get network."$section".gateway 2>/dev/null)
        local interface=$(uci get network."$section".interface 2>/dev/null)
        local metric=$(uci get network."$section".metric 2>/dev/null)
        
        printf "  %-20s %-15s %-10s %s\n" \
            "${target:-unknown}" \
            "${gateway:-direct}" \
            "${interface:-auto}" \
            "${metric:-0}"
    done
}