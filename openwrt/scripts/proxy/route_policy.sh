#!/bin/bash
#
# route_policy.sh — 纯系统命令层：策略路由 / 防火墙规则管理
# 所有业务决策由 proxy.lua 预计算并写入 params 文件
#
# 用法:
#   ./route_policy.sh start|stop|restart
#   ./route_policy.sh server_start|server_stop
#   ./route_policy.sh dns_start <mode> <server>|dns_stop
#

set -e

MYNET_HOME="${MYNET_HOME:-/etc/mynet}"

# ─── 日志 ───
MYNET_LOG_DIR="$MYNET_HOME/logs"
mkdir -p "$MYNET_LOG_DIR" 2>/dev/null || true
MYNET_SCRIPT_LOG="$MYNET_LOG_DIR/route_policy.log"
exec > >(tee -a "$MYNET_SCRIPT_LOG") 2>&1
echo "[$(date '+%Y-%m-%d %H:%M:%S')] route_policy.sh $*"

# ─── 加载运行参数（由 proxy.lua write_policy_params 写入）───
PARAMS_FILE="$MYNET_HOME/var/proxy_policy_params.env"
if [ -f "$PARAMS_FILE" ]; then
    . "$PARAMS_FILE"
else
    case "$1" in
        stop) echo "[INFO] 参数文件不存在，尝试 fallback 清理" ;;
        *)    echo "[ERROR] 参数文件不存在: $PARAMS_FILE"; exit 1 ;;
    esac
fi

# 变量 fallback（stop 时 params 可能不存在）
TABLE_NAME="${TABLE_NAME:-mynet_proxy}"
TABLE_ID="${TABLE_ID:-200}"
FWMARK="${FWMARK:-0xc8}"
RULE_PRIORITY="${RULE_PRIORITY:-31800}"
ROUTE_CONFIG="${ROUTE_CONFIG:-$MYNET_HOME/conf/proxy/proxy_route.conf}"
IPSET_NAME="mynet_proxy"
NFT_SET_NAME="mynet_proxy"
IP_LIST_FILE="$MYNET_HOME/conf/proxy/proxy_outbound.txt"
MATCH_MODE="${MATCH_MODE:-normal}"
DNS_DOMESTIC_SERVER="${DNS_DOMESTIC_SERVER:-223.5.5.5,119.29.29.29}"

# 非国内 DNS 服务器列表由 proxy.lua 运行时注入（唯一真相）
# 未注入时使用以下 fallback
FOREIGN_DNS_SERVERS="${FOREIGN_DNS_SERVERS:-8.8.8.8 8.8.4.4 1.1.1.1 1.0.0.1 9.9.9.9 208.67.222.222 208.67.220.220}"

# ─── LAN 接口动态检测 ───
# 从 UCI 或路由表自动获取 LAN 桥接接口名和子网 CIDR
# 避免硬编码 br-lan，兼容 VLAN/DSA 等不同接口命名
detect_lan_interface() {
    # 1. 从 UCI 获取 LAN device（最可靠）
    if [ -z "${LAN_IFACE:-}" ]; then
        LAN_IFACE=$(uci get network.lan.device 2>/dev/null || echo "")
    fi
    # 2. fallback: 从路由表获取（scope link 的直连路由对应接口）
    if [ -z "$LAN_IFACE" ]; then
        LAN_IFACE=$(ip route show 2>/dev/null | awk '/scope link/ && !/lo / {print $3; exit}')
    fi
    # 3. 最终 fallback
    LAN_IFACE="${LAN_IFACE:-br-lan}"

    # LAN_PREFIX: LAN 子网 CIDR（用于路由查询）
    if [ -z "${LAN_PREFIX:-}" ]; then
        local lan_ip lan_mask
        lan_ip=$(uci get network.lan.ipaddr 2>/dev/null || echo "")
        lan_mask=$(uci get network.lan.netmask 2>/dev/null || echo "")
        if [ -n "$lan_ip" ] && [ -n "$lan_mask" ]; then
            # 从 IP 和 netmask 计算 CIDR 前缀长度
            local cidr=0
            for octet in $(echo "$lan_mask" | tr '.' ' '); do
                case "$octet" in
                    255) cidr=$((cidr + 8)) ;;
                    254) cidr=$((cidr + 7)) ;;
                    252) cidr=$((cidr + 6)) ;;
                    248) cidr=$((cidr + 5)) ;;
                    240) cidr=$((cidr + 4)) ;;
                    224) cidr=$((cidr + 3)) ;;
                    192) cidr=$((cidr + 2)) ;;
                    128) cidr=$((cidr + 1)) ;;
                esac
            done
            # 计算网络地址（简单方式：取 IP 的前 N 位）
            local IFS='.'; set -- $lan_ip; local a1=$1 a2=$2 a3=$3 a4=$4
            case "$cidr" in
                24) LAN_PREFIX="${a1}.${a2}.${a3}.0/${cidr}" ;;
                16) LAN_PREFIX="${a1}.${a2}.0.0/${cidr}" ;;
                8)  LAN_PREFIX="${a1}.0.0.0/${cidr}" ;;
                *)  LAN_PREFIX="${a1}.${a2}.${a3}.0/24" ;;
            esac
        else
            # fallback: 从路由表推断
            LAN_PREFIX=$(ip route show 2>/dev/null | awk '/dev .* scope link/ && !/lo / {print $1; exit}')
        fi
        LAN_PREFIX="${LAN_PREFIX:-192.168.1.0/24}"
    fi
    echo "[INFO] LAN interface: $LAN_IFACE, LAN prefix: $LAN_PREFIX"
}

