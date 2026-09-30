-- mynet/config.lua  — 配置文件管理
-- 读写 config.json（API地址）、zone.json（当前区域）、mynet.conf（VPN参数）。
-- 对应 Go 项目 internal/common/ 中的路径管理与 config 相关 infrastructure。

local M    = {}
local util = require("luci.model.mynet.util")

M.DEFAULT_API_URL         = "https://api.mynet.club/api/v2"
M.DEFAULT_CTL_URL         = "https://ctl.mynet.club"
M.DEFAULT_WWW_URL         = "https://www.mynet.club"
M.DEFAULT_GNB_ROOT        = util.GNB_DRIVER_ROOT
M.DEFAULT_GNB_BIN_PATH    = util.GNB_BIN_DIR .. "/gnb"
M.DEFAULT_SYSTEM_GNB_PATH = "/usr/sbin/gnb"
M.DEFAULT_MYNETD_BIN      = "/usr/bin/mynetd"
M.MYNETD_JSON             = util.CONF_DIR .. "/mynetd.json"

-- ─────────────────────────────────────────────────────────────
-- 运行模式: "mynet"（在线用户模式）| "guest"（GNB 离线模式）
-- ─────────────────────────────────────────────────────────────

function M.get_mode()
    local data = util.load_json_file(util.CONFIG_FILE)
    return data and data.mode or nil
end

function M.set_mode(mode)
    local data = util.load_json_file(util.CONFIG_FILE) or {}
    data.mode = mode
    return util.save_json_file(util.CONFIG_FILE, data)
end

-- ─────────────────────────────────────────────────────────────
-- 判断系统是否已有完整的 MyNet 配置
-- 依据配置文件状态判断，不依赖登录/Token 状态
-- 返回: true（已配置）/ false（未配置或不完整）
-- ─────────────────────────────────────────────────────────────
function M.is_mynet_configured()
    -- 1. mynet.conf 存在且有有效 NODE_ID
    local node_id = M.get_node_id()
    if not node_id or node_id == "0" then return false end
    -- 2. zone 已选择
    local zone = M.load_current_zone()
    if not zone or tostring(zone.zone_id) == "0" then return false end
    -- 3. mynet.conf 文件存在
    if not util.file_exists(util.VPN_CONF) then return false end
    return true
end

-- ─────────────────────────────────────────────────────────────
-- Server Config (config.json)
-- ─────────────────────────────────────────────────────────────

-- 加载服务端配置
-- 返回: { api_base_url, timeout, retry_count }
function M.load_server_config()
    local data = util.load_json_file(util.CONFIG_FILE)
    local sc   = data and data.server_config or {}
    return {
        api_base_url = sc.api_base_url or M.DEFAULT_API_URL,
        timeout      = sc.timeout      or 30,
        retry_count  = sc.retry_count  or 3,
    }
end

-- 保存服务端配置（仅更新 server_config 字段，保留其他配置不变）
function M.save_server_config(api_url, timeout, retry_count)
    local data = util.load_json_file(util.CONFIG_FILE) or {}
    data.server_config = {
        api_base_url = api_url      or M.DEFAULT_API_URL,
        timeout      = timeout      or 30,
        retry_count  = retry_count  or 3,
    }
    return util.save_json_file(util.CONFIG_FILE, data)
end

-- 快捷：获取 API base URL
function M.get_api_url()
    return M.load_server_config().api_base_url
end

-- ─────────────────────────────────────────────────────────────
-- GNB Config (config.json → gnb 字段)
-- 管理 gnb 二进制路径与配置根目录
-- ─────────────────────────────────────────────────────────────

-- 加载 gnb 配置
-- 返回: { gnb_root, gnb_bin_path, system_gnb_path, use_system_gnb }
function M.load_gnb_config()
    local data = util.load_json_file(util.CONFIG_FILE)
    local g    = data and data.gnb or {}
    return {
        gnb_root        = g.gnb_root        or M.DEFAULT_GNB_ROOT,
        gnb_bin_path    = g.gnb_bin_path    or M.DEFAULT_GNB_BIN_PATH,
        system_gnb_path = g.system_gnb_path or M.DEFAULT_SYSTEM_GNB_PATH,
        use_system_gnb  = g.use_system_gnb  or false,
    }
