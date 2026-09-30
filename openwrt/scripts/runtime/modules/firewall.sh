#!/bin/bash
# firewall.sh - OpenWrt防火墙管理模块 (基于UCI)
# 使用UCI系统管理防火墙配置，保持OpenWrt原生方式

# 检测防火墙版本
detect_firewall_version() {
    if command -v fw4 >/dev/null 2>&1; then
        echo "fw4"
    elif command -v fw3 >/dev/null 2>&1; then
        echo "fw3"
    else
        echo "unknown"
    fi
}

# 检查UCI防火墙配置
check_uci_firewall() {
    if ! command -v uci >/dev/null 2>&1; then
        echo "❌ UCI工具未安装"
        return 1
    fi
    
    # 检查防火墙配置是否存在
    if ! uci show firewall >/dev/null 2>&1; then
        echo "❌ UCI防火墙配置不存在"
        return 1
    fi
    
    return 0
}

# 显示OpenWrt防火墙信息
show_openwrt_firewall_info() {
    local fw_version=$(detect_firewall_version)
    
    echo "🔍 OpenWrt防火墙信息:"
    echo "  版本: $fw_version"
    
    if check_uci_firewall; then
        echo "  UCI配置: ✅ 正常"
        
        # 显示zones
        local zones=$(uci show firewall | grep "firewall\..*=zone" | wc -l)
        echo "  防火墙区域: $zones 个"
        
        # 显示rules
        local rules=$(uci show firewall | grep "firewall\..*=rule" | wc -l)
        echo "  防火墙规则: $rules 条"
        
        # 显示forwards
        local forwards=$(uci show firewall | grep "firewall\..*=forwarding" | wc -l)
        echo "  转发规则: $forwards 条"
    else
        echo "  UCI配置: ❌ 异常"
    fi
}

# 创建VPN防火墙区域
create_vpn_zone() {
    local zone_name="$1"
    local vpn_interface="$2"
    
    if [ -z "$zone_name" ] || [ -z "$vpn_interface" ]; then
        echo "❌ 错误: 缺少必要参数"
        return 1
    fi
    
    echo "🔧 创建VPN防火墙区域: $zone_name"
    
    # 检查区域是否已存在
    if uci show firewall | grep -q "name='$zone_name'"; then
        echo "⚠️ 区域已存在，将更新配置"
        local section=$(uci show firewall | grep "name='$zone_name'" | cut -d'.' -f2 | cut -d'=' -f1)
        uci delete firewall."$section" 2>/dev/null || true
    fi
    
    # 创建新的zone配置
    local zone_section=$(uci add firewall zone)
    uci set firewall."$zone_section".name="$zone_name"
    uci set firewall."$zone_section".input="ACCEPT"
    uci set firewall."$zone_section".output="ACCEPT" 
    uci set firewall."$zone_section".forward="ACCEPT"
    uci set firewall."$zone_section".masq="1"
    uci set firewall."$zone_section".mtu_fix="1"
    
    # 添加接口到zone
    uci add_list firewall."$zone_section".device="$vpn_interface"
    
    echo "✅ VPN区域创建完成: $zone_name"
    return 0
}

# 配置zone间转发规则
configure_zone_forwarding() {
    local from_zone="$1"
    local to_zone="$2"
    local action="${3:-ACCEPT}"
    
    if [ -z "$from_zone" ] || [ -z "$to_zone" ]; then
        echo "❌ 错误: 缺少必要参数"
        return 1
    fi
    
    echo "🔧 配置区域转发: $from_zone -> $to_zone ($action)"
    
    # 检查转发规则是否已存在
    if uci show firewall | grep -q "src='$from_zone'" | grep -q "dest='$to_zone'"; then
        echo "⚠️ 转发规则已存在"
        return 0
    fi
    
    # 创建转发规则
    local forward_section=$(uci add firewall forwarding)
    uci set firewall."$forward_section".src="$from_zone"
    uci set firewall."$forward_section".dest="$to_zone"
    
    echo "✅ 转发规则创建完成: $from_zone -> $to_zone"
    return 0
}

# 配置VPN防火墙规则
configure_vpn_firewall_openwrt() {
    local vpn_interface="$1"
    local vpn_zone="${2:-mynet}"
    local lan_zone="${3:-lan}"
    local wan_zone="${4:-wan}"
    
    if [ -z "$vpn_interface" ]; then
        echo "❌ 错误: 未指定VPN接口"
        return 1
    fi
    
    echo "🔧 配置OpenWrt VPN防火墙..."
    echo "  VPN接口: $vpn_interface"
    echo "  VPN区域: $vpn_zone"
    echo "  LAN区域: $lan_zone" 
    echo "  WAN区域: $wan_zone"
    
    # 备份当前配置
    backup_file "/etc/config/firewall"
    
    # 创建VPN区域
    if ! create_vpn_zone "$vpn_zone" "$vpn_interface"; then
        echo "❌ VPN区域创建失败"
        return 1
    fi
    
    # 配置区域间转发
    configure_zone_forwarding "$lan_zone" "$vpn_zone" "ACCEPT"
    configure_zone_forwarding "$vpn_zone" "$lan_zone" "ACCEPT"
    configure_zone_forwarding "$vpn_zone" "$wan_zone" "ACCEPT"
    
    # 提交UCI配置
    if uci commit firewall; then
        echo "✅ UCI防火墙配置已提交"
    else
        echo "❌ UCI配置提交失败"
        return 1
    fi
    
    # 重启防火墙服务
    if /etc/init.d/firewall restart; then
        echo "✅ 防火墙服务已重启"
    else
        echo "❌ 防火墙服务重启失败"
        return 1
    fi
    
    echo "✅ OpenWrt VPN防火墙配置完成"
    return 0
}