# ─── 共用函数 ───

ensure_route_table() {
    if ! grep -qE "^[[:space:]]*${TABLE_ID}[[:space:]]+${TABLE_NAME}" /etc/iproute2/rt_tables 2>/dev/null; then
        echo "$TABLE_ID $TABLE_NAME" >> /etc/iproute2/rt_tables
        echo "[INFO] 已注册路由表 $TABLE_NAME ($TABLE_ID)"
    fi
}

add_default_routes() {
    if ip route show table "$TABLE_ID" 2>/dev/null | grep -q '.'; then
        echo "[INFO] 路由表已有路由，跳过"
        return 0
    fi
    IFS=',' read -ra gw_array <<< "$PROXY_GATEWAYS"
    if [ ${#gw_array[@]} -eq 1 ]; then
        ip route add default via "${gw_array[0]}" dev "$GNB_INTERFACE" table "$TABLE_ID" 2>&1 || true
    else
        local nexthops=""
        for gw in "${gw_array[@]}"; do
            gw=$(echo "$gw" | xargs)
            nexthops="$nexthops nexthop via $gw dev $GNB_INTERFACE weight 1"
        done
        eval ip route add default table "$TABLE_ID" $nexthops 2>&1 || true
    fi
    if ip route show table "$TABLE_ID" 2>/dev/null | grep -q 'default'; then
        echo "[INFO] ✓ 路由表已配置 (gw=${PROXY_GATEWAYS}, dev=$GNB_INTERFACE)"
    else
        echo "[ERROR] 路由添加失败"
        return 1
    fi
}

add_ip_rules() {
    if ! ip rule list | grep -q "fwmark $FWMARK"; then
        ip rule add fwmark "$FWMARK" table "$TABLE_ID" prio "$RULE_PRIORITY" 2>/dev/null || true
        echo "[INFO] ✓ fwmark 策略路由已配置"
    fi
}

# ─── 修复 WAN 网关路由冲突 ───
# GNB 可能学到与 WAN 同子网的路由（如 192.168.10.0/24 via gnb_tun），
# 覆盖 eth1 的直连路由，导致网关不可达。
# 通过添加 /32 host route 确保网关始终走 WAN 接口。
# GUARD-OK 代理策略表 200 / DNS /32 的写入点，不是「对端路由」写入者
# （对端路由唯一入口 = mynet apply，见 spec/route-apply-unification.md）。
fix_wan_gateway_route() {
    local wan_gw wan_dev
    wan_gw=$(ip route show default | awk '/dev (eth|wan|ppp)/{print $3; exit}')
    wan_dev=$(ip route show default | awk '/dev (eth|wan|ppp)/{print $5; exit}')
    [ -z "$wan_gw" ] || [ -z "$wan_dev" ] && return 0

    # 检查网关是否被 GNB 子网路由覆盖
    local current_dev
    current_dev=$(ip route get "$wan_gw" 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}')
    if [ "$current_dev" = "$GNB_INTERFACE" ]; then
        ip route replace "${wan_gw}/32" dev "$wan_dev" 2>/dev/null
        echo "[INFO] ✓ 修复 WAN 网关路由: ${wan_gw} → ${wan_dev} (原 ${current_dev})"
    fi
}

# ─── 路由非国内 DNS 服务器 ───
# 该逻辑统一由 proxy.lua (M.route_foreign_dns / M.unroute_foreign_dns) 维护
# 本脚本不再重复实现,以保证 Lua 与 shell 单一真相

# ─── split DNS 模式（dnsmasq + GFW list 域名分流）───
# 完全委托给 dns_split.sh —— 本脚本不再保留 inline 实现,避免双源真相

setup_split_dns() {
    echo "[INFO] 配置 split DNS 模式..."
    local dns_split_sh="$MYNET_HOME/scripts/proxy/dns_split.sh"
    if [ ! -f "$dns_split_sh" ]; then
        echo "[ERROR] dns_split.sh 不存在: $dns_split_sh"
        return 1
    fi
    bash "$dns_split_sh" setup "$DNS_DOMESTIC_SERVER" "${DNS_SERVER:-8.8.8.8,1.1.1.1}" || {
        echo "[ERROR] dns_split.sh setup 失败"
        return 1
    }
    echo "[INFO] ✓ split DNS 配置完成"
}

setup_split_dns_inline() {
    # 内联配置：直接部署 dnsmasq 分流
    local domestic_dns
    domestic_dns=$(echo "$DNS_DOMESTIC_SERVER" | tr ',' ' ')
    local foreign_dns="${DNS_SERVER:-8.8.8.8}"

    # 配置 dnsmasq 默认上游为国内 DNS
    uci -q del dhcp.@dnsmasq[0].server || true
    for dns in $domestic_dns; do
        uci add_list dhcp.@dnsmasq[0].server="$dns"
    done
    uci set dhcp.@dnsmasq[0].noresolv='1'
    uci -q delete dhcp.@dnsmasq[0].confdir || true
    uci add_list dhcp.@dnsmasq[0].confdir='/etc/dnsmasq.d'
    uci commit dhcp

    # 确保 dnsmasq.d 目录存在
    mkdir -p /etc/dnsmasq.d

    # 下载并解析 GFW list（如果不存在或需要更新）
    local gfw_conf="/etc/dnsmasq.d/gfwlist.conf"
    if [ ! -f "$gfw_conf" ] || [ "$(find "$gfw_conf" -mtime +1 2>/dev/null)" ]; then
        update_gfwlist "$foreign_dns"
    fi

    # 重启 dnsmasq
    /etc/init.d/dnsmasq restart 2>/dev/null
    echo "[INFO] ✓ dnsmasq split DNS 已配置 (国内: $domestic_dns, 国外: $foreign_dns)"
}

update_gfwlist() {
    local foreign_dns="${1:-8.8.8.8}"
    local gfw_conf="/etc/dnsmasq.d/gfwlist.conf"
    local gfw_tmp="/tmp/gfwlist.txt"

    echo "[INFO] 下载 GFW list..."
    curl -s --max-time 60 -o "$gfw_tmp" \
        'https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt' 2>/dev/null
    if [ ! -s "$gfw_tmp" ]; then
        curl -s --max-time 60 -o "$gfw_tmp" \
            'https://ghproxy.net/https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt' 2>/dev/null
    fi
    if [ ! -s "$gfw_tmp" ]; then
        echo "[WARN] GFW list 下载失败，跳过"
        return 1
    fi

    # 解析 GFW list（内联 Python）
    python3 - "$gfw_tmp" "$gfw_conf" "$foreign_dns" << 'PYTHON'
import base64, re, sys

gfw_file, out_file, dns_server = sys.argv[1], sys.argv[2], sys.argv[3]

with open(gfw_file) as f:
    text = f.read()

decoded = base64.b64decode(text).decode("utf-8", errors="ignore")
domains = set()

for line in decoded.split("\n"):
    line = line.strip()
    if not line or line[0] in ("!", "@", "["):
        continue
    line = line.lstrip("||").lstrip("|").lstrip(".")
    m = re.match(r"([a-zA-Z0-9][-a-zA-Z0-9]*\.)+[a-zA-Z]{2,}", line)
    if m:
        d = m.group(0).lower()
        if d.endswith(".cn") or d.endswith(".com.cn"):
            continue
        parts = d.split(".")
        for i in range(len(parts) - 1):
            sub = ".".join(parts[i:])
            if len(sub.split(".")) >= 2:
                domains.add(sub)

with open(out_file, "w") as f:
    for d in sorted(domains):
        f.write(f"server=/{d}/{dns_server}\n")

print(f"[INFO] GFW list: {len(domains)} domains → {dns_server}")
PYTHON

    rm -f "$gfw_tmp"
    echo "[INFO] ✓ GFW list 已更新"
}

# ─── start ───

start() {
    echo "[INFO] 启动代理路由 (FW=$FW_TYPE, TABLE=$TABLE_ID, MARK=$FWMARK)"

    if [ -z "$GNB_INTERFACE" ] || ! ip link show "$GNB_INTERFACE" >/dev/null 2>&1; then
        echo "[ERROR] GNB 接口 ${GNB_INTERFACE:-未配置} 不存在"
        return 1
    fi
    [ ! -f "$ROUTE_CONFIG" ] && { echo "[ERROR] 配置文件不存在: $ROUTE_CONFIG"; return 1; }

    ensure_route_table

    case "$FW_TYPE" in
        nftables) start_nftables ;;
        iptables) start_iptables ;;
        *)        echo "[ERROR] 不支持的防火墙类型: $FW_TYPE"; return 1 ;;
    esac

    add_default_routes
    add_ip_rules
    fix_wan_gateway_route

    # 非国内 DNS 路由已由 proxy.lua (M.route_foreign_dns) 在调用本脚本后执行，
    # 此处不重复处理；split DNS 同样委托给 dns_split.sh

    # DNS 模式处理
    case "${DNS_MODE:-none}" in
        none)
            echo "[INFO] DNS_MODE=none，跳过 dnsmasq 配置（本机 DNS 方案保持不变）"
            ;;
        split)
            # split 模式：dnsmasq + GFW list 域名分流
            # 不让 dns_split 失败拦截整个代理路由启动
            setup_split_dns || echo "[WARN] DNS split 配置失败，代理路由仍继续"
            ;;
        redirect|resolv)
            # 传统模式：dnsmasq 上游切换到 peer smartdns
            uci -q del dhcp.@dnsmasq[0].server || true
            uci add_list dhcp.@dnsmasq[0].server="$PROXY_GATEWAYS"
            uci commit dhcp && /etc/init.d/dnsmasq reload 2>/dev/null
            echo "[INFO] ✓ dnsmasq 已切换到 smartdns ($PROXY_GATEWAYS via gnb_tun)"

            # DNS 劫持（redirect 模式）
            local _dns_srv="${DNS_SERVER:-$PROXY_GATEWAYS}"
            [ -n "$_dns_srv" ] && start_dns_intercept "$DNS_MODE" "$_dns_srv"
            ;;
    esac

    echo "[INFO] ✓ 代理路由已启动"
}