end

-- 保存 gnb 配置（仅更新 gnb 字段，保留其他配置不变）
function M.save_gnb_config(gnb_root, gnb_bin_path, system_gnb_path, use_system_gnb)
    local data = util.load_json_file(util.CONFIG_FILE) or {}
    data.gnb = {
        gnb_root        = gnb_root        or M.DEFAULT_GNB_ROOT,
        gnb_bin_path    = gnb_bin_path    or M.DEFAULT_GNB_BIN_PATH,
        system_gnb_path = system_gnb_path or M.DEFAULT_SYSTEM_GNB_PATH,
        use_system_gnb  = use_system_gnb  or false,
    }
    return util.save_json_file(util.CONFIG_FILE, data)
end

-- 获取当前有效的 gnb 可执行文件路径
-- use_system_gnb=true → system_gnb_path；否则 → gnb_bin_path
function M.get_gnb_bin()
    local gc = M.load_gnb_config()
    if gc.use_system_gnb then
        return gc.system_gnb_path
    end
    return gc.gnb_bin_path
end

-- 获取 gnb 节点配置根目录（即 gnb_root/conf）
function M.get_gnb_conf_root()
    return M.load_gnb_config().gnb_root .. "/conf"
end

-- ─────────────────────────────────────────────────────────────
-- Zone Config (zone.json)
-- ─────────────────────────────────────────────────────────────

-- 加载当前区域
-- 返回: { zone_id, zone_name } 或 nil
function M.load_current_zone()
    local d = util.load_json_file(util.ZONE_FILE)
    if not d then return nil end
    d.zone_id = util.int_str(d.zone_id or 0)
    return d
end

-- 保存当前区域选择
function M.save_current_zone(zone_id, zone_name)
    return util.save_json_file(util.ZONE_FILE, {
        zone_id    = util.int_str(zone_id or 0),
        zone_name  = zone_name  or "",
        updated_at = util.format_time(util.time_now()),
    })
end

-- ─────────────────────────────────────────────────────────────
-- VPN Runtime Config (mynet.conf)
-- bash-style KEY="VALUE" 格式
-- ─────────────────────────────────────────────────────────────

-- 加载 mynet.conf → { KEY = value, ... }
function M.load_vpn_conf()
    return util.parse_bash_conf(util.VPN_CONF) or {}
end

-- 快捷：VPN 类型（gnb 或 wireguard）
function M.get_vpn_type()
    return M.load_vpn_conf().VPN_TYPE or "gnb"
end

-- 快捷：VPN 网络接口名
function M.get_vpn_interface()
    return M.load_vpn_conf().VPN_INTERFACE or "gnb_tun"
end

-- 快捷：mynet.conf 中配置的 NODE_ID（十进制字符串或 nil）
-- 注意：返回 string 而非 number，避免 Lua tostring() 产生科学计数法
function M.get_node_id()
    local v = M.load_vpn_conf().NODE_ID
    if not v or v == "" then return nil end
    return util.int_str(tonumber(v) or 0)
end