# 移除VPN防火墙配置
remove_vpn_firewall_openwrt() {
    local vpn_zone="${1:-mynet}"
    local vpn_interface="${2}"  # 可选：指定要删除的接口
    
    echo "🧹 移除OpenWrt VPN防火墙配置..."
    
    # 备份当前配置
    backup_file "/etc/config/firewall"
    
    # 查找zone section
    local zone_section=$(uci show firewall | grep "name='$vpn_zone'" | cut -d'.' -f2 | cut -d'=' -f1 | head -n1)
    
    if [ -z "$zone_section" ]; then
        echo "⚠️ 未找到zone: $vpn_zone"
        return 0
    fi
    
    # 如果指定了接口，尝试仅删除该接口
    if [ -n "$vpn_interface" ]; then
        # 获取当前zone中的所有device/network
        local device_count=$(uci show firewall."$zone_section" | grep -c "\\.device=\\|\.network=" || echo "0")
        
        if [ "$device_count" -gt 1 ]; then
            echo "  检测到zone中有多个接口，仅删除接口: $vpn_interface"
            
            # 删除指定的device
            uci del_list firewall."$zone_section".device="$vpn_interface" 2>/dev/null || true
            uci del_list firewall."$zone_section".network="$vpn_interface" 2>/dev/null || true
            
            # 提交配置并重启防火墙
            uci commit firewall
            /etc/init.d/firewall restart
            
            echo "✅ 已从zone中删除接口: $vpn_interface"
            return 0
        else
            echo "  zone中只有一个接口，将删除整个zone"
        fi
    fi
    
    # 删除整个zone（当zone中只有一个接口或未指定接口时）
    echo "  移除整个zone: $zone_section"
    uci delete firewall."$zone_section" 2>/dev/null || true
    
    # 移除相关转发规则
    local forward_sections=$(uci show firewall | grep -E "(src='$vpn_zone'|dest='$vpn_zone')" | cut -d'.' -f2 | cut -d'=' -f1 | sort | uniq)
    for section in $forward_sections; do
        echo "  移除转发规则: $section"
        uci delete firewall."$section" 2>/dev/null || true
    done
    
    # 提交配置
    if uci commit firewall; then
        echo "✅ UCI防火墙配置已提交"
    else
        echo "❌ UCI配置提交失败"
        return 1
    fi
    
    # 重启防火墙服务
    if /etc/init.d/firewall restart; then
        echo "✅ 防火墙服务已重启"
    else
        echo "❌ 防火墙服务重启失败"
        return 1
    fi
    
    echo "✅ OpenWrt VPN防火墙配置移除完成"
    return 0
}

# 显示防火墙状态
show_firewall_status() {
    echo "📊 OpenWrt防火墙状态:"
    
    # 显示zones
    echo ""
    echo "🔹 防火墙区域:"
    uci show firewall | grep "=zone" | while read -r line; do
        local section=$(echo "$line" | cut -d'.' -f2 | cut -d'=' -f1)
        local name=$(uci get firewall."$section".name 2>/dev/null || echo "unnamed")
        local input=$(uci get firewall."$section".input 2>/dev/null || echo "REJECT")
        local output=$(uci get firewall."$section".output 2>/dev/null || echo "REJECT")
        local forward=$(uci get firewall."$section".forward 2>/dev/null || echo "REJECT")
        local masq=$(uci get firewall."$section".masq 2>/dev/null || echo "0")
        
        echo "  $name: input=$input, output=$output, forward=$forward, masq=$masq"
    done
    
    # 显示转发规则
    echo ""
    echo "🔹 转发规则:"
    uci show firewall | grep "=forwarding" | while read -r line; do
        local section=$(echo "$line" | cut -d'.' -f2 | cut -d'=' -f1)
        local src=$(uci get firewall."$section".src 2>/dev/null || echo "any")
        local dest=$(uci get firewall."$section".dest 2>/dev/null || echo "any")
        
        echo "  $src -> $dest"
    done
    
    # 显示服务状态
    echo ""
    echo "🔹 防火墙服务状态:"
    if /etc/init.d/firewall status >/dev/null 2>&1; then
        echo "  ✅ 防火墙服务正在运行"
    else
        echo "  ❌ 防火墙服务未运行"
    fi
}

# 验证防火墙配置
validate_firewall_config() {
    echo "🔍 验证防火墙配置..."
    
    # 检查UCI配置语法
    if ! uci show firewall >/dev/null 2>&1; then
        echo "❌ UCI防火墙配置语法错误"
        return 1
    fi
    
    # 检查必要的zones是否存在
    local required_zones="lan wan"
    for zone in $required_zones; do
        if ! uci show firewall | grep -q "name='$zone'"; then
            echo "⚠️ 缺少必要的防火墙区域: $zone"
        fi
    done
    
    echo "✅ 防火墙配置验证完成"
    return 0
}