start_iptables() {
    echo "[INFO] iptables + ipset 模式"

    # ipset 创建 + 批量加载
    ipset create $IPSET_NAME hash:net 2>/dev/null || ipset flush $IPSET_NAME
    [ ! -f "$IP_LIST_FILE" ] && { echo "[ERROR] IP 列表不存在: $IP_LIST_FILE"; return 1; }

    local temp="/tmp/ipset_restore_$$"
    {
        echo "create $IPSET_NAME hash:net -exist"
        grep -v '^#' "$IP_LIST_FILE" | grep -v '^$' | sed "s/^/add $IPSET_NAME /"
    } > "$temp"
    if ipset restore -file "$temp" 2>/dev/null; then
        echo "[INFO] ✓ ipset 批量加载完成"
    else
        echo "[WARN] 批量失败，逐条添加"
        grep -v '^#' "$IP_LIST_FILE" | grep -v '^$' | while IFS= read -r ip; do
            ipset add $IPSET_NAME "$ip" 2>/dev/null || true
        done
    fi
    rm -f "$temp"

    # iptables mangle PREROUTING（排除 VPN 接口入方向，避免回包循环）
    if [ "$MATCH_MODE" = "inverted" ]; then
        # 反转匹配：非国内 IP 走代理
        iptables -w 5 -t mangle -C PREROUTING ! -i "$GNB_INTERFACE" \
            -m set ! --match-set $IPSET_NAME dst -j MARK --set-mark "$FWMARK" 2>/dev/null || \
        iptables -w 5 -t mangle -A PREROUTING ! -i "$GNB_INTERFACE" \
            -m set ! --match-set $IPSET_NAME dst -j MARK --set-mark "$FWMARK"
        echo "[INFO] ✓ iptables mangle 规则已配置（反转匹配: 非国内走代理）"
    else
        # 正常匹配
        iptables -w 5 -t mangle -C PREROUTING ! -i "$GNB_INTERFACE" \
            -m set --match-set $IPSET_NAME dst -j MARK --set-mark "$FWMARK" 2>/dev/null || \
        iptables -w 5 -t mangle -A PREROUTING ! -i "$GNB_INTERFACE" \
            -m set --match-set $IPSET_NAME dst -j MARK --set-mark "$FWMARK"
        echo "[INFO] ✓ iptables mangle 规则已配置（正常匹配）"
    fi

    # 源地址策略路由（本机流量可选）
    local src_ip
    src_ip=$(ip addr show "$GNB_INTERFACE" 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
    if [ -n "$src_ip" ]; then
        ip rule list | grep -q "from $src_ip.*lookup $TABLE_NAME" || \
            ip rule add from "$src_ip" table "$TABLE_ID" prio $((RULE_PRIORITY - 1)) 2>/dev/null || true
    fi
}

start_nftables() {
    echo "[INFO] nftables 模式"

    # 创建 table + set
    nft add table inet mynet_proxy 2>/dev/null || true
    if nft list set inet mynet_proxy $NFT_SET_NAME >/dev/null 2>&1; then
        nft flush set inet mynet_proxy $NFT_SET_NAME 2>/dev/null || true
    else
        nft add set inet mynet_proxy $NFT_SET_NAME \
            '{ type ipv4_addr; flags interval; auto-merge; }' 2>/dev/null || true
    fi

    # 批量加载 IP set
    if nft -f "$ROUTE_CONFIG" 2>/dev/null; then
        local count
        count=$(grep -c "^add element" "$ROUTE_CONFIG" 2>/dev/null || echo 0)
        echo "[INFO] ✓ nft set 已加载 ($count 条)"
    else
        echo "[ERROR] nft 批量加载失败"
        return 1
    fi

    # PREROUTING mangle chain
    nft delete chain inet mynet_proxy mangle_prerouting 2>/dev/null || true
    nft add chain inet mynet_proxy mangle_prerouting \
        '{ type filter hook prerouting priority mangle; }'
    nft add set inet mynet_proxy exclude_ips \
        '{ type ipv4_addr; flags interval; }' 2>/dev/null || true
    # rule1: LAN 网段回包直接放行（排除 LAN 客户端的目的地址）
    nft add rule inet mynet_proxy mangle_prerouting ip daddr @exclude_ips return
    # rule2: VPN 接口入方向的 established/related 包直接 return，不打 mark
    #        避免 LAN 客户端经 VM 转发时，VPN 回包被误打 mark 再次路由进 VPN（死循环）
    nft add rule inet mynet_proxy mangle_prerouting \
        iifname "$GNB_INTERFACE" ct state established,related return
    # rule3: 非 VPN 接口的 established/related 包，恢复 ct mark（保持会话一致性）
    nft add rule inet mynet_proxy mangle_prerouting \
        iifname != "$GNB_INTERFACE" ip protocol != icmp \
        ct state established,related meta mark set ct mark return

    # 根据 MATCH_MODE 决定匹配逻辑
    if [ "$MATCH_MODE" = "inverted" ]; then
        # 反转匹配：非列表内 IP 走代理
        # domestic+blacklist: set=chinaip → !chinaip=境外 走代理
        nft add rule inet mynet_proxy mangle_prerouting \
            ip daddr != @$NFT_SET_NAME meta mark set "$FWMARK" counter
        nft add rule inet mynet_proxy mangle_prerouting \
            ip daddr != @$NFT_SET_NAME ct mark set meta mark
        echo "[INFO] ✓ nft PREROUTING 规则已配置（反转匹配）"
    else
        # 正常匹配：列表内 IP 走代理
        # domestic+whitelist: set=interip → 境外 走代理
        nft add rule inet mynet_proxy mangle_prerouting \
            ip daddr @$NFT_SET_NAME meta mark set "$FWMARK" counter
        nft add rule inet mynet_proxy mangle_prerouting \
            ip daddr @$NFT_SET_NAME ct mark set meta mark
        echo "[INFO] ✓ nft PREROUTING 规则已配置（正常匹配）"
    fi

    # POSTROUTING：防止 fw4 srcnat_lan 把 VPN→LAN 回包的源 IP 错误 masquerade
    # 场景：LAN 客户端经 VM 转发时，gnb_tun 回包经 br-lan 出去，fw4 会把 src 改成 VM IP
    # 解决：在比 fw4 srcnat 更高优先级（srcnat-5=95）先行 return，跳过 fw4 的 masquerade
    detect_lan_interface
    local lan_bridge="$LAN_IFACE"
    nft delete chain inet mynet_proxy postrouting_bypass 2>/dev/null || true
    nft add chain inet mynet_proxy postrouting_bypass \
        '{ type nat hook postrouting priority srcnat - 5; }'
    nft add rule inet mynet_proxy postrouting_bypass \
        iifname "$GNB_INTERFACE" oifname "$lan_bridge" return
    echo "[INFO] ✓ postrouting bypass 已配置（gnb_tun→${lan_bridge} 跳过 fw4 srcnat_lan）"

    # MSS clamping：防止 TCP 大包在 GNB 隧道（MTU 1450）被丢弃
    nft list chain inet fw4 mangle_postrouting 2>/dev/null | grep -q "${GNB_INTERFACE}.*maxseg" || \
    nft add rule inet fw4 mangle_postrouting \
        oifname "$GNB_INTERFACE" tcp flags '&' '(fin | syn | rst)' == syn \
        tcp option maxseg size set rt mtu \
        comment '"!fw4: mynet GNB egress MSS fix"' 2>/dev/null || true
    echo "[INFO] ✓ MSS clamping 已配置 (oifname $GNB_INTERFACE, rt mtu)"
}

# ─── server mode ───

start_server_mode() {
    echo "[INFO] 启动 Server 模式..."
    sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1

    local wan_iface
    wan_iface=$(ip route show default 2>/dev/null | awk '{print $5}' | head -1)
    [ -z "$wan_iface" ] && { echo "[ERROR] 无法检测 WAN 接口"; return 1; }
    local tun_iface="${GNB_INTERFACE:-}"
    [ -z "$tun_iface" ] && { echo "[ERROR] VPN 接口未配置"; return 1; }
    echo "[INFO] $tun_iface → $wan_iface"

    case "$FW_TYPE" in
        nftables)
            nft add table inet mynet_proxy 2>/dev/null || true
            nft delete chain inet mynet_proxy server_postrouting 2>/dev/null || true
            nft add chain inet mynet_proxy server_postrouting \
                '{ type nat hook postrouting priority srcnat; }'
            nft add rule inet mynet_proxy server_postrouting oifname "$wan_iface" masquerade
            nft delete chain inet mynet_proxy server_forward 2>/dev/null || true
            nft add chain inet mynet_proxy server_forward \
                '{ type filter hook forward priority filter; }'
            nft add rule inet mynet_proxy server_forward \
                iifname "$tun_iface" oifname "$wan_iface" accept
            nft add rule inet mynet_proxy server_forward \
                iifname "$wan_iface" oifname "$tun_iface" ct state established,related accept
            # 白名单（可选）
            local whitelist="$MYNET_HOME/conf/proxy/proxy_whitelist.txt"
            if [ -f "$whitelist" ]; then
                nft add set inet mynet_proxy server_whitelist \
                    '{ type ipv4_addr; }' 2>/dev/null || true
                while IFS= read -r ip; do
                    case "$ip" in '#'*|'') continue ;; esac
                    nft add element inet mynet_proxy server_whitelist "{ $ip }" 2>/dev/null
                done < "$whitelist"
                nft insert rule inet mynet_proxy server_forward \
                    iifname "$tun_iface" ip saddr != @server_whitelist drop
                echo "[INFO] ✓ 白名单已加载"
            fi
            ;;
        iptables)
            iptables -w 5 -t nat -C POSTROUTING -o "$wan_iface" -j MASQUERADE 2>/dev/null || \
                iptables -w 5 -t nat -A POSTROUTING -o "$wan_iface" -j MASQUERADE
            iptables -w 5 -C FORWARD -i "$tun_iface" -o "$wan_iface" -j ACCEPT 2>/dev/null || \
                iptables -w 5 -A FORWARD -i "$tun_iface" -o "$wan_iface" -j ACCEPT
            iptables -w 5 -C FORWARD -i "$wan_iface" -o "$tun_iface" \
                -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || \
                iptables -w 5 -A FORWARD -i "$wan_iface" -o "$tun_iface" \
                -m state --state ESTABLISHED,RELATED -j ACCEPT
            ;;
    esac
    echo "[INFO] ✓ Server 模式已启动"
}