-- 写入 mynet.conf 中的 NODE_ID（保留其余行不变）
function M.save_node_id(node_id)
    local id_str = util.int_str(node_id or 0)
    local content = util.read_file(util.VPN_CONF) or ""
    local found = false
    local lines = {}
    for line in (content .. "\n"):gmatch("([^\n]*)\n") do
        if line:match("^NODE_ID=") then
            lines[#lines+1] = 'NODE_ID="' .. id_str .. '"'
            found = true
        elseif line ~= "" or found then
            lines[#lines+1] = line
        end
    end
    if not found then
        lines[#lines+1] = 'NODE_ID="' .. id_str .. '"'
    end
    util.ensure_dir(util.CONF_DIR)
    return util.write_file(util.VPN_CONF, table.concat(lines, "\n") .. "\n")
end

-- ─────────────────────────────────────────────────────────────
-- 生成完整 mynet.conf（bash-style KEY="VALUE" 格式）
-- 供 rc.mynet init 脚本、route.mynet 等 shell 脚本使用
-- 对齐 service-manager.sh generate_mynet_config()
-- ─────────────────────────────────────────────────────────────
function M.generate_mynet_conf(node_id)
    local nid_str = util.int_str(node_id or 0)
    local vpn_type = M.get_vpn_type()
    local mynet_home = util.MYNET_HOME

    local vpn_interface, vpn_binary, vpn_config_dir, vpn_pid_file, route_config
    local gnb_bin, gnb_conf

    if vpn_type == "wireguard" then
        vpn_interface = "wg_" .. nid_str
        vpn_binary = "wg-quick"
        vpn_config_dir = mynet_home .. "/driver/wireguard/" .. nid_str
        vpn_pid_file = "/var/run/wg-wg_" .. nid_str .. ".pid"
        route_config = mynet_home .. "/conf/route.conf"
        gnb_bin = ""
        gnb_conf = ""
    else
        -- gnb (default)
        -- 接口名唯一权威来源：node.conf 的 ifname 字段
        -- （系统中可能运行多个 gnb 实例，不能用 ip link 探测）
        local node_conf_path = util.GNB_CONF_DIR .. "/" .. nid_str .. "/node.conf"
        local node_conf = util.read_file(node_conf_path) or ""
        local iface_from_conf = node_conf:match("^ifname%s+(%S+)") or node_conf:match("\nifname%s+(%S+)")
        local base_iface = (iface_from_conf and iface_from_conf ~= "") and iface_from_conf or "gnb_tun"
        -- 若 GNB 已运行，尝试找以 base_iface 开头的实际接口（如 gnb_tun_16）
        -- 多实例下通过 node_id 对应的 pid 文件确认
        local actual_iface = base_iface
        local gnb_pid_file = util.GNB_CONF_DIR .. "/" .. nid_str .. "/gnb.pid"
        if util.file_exists(gnb_pid_file) then
            local pid = util.trim(util.read_file(gnb_pid_file) or "")
            if pid ~= "" then
                -- 从 /proc/{pid}/net/if_inet6 或 ip link 找以 base_iface 开头的接口
                local found = util.trim(util.exec(
                    "ip link show 2>/dev/null | grep -o '" .. base_iface .. "[^:@]*'" ..
                    " | head -1") or "")
                if found ~= "" then actual_iface = found end
            end
        end
        vpn_interface = actual_iface
        gnb_bin = M.get_gnb_bin()
        gnb_conf = util.GNB_CONF_DIR .. "/" .. nid_str
        vpn_binary = gnb_bin
        vpn_config_dir = gnb_conf
        vpn_pid_file = gnb_conf .. "/gnb.pid"
        route_config = mynet_home .. "/conf/route.conf"
    end

    local lines = {
        "# MyNet 配置文件（自动生成）",
        "# VPN 类型: " .. vpn_type .. ", 节点 ID: " .. nid_str,
        "# 生成时间: " .. os.date("%Y-%m-%d %H:%M:%S"),
        "",
        "# 基础配置",
        'ROUTER_MODE="auto"',
        'VPN_ZONE="mynet"',
        "",
        "# VPN 类型配置",
        'VPN_TYPE="' .. vpn_type .. '"',
        'NODE_ID="' .. nid_str .. '"',
        'VPN_INTERFACE="' .. vpn_interface .. '"',
        "",
        "# 驱动路径",
        'VPN_BINARY="' .. vpn_binary .. '"',
        'VPN_CONFIG="' .. vpn_config_dir .. '"',
        'VPN_PID_FILE="' .. vpn_pid_file .. '"',
        "",
        "# GNB 配置",
        'GNB_BIN="' .. (gnb_bin or "") .. '"',
        'GNB_CONF="' .. (gnb_conf or "") .. '"',
        "",
        "# 路径配置",
        'MYNET_HOME="' .. mynet_home .. '"',
        'VPN_DRIVER_DIR="' .. mynet_home .. "/driver/" .. vpn_type .. '"',
        'VPN_CONFIG_DIR="' .. vpn_config_dir .. '"',
        'ROUTE_CONFIG="' .. route_config .. '"',
        "",
        "# 网络配置",
        'NETWORK_CONFIG_ENABLED="1"',
        'AUTO_ROUTE_SETUP="1"',
        'ROUTE_TABLE_ID="100"',
        'ROUTE_PRIORITY="1000"',
        "",
        "# 监控配置",
        'HEALTH_CHECK_ENABLED="1"',
        'HEALTH_CHECK_INTERVAL="60"',
        'VPN_TIMEOUT="30"',
        "",
        "# 服务配置",
        'AUTO_START="1"',
        'RELOAD_CONFIG_ON_CHANGE="1"',
        'CLEANUP_ON_STOP="1"',
    }

    -- 检测接口名变更 → 同步更新 UCI network/firewall 绑定
    local old_iface = M.get_vpn_interface()
    util.ensure_dir(util.CONF_DIR)
    local ok, err = util.write_file(util.VPN_CONF, table.concat(lines, "\n") .. "\n")
    if ok and vpn_interface ~= old_iface and vpn_interface ~= "" then
        util.log_info("config", "VPN interface changed: " .. (old_iface or "?") .. " → " .. vpn_interface)
        -- 更新 UCI network.mynet.device
        util.exec("uci set network.mynet.device='" .. vpn_interface .. "' 2>/dev/null")
        util.exec("uci commit network 2>/dev/null")
        -- 更新 firewall zone device（遍历查找 mynet zone）
        for i = 0, 15 do
            local name = util.trim(util.exec(
                string.format("uci get firewall.@zone[%d].name 2>/dev/null", i)) or "")
            if name == "" then break end
            if name == "mynet" then
                util.exec(string.format(
                    "uci set firewall.@zone[%d].device='%s' 2>/dev/null", i, vpn_interface))
                util.exec("uci commit firewall 2>/dev/null")
                break
            end
        end
    end
    return ok, err
end

-- ─────────────────────────────────────────────────────────────
-- Monitor Config (config.json → monitor 字段 + mynetd.json)
-- 管理 mynetd 守护进程的运行参数
-- ─────────────────────────────────────────────────────────────

-- 加载 mynetd 监控配置
-- 返回: { mynetd_bin_path, heartbeat_interval, log_level, auto_refresh_nodes, daemon_mode }
function M.load_monitor_config()
    local data = util.load_json_file(util.CONFIG_FILE)
    local m    = data and data.monitor or {}
    return {
        mynetd_bin_path    = m.mynetd_bin_path    or M.DEFAULT_MYNETD_BIN,
        heartbeat_interval = m.heartbeat_interval or 300,
        log_level          = m.log_level          or "info",
        auto_refresh_nodes = m.auto_refresh_nodes == true,
        daemon_mode        = m.daemon_mode ~= false,
    }
end

-- 保存 mynetd 监控配置
-- 同时更新 config.json monitor 字段和 mynetd.json（供 mynetd 二进制读取）
function M.save_monitor_config(mynetd_bin_path, heartbeat_interval, log_level, auto_refresh_nodes, daemon_mode)
    local data = util.load_json_file(util.CONFIG_FILE) or {}
    local interval = tonumber(heartbeat_interval) or 300
    if interval < 10  then interval = 10  end
    if interval > 3600 then interval = 3600 end
    data.monitor = {
        mynetd_bin_path    = mynetd_bin_path or M.DEFAULT_MYNETD_BIN,
        heartbeat_interval = interval,
        log_level          = log_level or "info",
        auto_refresh_nodes = auto_refresh_nodes == true,
        daemon_mode        = daemon_mode ~= false,
    }
    -- 同步写入 mynetd.json，供 mynetd 二进制直接读取
    local mynetd_cfg = {
        mynet_conf_path    = util.CONF_DIR .. "/mynet.conf",
        log_dir            = util.MYNET_HOME .. "/var/logs",
        pid_file           = "/var/run/mynetd.pid",
        heartbeat_interval = interval,
        log_level          = log_level or "info",
        daemon_mode        = daemon_mode ~= false,
        auto_refresh_nodes = auto_refresh_nodes == true,
    }
    util.save_json_file(M.MYNETD_JSON, mynetd_cfg)
    return util.save_json_file(util.CONFIG_FILE, data)
end

-- 获取当前有效的 mynetd 可执行文件路径
function M.get_mynetd_bin()
    return M.load_monitor_config().mynetd_bin_path
end

return M