stop_server_mode() {
    echo "[INFO] 停止 Server 模式..."
    case "${FW_TYPE:-}" in
        nftables)
            nft delete chain inet mynet_proxy server_postrouting 2>/dev/null || true
            nft delete chain inet mynet_proxy server_forward 2>/dev/null || true
            nft delete set inet mynet_proxy server_whitelist 2>/dev/null || true
            ;;
        iptables)
            local wan_iface
            wan_iface=$(ip route show default 2>/dev/null | awk '{print $5}' | head -1)
            local tun_iface="${GNB_INTERFACE:-}"
            if [ -n "$wan_iface" ] && [ -n "$tun_iface" ]; then
                iptables -w 5 -t nat -D POSTROUTING -o "$wan_iface" -j MASQUERADE 2>/dev/null || true
                iptables -w 5 -D FORWARD -i "$tun_iface" -o "$wan_iface" -j ACCEPT 2>/dev/null || true
                iptables -w 5 -D FORWARD -i "$wan_iface" -o "$tun_iface" \
                    -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true
            fi
            ;;
    esac
    echo "[INFO] ✓ Server 模式已停止"
}

# ─── DNS intercept ───

start_dns_intercept() {
    local dns_mode="${1:-none}"
    local dns_server="${2:-}"
    [ "$dns_mode" = "none" ] || [ -z "$dns_mode" ] && return 0
    [ -z "$dns_server" ] && { echo "[ERROR] DNS 需要指定服务器地址"; return 1; }
    echo "[INFO] DNS 劫持: $dns_mode → $dns_server"

    # 如果 DNS server 不是 peer，强制路由到 gnb_tun（nft set/ipset 无法处理本机出站流量）
    local _peer="${PROXY_GATEWAYS%%,*}"
    if [ -n "$_peer" ] && [ "$dns_server" != "$_peer" ]; then
        ip route replace "${dns_server}/32" dev "$GNB_INTERFACE" 2>/dev/null && \
            echo "[INFO] DNS 路由: ${dns_server}/32 → ${GNB_INTERFACE}（非 peer，强制走隧道）"
        # 记录 DNS server，用于 stop 时清理路由
        mkdir -p "$MYNET_HOME/var" 2>/dev/null
        echo "$dns_server" > "$MYNET_HOME/var/dns_route_pin"
    fi

    detect_lan_interface
    case "$dns_mode" in
        redirect)
            case "$FW_TYPE" in
                nftables)
                    nft add table inet mynet_proxy 2>/dev/null || true
                    nft delete chain inet mynet_proxy dns_intercept 2>/dev/null || true
                    nft add chain inet mynet_proxy dns_intercept \
                        '{ type nat hook prerouting priority dstnat; }'
                    nft add rule inet mynet_proxy dns_intercept \
                        iifname "$LAN_IFACE" udp dport 53 dnat ip to "$dns_server:53"
                    nft add rule inet mynet_proxy dns_intercept \
                        iifname "$LAN_IFACE" tcp dport 53 dnat ip to "$dns_server:53"
                    ;;
                iptables)
                    iptables -w 5 -t nat -C PREROUTING -i "$LAN_IFACE" -p udp --dport 53 \
                        -j DNAT --to-destination "$dns_server:53" 2>/dev/null || \
                    iptables -w 5 -t nat -A PREROUTING -i "$LAN_IFACE" -p udp --dport 53 \
                        -j DNAT --to-destination "$dns_server:53"
                    iptables -w 5 -t nat -C PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 \
                        -j DNAT --to-destination "$dns_server:53" 2>/dev/null || \
                    iptables -w 5 -t nat -A PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 \
                        -j DNAT --to-destination "$dns_server:53"
                    ;;
            esac
            ;;
        resolv)
            local f="/tmp/resolv.conf.d/resolv.conf.auto"
            [ -f "$f" ] && cp "$f" "${f}.proxy_bak" && echo "nameserver $dns_server" > "$f"
            ;;
    esac
    echo "[INFO] ✓ DNS 劫持已启用"
}

stop_dns_intercept() {
    echo "[INFO] 停止 DNS 劫持..."

    # 清理 DNS 路由（由 start_dns_intercept 添加的）
    local _pin_file="$MYNET_HOME/var/dns_route_pin"
    if [ -f "$_pin_file" ]; then
        local _dns_srv
        _dns_srv=$(cat "$_pin_file")
        ip route delete "${_dns_srv}/32" dev "$GNB_INTERFACE" 2>/dev/null && \
            echo "[INFO] 已移除 DNS 路由: ${_dns_srv}/32"
        rm -f "$_pin_file"
    fi

    case "${FW_TYPE:-}" in
        nftables) nft delete chain inet mynet_proxy dns_intercept 2>/dev/null || true ;;
        iptables)
            detect_lan_interface
            iptables -w 5 -t nat -D PREROUTING -i "$LAN_IFACE" -p udp --dport 53 -j DNAT 2>/dev/null || true
            iptables -w 5 -t nat -D PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 -j DNAT 2>/dev/null || true
            ;;
    esac
    local bak="/tmp/resolv.conf.d/resolv.conf.auto.proxy_bak"
    [ -f "$bak" ] && mv "$bak" "/tmp/resolv.conf.d/resolv.conf.auto"
    echo "[INFO] ✓ DNS 劫持已停止"
}

# ─── stop ───

stop() {
    echo "[INFO] 停止代理路由..."
    stop_dns_intercept
    stop_server_mode

    # 非国内 DNS 路由由 proxy.lua (M.unroute_foreign_dns) 负责清理，本脚本不重复

    case "${FW_TYPE:-}" in
        nftables)
            nft delete table inet mynet_proxy 2>/dev/null || true
            # 清理 MSS clamping 规则
            local mss_handle
            mss_handle=$(nft -a list chain inet fw4 mangle_postrouting 2>/dev/null | \
                grep "${GNB_INTERFACE}.*maxseg\|maxseg.*${GNB_INTERFACE}" | awk '{print $NF}')
            [ -n "$mss_handle" ] && nft delete rule inet fw4 mangle_postrouting handle "$mss_handle" 2>/dev/null || true
            ;;
        iptables)
            iptables-save -t mangle 2>/dev/null | grep -- "--match-set $IPSET_NAME " | \
            while IFS= read -r rule; do
                iptables -w 5 -t mangle ${rule/-A /-D } 2>/dev/null || true
            done
            ipset destroy $IPSET_NAME 2>/dev/null || true
            ;;
        "")
            # FW_TYPE 未知 — fallback 两种都清理
            nft delete table inet mynet_proxy 2>/dev/null || true
            iptables-save -t mangle 2>/dev/null | grep -- "--match-set mynet_proxy " | \
            while IFS= read -r rule; do
                iptables -w 5 -t mangle ${rule/-A /-D } 2>/dev/null || true
            done
            ipset destroy mynet_proxy 2>/dev/null || true
            ;;
    esac

    # 清理所有 mynet_proxy 策略路由规则
    while ip rule list 2>/dev/null | grep -q "lookup $TABLE_NAME"; do
        local line
        line=$(ip rule list | grep "lookup $TABLE_NAME" | head -1)
        if [[ "$line" =~ fwmark[[:space:]]+([x0-9a-f]+) ]]; then
            ip rule del fwmark "${BASH_REMATCH[1]}" 2>/dev/null || break
        elif [[ "$line" =~ from[[:space:]]+([0-9.]+) ]]; then
            ip rule del from "${BASH_REMATCH[1]}" table "$TABLE_ID" 2>/dev/null || break
        else
            break
        fi
    done

    # 清理路由表
    if [ -f /etc/iproute2/rt_tables ]; then
        grep -E "^[[:space:]]*[0-9]+[[:space:]]+$TABLE_NAME" /etc/iproute2/rt_tables 2>/dev/null | \
        awk '{print $1}' | while read -r id; do
            ip route flush table "$id" 2>/dev/null || true
        done
    fi

    # 恢复 dnsmasq
    if [ "${DNS_MODE:-none}" != "none" ]; then
        # split 模式：委托 dns_split.sh 统一清理（gfwlist.conf / extra-domains.conf / dnsmasq.conf 注入段）
        if [ "${DNS_MODE}" = "split" ]; then
            local dns_split_sh="$MYNET_HOME/scripts/proxy/dns_split.sh"
            if [ -f "$dns_split_sh" ]; then
                bash "$dns_split_sh" stop 2>/dev/null || true
            else
                # 脚本丢失：fallback 仅清理已知文件
                rm -f /etc/dnsmasq.d/gfwlist.conf /etc/dnsmasq.d/extra-domains.conf 2>/dev/null
                uci -q delete dhcp.@dnsmasq[0].confdir 2>/dev/null || true
                uci set dhcp.@dnsmasq[0].noresolv='0' 2>/dev/null || true
            fi
        fi
        # redirect/resolv 模式：恢复 UCI 默认国内 DNS
        if [ "${DNS_MODE}" = "redirect" ] || [ "${DNS_MODE}" = "resolv" ]; then
            uci -q del dhcp.@dnsmasq[0].server || true
            local _restore_dns="${DNS_DOMESTIC_SERVER:-223.5.5.5,119.29.29.29}"
            IFS=',' read -ra _arr <<< "$_restore_dns"
            for d in "${_arr[@]}"; do
                d=$(echo "$d" | xargs)
                [ -n "$d" ] && uci add_list dhcp.@dnsmasq[0].server="$d"
            done
            uci commit dhcp && /etc/init.d/dnsmasq reload 2>/dev/null
            echo "[INFO] ✓ dnsmasq 已恢复默认 DNS ($_restore_dns)"
        fi
    else
        echo "[INFO] DNS_MODE=none，跳过 dnsmasq 恢复（本机 DNS 方案保持不变）"
    fi

    echo "[INFO] ✓ 代理路由已停止"
}

# ─── 主入口 ───
case "${1:-}" in
    start)         start ;;
    stop)          stop ;;
    restart)       stop; sleep 1; start ;;
    server_start)  start_server_mode ;;
    server_stop)   stop_server_mode ;;
    dns_start)     start_dns_intercept "$2" "$3" ;;
    dns_stop)      stop_dns_intercept ;;
    *)
        echo "用法: $0 {start|stop|restart|server_start|server_stop|dns_start|dns_stop}"
        exit 1
        ;;
esac
