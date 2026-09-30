-- mynet/system.lua — 系统环境检测、路由器信息收集、GNB数据解析
-- 从 controller/mynet.lua 拆出，供 controller 调用。

local M = {}
local util = require("luci.model.mynet.util")

-- ─────────────────────────────────────────────────────────────
-- 私有辅助
-- ─────────────────────────────────────────────────────────────

local function uci_get(key)
    return util.trim(util.exec("uci get " .. key .. " 2>/dev/null") or "")
end

local function cmd_exists(name)
    local _, c = util.exec_status("command -v " .. name .. " 2>/dev/null")
    return c == 0
end

-- 本项目仅服务 OpenWrt
function M.is_openwrt() return true end

-- ─────────────────────────────────────────────────────────────
-- mynetd 运行状态  →  (status_str, pid_str)
-- ─────────────────────────────────────────────────────────────

function M.get_mynetd_status()
    local pid_str = util.trim(util.read_file("/var/run/mynetd.pid") or "")
    if pid_str ~= "" then
        local _, code = util.exec_status("kill -0 " .. pid_str .. " 2>/dev/null")
        if code == 0 then return "running", pid_str end
    end
    local out, code = util.exec_status("pgrep -x mynetd 2>/dev/null")
    if code == 0 and util.trim(out or "") ~= "" then
        return "running", util.trim(out):match("^(%d+)")
    end
    return "stopped", nil
end

-- ─────────────────────────────────────────────────────────────
-- 依赖检查
-- ─────────────────────────────────────────────────────────────

-- 检查 kmod-tun 是否加载（/dev/net/tun 或 lsmod）
local function check_kmod_tun()
    local _, c = util.exec_status("test -c /dev/net/tun 2>/dev/null")
    if c == 0 then return true, nil end
    local out = util.trim(util.exec("lsmod 2>/dev/null | grep -w tun") or "")
    if out ~= "" then return true, nil end
    return false, "kmod-tun not loaded (/dev/net/tun missing)"
end

-- 检查 SSL/TLS 支持（curl https）
local function check_ssl()
    if not cmd_exists("curl") then
        return false, "curl not found"
    end
    -- curl --version 检查 https 协议支持
    local out = util.trim(util.exec("curl --version 2>/dev/null | head -2") or "")
    if out:lower():find("https") or out:lower():find("tls") or out:lower():find("ssl") then
        return true, nil
    end
    return false, "curl lacks HTTPS/SSL support (install curl with libopenssl)"
end

-- 检查 gnb_ctl 是否存在
local function check_gnb_ctl(node_id)
    local ctl = util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl"
    if util.file_exists(ctl) then return true, nil, ctl end
    return false, "gnb_ctl not found at " .. ctl, ctl
end

-- 检查 gnb 主进程是否在运行（通过 pid 文件或 pgrep）
local function check_gnb_process(node_id)
    if not node_id or util.int_str(node_id) == "0" then
        return false, "no node_id configured"
    end
    local pid_file = util.GNB_DRIVER_ROOT .. "/conf/" .. util.int_str(node_id) .. "/gnb.pid"
    local pid_str = util.trim(util.read_file(pid_file) or "")
    if pid_str ~= "" then
        local _, code = util.exec_status("kill -0 " .. pid_str .. " 2>/dev/null")
        if code == 0 then return true, nil, pid_str end
    end
    local out, code = util.exec_status("pgrep gnb 2>/dev/null")
    if code == 0 and util.trim(out or "") ~= "" then
        return true, nil, util.trim(out):match("^(%d+)")
    end
    return false, "gnb process not running"
end

-- 汇总依赖检查结果
function M.check_deps(node_id)
    local deps = {}

    -- curl
    local has_curl = cmd_exists("curl")
    table.insert(deps, {
        name = "curl",
        ok   = has_curl,
        hint = has_curl and nil or "opkg install curl",
    })

    -- SSL
    local ssl_ok, ssl_err = check_ssl()
    table.insert(deps, {
        name = "SSL/HTTPS",
        ok   = ssl_ok,
        hint = ssl_ok and nil or ((ssl_err or "check failed") .. " — opkg install libopenssl"),
    })

    -- kmod-tun
    local tun_ok, tun_err = check_kmod_tun()
    table.insert(deps, {
        name = "kmod-tun",
        ok   = tun_ok,
        hint = tun_ok and nil or ((tun_err or "check failed") .. " — opkg install kmod-tun"),
    })

    -- ip command
    local ip_ok = cmd_exists("ip")
    table.insert(deps, {
        name = "ip",
        ok   = ip_ok,
        hint = ip_ok and nil or "opkg install ip-full",
    })

    -- gnb_ctl
    local gnb_ok, gnb_err = check_gnb_ctl(node_id)
    table.insert(deps, {
        name = "gnb_ctl",
        ok   = gnb_ok,
        hint = gnb_ok and nil or ((gnb_err or "not found") .. " — run mynet install or download gnb driver"),
    })

    -- gnb process
    local gnbp_ok, gnbp_err, gnbp_pid = check_gnb_process(node_id)
    table.insert(deps, {
        name = "gnb process",
        ok   = gnbp_ok,
        hint = gnbp_ok and ("pid=" .. (gnbp_pid or "?")) or gnbp_err,
        pid  = gnbp_pid,
    })

    -- 总体状态
    local all_ok = true
    local critical_ok = true  -- 只统计非 gnb-process 的 critical 依赖
    for _, d in ipairs(deps) do
        if not d.ok then
            all_ok = false
            if d.name ~= "gnb process" then critical_ok = false end
        end
    end
    return deps, all_ok, critical_ok
end

-- ─────────────────────────────────────────────────────────────
-- 防火墙状态（OpenWrt UCI）
-- ─────────────────────────────────────────────────────────────

-- 查找 mynet zone 的 UCI 索引（返回 nil 若不存在）
local function find_mynet_zone_index()
    for i = 0, 15 do
        local name = uci_get(string.format("firewall.@zone[%d].name", i))
        if name == "" then break end
        if name == "mynet" then return i end
    end
    return nil
end

function M.collect_firewall_info()
    local fw = {
        mynet_zone_exists = false,
        mynet_masq        = false,
        forwards          = {},  -- { src, dest, action }
    }

    local zi = find_mynet_zone_index()
    if not zi then return fw end

    fw.mynet_zone_exists = true
    fw.mynet_masq = (uci_get(string.format("firewall.@zone[%d].masq", zi)) == "1")

    -- 收集 forwarding 规则
    local pairs_checked = { lan_mynet=false, mynet_lan=false, wan_mynet=false, mynet_wan=false }
    for i = 0, 30 do
        local src  = uci_get(string.format("firewall.@forwarding[%d].src",  i))
        local dest = uci_get(string.format("firewall.@forwarding[%d].dest", i))
        if src == "" then break end
        local key = src .. "_" .. dest
        if (src == "lan" or src == "wan" or src == "mynet") and
           (dest == "lan" or dest == "wan" or dest == "mynet") then
            -- 避免重复
            if not pairs_checked[key] then
                pairs_checked[key] = true
                table.insert(fw.forwards, { src = src, dest = dest, action = "ACCEPT" })
            end
        end
    end

    return fw
end

-- ─────────────────────────────────────────────────────────────
-- 防火墙自动管理
-- ─────────────────────────────────────────────────────────────

-- firewall.mynet 脚本路径（ipk 安装时已部署到 scripts/）
local function find_fw_script()
    if util.file_exists(util.FIREWALL_SCRIPT) then return util.FIREWALL_SCRIPT end
    return nil
end

--- 确保防火墙 zone/forwarding/include 已创建（幂等）
-- @return ok:boolean, msg:string
function M.ensure_firewall_zone()
    -- 已存在则跳过
    local zi = find_mynet_zone_index()
    if zi then return true, "zone already exists" end

    local fw_script = find_fw_script()
    if not fw_script then
        return false, "firewall.mynet script not found"
    end

    -- install 会自动创建 network.mynet + zone + forwarding（不需要 --interface）
    local env = "MYNET_HOME=" .. util.MYNET_HOME
    local out, code = util.exec_status(
        env .. " sh " .. fw_script .. " install 2>&1")
    out = util.trim(out or "")
    if code == 0 then
        return true, out ~= "" and out or "firewall zone created"
    else
        return false, out ~= "" and out or "firewall install failed (code " .. tostring(code) .. ")"
    end
end

--- 删除防火墙 zone/forwarding/include（幂等）
-- @return ok:boolean, msg:string
function M.remove_firewall_zone()
    local zi = find_mynet_zone_index()
    if not zi then return true, "zone already removed" end

    local fw_script = find_fw_script()
    if fw_script then
        local env = "MYNET_HOME=" .. util.MYNET_HOME
        util.exec_status(env .. " sh " .. fw_script .. " uninstall --interface gnb_tun 2>&1")
    end

    -- 验证 shell 脚本是否成功：若 zone 仍存在则用 UCI 直接清理
    zi = find_mynet_zone_index()
    if not zi then return true, "firewall zone removed" end

    -- Fallback: 直接 UCI 删除（修复 shell 脚本对单值 network 字段的兼容问题）
    -- 1. 删除 forwarding 规则（倒序避免索引偏移）
    for i = 30, 0, -1 do
        local src  = uci_get(string.format("firewall.@forwarding[%d].src",  i))
        local dest = uci_get(string.format("firewall.@forwarding[%d].dest", i))
        if src == "mynet" or dest == "mynet" then
            util.exec(string.format("uci delete firewall.@forwarding[%d] 2>/dev/null", i))
        end
    end
    -- 2. 删除 zone
    zi = find_mynet_zone_index()
    if zi then
        util.exec(string.format("uci delete firewall.@zone[%d] 2>/dev/null", zi))
    end
    -- 3. 删除 include
    util.exec("uci delete firewall.mynet_include 2>/dev/null")
    -- 4. 提交并重载
    util.exec("uci commit firewall 2>/dev/null")
    util.exec("/etc/init.d/firewall reload 2>/dev/null")

    -- 最终验证
    zi = find_mynet_zone_index()
    if not zi then
        return true, "firewall zone removed (UCI fallback)"
    end
    return false, "failed to remove zone"
end

--- 重新应用 masq 规则（幂等，需 VPN 接口已启动）
-- @return ok:boolean, msg:string
function M.apply_firewall_masq()
    local fw_script = find_fw_script()
    if not fw_script then
        return false, "firewall.mynet script not found"
    end

    local env = "MYNET_HOME=" .. util.MYNET_HOME
    local out, code = util.exec_status(
        env .. " sh " .. fw_script .. " start --interface gnb_tun 2>&1")
    out = util.trim(out or "")
    if code == 0 then
        return true, out ~= "" and out or "masq rules applied"
    else
        return false, out ~= "" and out or "masq apply failed (code " .. tostring(code) .. ")"
    end
end

-- ─────────────────────────────────────────────────────────────
-- 路由器核心信息收集
-- ─────────────────────────────────────────────────────────────

function M.collect_router_info(node_id)
    local info = { openwrt = true }

    -- ── 路由模式 ──
    local wan_iface = uci_get("network.wan.device")
    if wan_iface == "" then wan_iface = uci_get("network.wan.ifname") end
    info.wan_iface = wan_iface ~= "" and wan_iface or nil
    info.routing_mode = (wan_iface ~= "") and "主路由 (Gateway)" or "旁路由 (Bypass)"

    -- ── 一次 ip addr 获取所有接口 IP（替代多次 ip addr show X）──
    local addr_out = util.exec("ip addr 2>/dev/null") or ""
    local iface_ips = {}   -- { [iface] = "x.x.x.x/prefix" }
    local cur_iface = nil
    for line in (addr_out .. "\n"):gmatch("([^\n]+)\n") do
        local iface = line:match("^%d+: ([^:@ ]+)")
        if iface then
            cur_iface = iface
        elseif cur_iface then
            local ip = line:match("inet (%S+)")
            if ip and not iface_ips[cur_iface] then
                iface_ips[cur_iface] = ip
            end
        end
    end

    -- WAN IP
    if info.wan_iface then
        info.wan_ip = iface_ips[info.wan_iface]
    end

    -- LAN IP（优先 UCI，回落 ip addr）
    info.lan_ip = uci_get("network.lan.ipaddr")
    if info.lan_ip == "" then
        local cidr = iface_ips["br-lan"]
        info.lan_ip = cidr and cidr:match("([^/]+)") or nil
    end
    if info.lan_ip == "" then info.lan_ip = nil end

    -- VPN 接口（优先 UCI，回落从 iface_ips 找 gnb_tun*）
    info.vpn_iface = uci_get("network.mynet.device")
    if info.vpn_iface == "" then info.vpn_iface = uci_get("network.mynet.ifname") end
    if info.vpn_iface == "" then
        for iface in pairs(iface_ips) do
            if iface:match("^gnb_tun") then info.vpn_iface = iface; break end
        end
    end
    if info.vpn_iface == "" then info.vpn_iface = nil end

    -- VPN IP
    if info.vpn_iface then
        info.vpn_ip = iface_ips[info.vpn_iface]
    end

    -- ── 一次 ip route show 获取网关 + 路由计数（替代两条独立调用）──
    local routes_out = util.exec("ip route show 2>/dev/null") or ""
    local total, vpn_routes = 0, 0
    local vpn_dev = info.vpn_iface or "__none__"
    for line in (routes_out .. "\n"):gmatch("([^\n]+)\n") do
        if line:match("%S") then
            total = total + 1
            if line:match("^default ") and not info.gateway then
                info.gateway = line:match("via (%S+)")
            end
            if line:find("dev mynet") or line:find("dev wg0")
               or line:find("dev " .. vpn_dev) then
                vpn_routes = vpn_routes + 1
            end
        end
    end
    info.total_routes = total
    info.vpn_routes   = vpn_routes

    -- ── 防火墙 ──
    info.firewall = M.collect_firewall_info()

    -- ── 依赖检查 ──
    info.deps, info.deps_all_ok, info.deps_critical_ok = M.check_deps(node_id)

    -- ── gnb 进程运行状态 ──
    local gnbp_ok, gnbp_err, gnbp_pid = check_gnb_process(node_id)
    info.gnb_running = gnbp_ok
    info.gnb_pid     = gnbp_pid
    info.gnb_err     = gnbp_err

    return info
end

-- ─────────────────────────────────────────────────────────────
-- 解析 gnb_ctl 输出
-- ─────────────────────────────────────────────────────────────

function M.parse_gnb_nodes(output)
    local nodes = {}
    local cur = nil
    local in_avail = false

    for raw_line in (output .. "\n"):gmatch("([^\n]*)\n") do
        local line = raw_line:match("^%s*(.-)%s*$")
        if line ~= "" then
            local node_id_str = line:match("^node (%d+)$")
            if node_id_str then
                if cur then table.insert(nodes, cur) end
                cur = {
                    node_id      = tonumber(node_id_str),
                    tun_ipv4     = "", tun_ipv6 = "",
                    wan_ipv4     = "", wan_ipv6 = "",
                    detect_count = 0,
                    ipv4_latency = 0,  ipv6_latency = 0,
                    conn_status  = "unknown",
                    in_bytes     = 0,  out_bytes = 0,
                    avail_addrs  = 0,  avail_ips = {},
                    is_local     = false,
                }
                in_avail = false
            elseif cur then
                if line == "available_address4:" then
                    in_avail = true
                elseif in_avail then
                    local ip_port = line:match("^address (.+)$")
                    if ip_port then
                        cur.avail_addrs = cur.avail_addrs + 1
                        local ip = ip_port:match("^([%d%.]+)")
                        if ip then table.insert(cur.avail_ips, ip) end
                    elseif line:match(":$") then
                        in_avail = false
                    end
                else
                    local k, v = line:match("^(%S+) (.+)$")
                    if k and v then
                        if     k == "tun_ipv4"                then cur.tun_ipv4     = v
                        elseif k == "tun_ipv6"                then cur.tun_ipv6     = v
                        elseif k == "wan_ipv4"                then cur.wan_ipv4     = v
                        elseif k == "wan_ipv6"                then cur.wan_ipv6     = v
                        elseif k == "detect_count"            then cur.detect_count = tonumber(v) or 0
                        elseif k == "addr4_ping_latency_usec" then cur.ipv4_latency = tonumber(v) or 0
                        elseif k == "addr6_ping_latency_usec" then cur.ipv6_latency = tonumber(v) or 0
                        elseif k == "in" then
                            -- format: "in  2350576454 (2.189G bytes)" — take raw count before '('
                            cur.in_bytes = tonumber(v:match("^%s*(%d+)")) or 0
                        elseif k == "out" then
                            cur.out_bytes = tonumber(v:match("^%s*(%d+)")) or 0
                        elseif k == "ipv4" then
                            if     v:find("Direct Point to Point") then cur.conn_status = "Direct"
                            elseif v:find("InDirect")              then cur.conn_status = "InDirect"
                            elseif v:find("Local node")            then cur.conn_status = "Local"; cur.is_local = true
                            end
                        end
                    end
                end
            end
        end
    end
    if cur then table.insert(nodes, cur) end
    return nodes
end

-- ─────────────────────────────────────────────────────────────
-- 上报命令执行结果到服务端
-- PATCH /api/v2/monitor/commands/{id}/result
-- 与心跳同样使用 HMAC-SHA256 Node-Sig 认证
-- ─────────────────────────────────────────────────────────────
function M._report_command_result(cmd_id, nid_str, pub_hex, ok, message)
    if not cmd_id then return end
    -- E-4：结果回 mynetd（纯执行器），由 mynetd 经 WS 上报——不再直连 back
    local body = string.format(
        '{"id":"%s","ok":%s,"message":%s,"source":"lua"}',
        tostring(cmd_id),
        ok and "true" or "false",
        util.json_encode(message or "") or '""')
    local cmd = string.format(
        "curl -s -m 3 -X POST -H 'Content-Type: application/json'"
        .. " --data '%s' 'http://127.0.0.1:8096/api/v1/command-result' >/dev/null 2>&1",
        body:gsub("'", "'\\''"))
    os.execute(cmd)
end

-- ─────────────────────────────────────────────────────────────
-- 处理心跳响应中的服务端下发命令
-- commands: [{ id, action, params }]
-- action 类型: config.refresh / service.restart / gnb.restart /
--              gnb.start / gnb.stop / route.refresh /
--              firewall.refresh / client.report
-- nid_str/pub_hex: 用于命令结果回调签名（可选；缺省则不回调）
-- ─────────────────────────────────────────────────────────────
-- E-4（目标态）：Lua 职责收窄为**纯执行器**。
--   mynetd 负责心跳组装 + WS 上报；Lua 只执行命令并把结果**回 mynetd**，
--   由 mynetd 经 WS 上报——不再自己 submit_heartbeat / _report 到 back。
--   当前仍保留 submit_heartbeat 作兼容；迁移完成后删除。
function M._handle_heartbeat_commands(commands, node_id, nid_str, pub_hex)
    if type(commands) ~= "table" or #commands == 0 then return end
    local node_m = require("luci.model.mynet.node")
    local cfg_m  = require("luci.model.mynet.config")
    nid_str = nid_str or util.int_str(node_id)
    for _, cmd in ipairs(commands) do
        local action = cmd.action or ""
        util.log_info("heartbeat", "server command: " .. action
            .. (cmd.params and cmd.params.reason and (" reason=" .. cmd.params.reason) or ""))

        local ok, msg = false, ""

        if action == "config.refresh" then
            local r, err = node_m.refresh_configs_bundle(node_id)
            ok = (r ~= nil) and not err
            msg = err or "config refreshed"
        elseif action == "service.restart" then
            local r, err = node_m.restart_vpn()
            ok = (r == true) or (r ~= nil and not err)
            msg = err or "service restarted"
        elseif action == "gnb.restart" then
            local r, err = node_m.restart_gnb(node_id)
            ok = (r == true) or (r ~= nil and not err)
            msg = err or "gnb restarted"
        elseif action == "gnb.start" then
            local r, err = node_m.start_gnb(node_id)
            ok = (r == true) or (r ~= nil and not err)
            msg = err or "gnb started"
        elseif action == "gnb.stop" then
            local r, err = node_m.stop_gnb(node_id)
            ok = (r == true) or (r ~= nil and not err)
            msg = err or "gnb stopped"
        elseif action == "route.refresh" then
            local r, err = node_m.refresh_single_config(node_id, "route")
            ok = (r ~= nil) and not err
            msg = err or "route refreshed"
        elseif action == "firewall.refresh" then
            -- 调用 firewall.mynet 脚本重新应用 NAT/转发规则
            if not util.file_exists(util.FIREWALL_SCRIPT) then
                ok, msg = false, "firewall.mynet script not found"
            else
                local iface = cfg_m.get_vpn_interface()
                local env = "MYNET_HOME=" .. util.MYNET_HOME
                local out, code = util.exec_status(
                    env .. " sh " .. util.FIREWALL_SCRIPT
                    .. " start --interface " .. iface .. " 2>&1")
                out = util.trim(out or "")
                ok = (code == 0)
                msg = ok and (out ~= "" and out or "firewall refreshed")
                       or (out ~= "" and out
                           or ("firewall refresh failed (code " .. tostring(code) .. ")"))
            end
        elseif action == "client.report" then
            -- 立即再上报一次（避免递归：仅日志记录，不再次调用）
            util.log_info("heartbeat", "client.report requested, will report on next cycle")
            ok, msg = true, "will report on next cycle"
        else
            util.log_warn("heartbeat: unknown command action: " .. action)
            ok, msg = false, "unknown action: " .. action
        end

        -- 回报执行结果（best-effort，需要签名密钥）
        if cmd.id and pub_hex and pub_hex ~= "" then
            M._report_command_result(cmd.id, nid_str, pub_hex, ok, msg)
        end
    end
end

-- ─────────────────────────────────────────────────────────────
-- Heartbeat 指标采集（对齐 client heartbeat metrics）
-- 返回: { cpu_percent, memory_used, memory_total, disk_used,
--         disk_total, uptime, vpn_status, peer_count,
--         rx_bytes, tx_bytes, load_avg }
-- ─────────────────────────────────────────────────────────────
function M.collect_metrics(node_id)
    local m = {}

    -- CPU — 从 /proc/stat 取瞬时 idle 占比（近似值）
    local stat = util.read_file("/proc/stat")
    if stat then
        local user, nice, system, idle = stat:match("^cpu%s+(%d+)%s+(%d+)%s+(%d+)%s+(%d+)")
        if user then
            local total = (tonumber(user) or 0) + (tonumber(nice) or 0)
                        + (tonumber(system) or 0) + (tonumber(idle) or 0)
            if total > 0 then
                m.cpu_percent = math.floor(((total - (tonumber(idle) or 0)) / total) * 100 + 0.5)
            end
        end
    end
    m.cpu_percent = m.cpu_percent or 0

    -- Memory — /proc/meminfo
    local meminfo = util.read_file("/proc/meminfo") or ""
    local mem_total = tonumber(meminfo:match("MemTotal:%s*(%d+)"))    or 0
    local mem_free  = tonumber(meminfo:match("MemFree:%s*(%d+)"))     or 0
    local mem_buf   = tonumber(meminfo:match("Buffers:%s*(%d+)"))     or 0
    local mem_cache = tonumber(meminfo:match("Cached:%s*(%d+)"))      or 0
    m.memory_total = mem_total * 1024   -- kB → bytes
    m.memory_used  = (mem_total - mem_free - mem_buf - mem_cache) * 1024

    -- Disk — df /
    local df_out = util.trim(util.exec("df / 2>/dev/null | tail -1") or "")
    local _, dsize, dused = df_out:match("(%S+)%s+(%d+)%s+(%d+)")
    m.disk_total = (tonumber(dsize) or 0) * 1024   -- 1K blocks → bytes
    m.disk_used  = (tonumber(dused) or 0) * 1024

    -- Uptime
    local uptime_raw = util.trim(util.read_file("/proc/uptime") or "")
    m.uptime = math.floor(tonumber(uptime_raw:match("^([%d%.]+)")) or 0)

    -- Load average
    m.load_avg = util.trim(util.exec("cat /proc/loadavg 2>/dev/null") or "")

    -- VPN status
    local node_m = require("luci.model.mynet.node")
    local cfg_m  = require("luci.model.mynet.config")
    local nid = node_id or cfg_m.get_node_id()
    if nid and nid ~= 0 and node_m.gnb_is_running(nid) then
        m.vpn_status = "running"
    else
        m.vpn_status = node_m.get_vpn_service_status()
    end

    -- Peer count — gnb_ctl 输出中的节点数
    local iface = cfg_m.get_vpn_interface()
    local peer_count = 0
    if nid and util.int_str(nid) ~= "0" then
        local gnb_ctl = util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl"
        local gnb_map = util.GNB_CONF_DIR .. "/" .. util.int_str(nid) .. "/gnb.map"
        if util.file_exists(gnb_ctl) and util.file_exists(gnb_map) then
            local out = util.exec("cd '" .. util.GNB_DRIVER_ROOT
                .. "' && ./bin/gnb_ctl -s -b 'conf/" .. util.int_str(nid)
                .. "/gnb.map' 2>/dev/null | grep -c '^node '") or "0"
            peer_count = tonumber(util.trim(out)) or 0
        end
    end
    m.peer_count = peer_count

    -- Network I/O
    local rx = util.trim(util.exec("cat /sys/class/net/" .. iface .. "/statistics/rx_bytes 2>/dev/null") or "0")
    local tx = util.trim(util.exec("cat /sys/class/net/" .. iface .. "/statistics/tx_bytes 2>/dev/null") or "0")
    m.rx_bytes = tonumber(rx) or 0
    m.tx_bytes = tonumber(tx) or 0

    return m
end

-- ─────────────────────────────────────────────────────────────
-- 提交 heartbeat 到服务端（POST /api/v2/monitor/heartbeat）
-- 使用 HMAC-SHA256 Node-Sig 认证（与 daemon 一致）
-- 返回: (response_table, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.submit_heartbeat(node_id)
    -- E-4：mynetd 在跑时 Lua 不再自报心跳（心跳归 mynetd）
    if util.file_exists("/var/run/mynetd.pid") or os.execute("pidof mynetd >/dev/null 2>&1") == 0 then
        return { success = true, skipped = "mynetd-active" }, nil
    end
    local cfg_m  = require("luci.model.mynet.config")
    local api_m  = require("luci.model.mynet.api")
    local node_m = require("luci.model.mynet.node")

    local nid_str = util.int_str(node_id)
    local ts = os.time()

    -- 读取公钥用于 HMAC 签名
    local pub_path = string.format("%s/%s/security/%s.public",
        util.GNB_CONF_DIR, nid_str, nid_str)
    local pub_hex = util.trim(util.read_file(pub_path) or "")
    if pub_hex == "" then
        return nil, "public key not found: " .. pub_path
    end

    local metrics = M.collect_metrics(node_id)

    -- v2 body: status + uptime + vpn_interfaces（复数数组）
    local iface = cfg_m.get_vpn_interface()
    -- 后端验证要求 status 为 "up"/"down"，而非 "running"/"stopped"
    local iface_status = (metrics.vpn_status == "running") and "up" or "down"
    local vpn_iface_entry = {
        type   = "gnb",
        ifname = iface,
        status = iface_status,
        ip     = "",
    }
    -- 获取 VPN IP
    local ip_out = util.exec("ip -4 addr show '" .. iface .. "' 2>/dev/null")
    if ip_out then
        vpn_iface_entry.ip = ip_out:match("inet%s+([%d%.]+)") or ""
    end

    local node_status = (metrics.vpn_status == "running") and "online" or "offline"
    local payload = {
        status          = node_status,
        uptime          = metrics.uptime or 0,
        connection_count = metrics.peer_count or 0,
        cpu_usage       = metrics.cpu_percent or 0,
        memory_usage    = metrics.memory_used and metrics.memory_total and metrics.memory_total > 0
                          and ((metrics.memory_used / metrics.memory_total) * 100) or 0,
        disk_usage      = metrics.disk_used and metrics.disk_total and metrics.disk_total > 0
                          and ((metrics.disk_used / metrics.disk_total) * 100) or 0,
        node_id         = nid_str,
        timestamp       = ts,
        vpn_interfaces  = { vpn_iface_entry },
    }

    local body_json = util.json_encode(payload) or "{}"

    -- HMAC-SHA256 签名
    local sign_path = "/api/v2/monitor/heartbeat"
    local sign_msg  = string.format("POST|%s|%d|%s", sign_path, ts, body_json)
    local sig_hex   = util.hmac_sha256(pub_hex, sign_msg)
    if not sig_hex then
        return nil, "HMAC signing failed"
    end
    local signature = util.hex_to_base64(sig_hex) or ""

    local data, api_err = api_m.post_heartbeat(
        cfg_m.get_api_url(), nid_str, ts, body_json, signature)
    if api_err then return nil, api_err end
    if data and data.success == false then
        return nil, data.message or "heartbeat rejected"
    end

    -- 处理服务端下发命令
    if data and data.commands then
        M._handle_heartbeat_commands(data.commands, node_id, nid_str, pub_hex)
    end

    return data, nil
end

-- ─────────────────────────────────────────────────────────────
-- Daemon 心跳（POST /api/v2/monitor/heartbeat）
-- 认证：节点公钥 HMAC-SHA256，无 JWT token，永不过期
-- 可从 cron 命令行调用：lua -e 'require("luci.model.mynet.system").run_daemon_heartbeat()'
-- 返回: (true, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.run_daemon_heartbeat()
    -- E-4：mynetd 常驻时本入口退化为 no-op（cron 兼容保留）
    if util.file_exists("/var/run/mynetd.pid") or os.execute("pidof mynetd >/dev/null 2>&1") == 0 then
        return true, "skipped: mynetd-active"
    end
    local cfg_m = require("luci.model.mynet.config")

    -- 读取 NODE_ID
    local node_id = cfg_m.get_node_id()
    if not node_id or node_id == "0" then
        return nil, "NODE_ID not configured"
    end
    local nid_str = node_id

    -- 读取公钥（32 字节 = 64 hex，HMAC key）
    local pub_path = string.format("%s/%s/security/%s.public",
        util.GNB_CONF_DIR, nid_str, nid_str)
    local pub_hex = util.trim(util.read_file(pub_path) or "")
    if pub_hex == "" then
        return nil, "public key not found: " .. pub_path
    end
    if #pub_hex ~= 64 then
        return nil, string.format("public key length wrong (%d chars, expected 64)", #pub_hex)
    end

    -- 内联最小采集（不依赖 node.lua，避免在 cron CLI 上下文加载全量模块链）
    local ts = os.time()

    -- Uptime
    local uptime_raw = util.trim(util.read_file("/proc/uptime") or "")
    local uptime = math.floor(tonumber(uptime_raw:match("^([%d%.]+)")) or 0)

    -- 内存使用率（/proc/meminfo）
    local mem_pct = 0
    local meminfo = util.read_file("/proc/meminfo") or ""
    local mem_total = tonumber(meminfo:match("MemTotal:%s*(%d+)"))    or 0
    local mem_avail = tonumber(meminfo:match("MemAvailable:%s*(%d+)")) or 0
    if mem_total > 0 then
        mem_pct = ((mem_total - mem_avail) / mem_total) * 100
    end

    -- VPN 接口信息
    local iface = cfg_m.get_vpn_interface()
    local vpn_status = "down"
    local vpn_ip     = ""
    local chk = util.exec("ip link show '" .. iface .. "' 2>/dev/null | head -1")
    if chk and chk ~= "" then
        vpn_status = "up"
        local ip_out = util.exec("ip -4 addr show '" .. iface .. "' 2>/dev/null")
        if ip_out then
            vpn_ip = ip_out:match("inet%s+([%d%.]+)") or ""
        end
    end

    -- 对端数（gnb_ctl map，失败则为 0）
    local peer_count = 0
    local gnb_ctl  = util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl"
    local gnb_map  = util.GNB_CONF_DIR .. "/" .. nid_str .. "/gnb.map"
    if util.file_exists(gnb_ctl) and util.file_exists(gnb_map) then
        local out = util.exec(
            "cd '" .. util.GNB_DRIVER_ROOT
            .. "' && ./bin/gnb_ctl -s -b 'conf/" .. nid_str
            .. "/gnb.map' 2>/dev/null | grep -c '^node '") or "0"
        peer_count = tonumber(util.trim(out)) or 0
    end

    -- v2 body 格式：status + uptime + vpn_interfaces（复数数组）
    local vpn_json = string.format(
        '{"type":"gnb","ifname":"%s","status":"%s","ip":"%s"}',
        iface, vpn_status, vpn_ip)
    local node_status = (vpn_status == "up") and "online" or "offline"
    local body = string.format(
        '{"status":"%s","uptime":%d,"connection_count":%d,"cpu_usage":0,"disk_usage":0,"memory_usage":%.6f,"node_id":%s,"timestamp":%d,"vpn_interfaces":[%s]}',
        node_status, uptime, peer_count, mem_pct, nid_str, ts, vpn_json)

    -- HMAC-SHA256 签名（与 Go: h := hmac.New(sha256.New, publicKey)）
    local sign_path = "/api/v2/monitor/heartbeat"
    local sign_msg  = string.format("POST|%s|%d|%s", sign_path, ts, body)
    local sig_hex   = util.hmac_sha256(pub_hex, sign_msg)
    if not sig_hex then
        return nil, "HMAC signing failed"
    end
    local sig_b64 = util.hex_to_base64(sig_hex)
    if not sig_b64 then
        return nil, "base64 encode failed"
    end

    -- POST（无 Authorization，只有 X-Node-* 头；直接调 curl 不经 api 模块）
    local api_base = cfg_m.get_api_url():match("^(https?://[^/]+)") or "https://api.mynet.club"
    local full_url = (api_base .. "/api/v2/monitor/heartbeat"):gsub("'", "'\\''")
    local safe_body = body:gsub("'", "'\\''")
    local cmd = string.format(
        "curl -s -m 25 -X POST"
        .. " -H 'Content-Type: application/json'"
        .. " -H 'X-Node-Id: %s'"
        .. " -H 'X-Timestamp: %d'"
        .. " -H 'X-Node-Signature: %s'"
        .. " --data '%s'"
        .. " -w '\\n__STATUS:%%{http_code}'"
        .. " '%s' 2>/dev/null",
        nid_str, ts, sig_b64, safe_body, full_url)
    local raw = util.exec(cmd)
    if not raw then return nil, "curl execution failed" end
    local status = tonumber(raw:match("__STATUS:(%d+)") or "0")
    local resp   = raw:gsub("\n?__STATUS:%d+%s*$", "")
    if status ~= 200 then
        return nil, string.format("HTTP %d: %s", status, resp:sub(1, 200))
    end
    if resp:find('"success":false') then
        return nil, "server rejected: " .. resp:sub(1, 200)
    end

    -- 处理服务端下发命令（config.refresh / service.restart 等）
    local resp_data = util.json_decode(resp)
    if resp_data and resp_data.commands then
        M._handle_heartbeat_commands(resp_data.commands, node_id, nid_str, pub_hex)
    end

    return true, nil
end

-- ─────────────────────────────────────────────────────────────
-- 健康检查聚合（Dashboard 用）
-- 返回 issues 数组: { level, title, detail, action_label, action_api }
-- level: "error" | "warn" | "ok"
-- ─────────────────────────────────────────────────────────────
function M.run_health_check(node_id, vpn_status, fw_info, deps)
    local issues = {}

    -- 1. 关键依赖缺失
    if deps then
        for _, d in ipairs(deps) do
            if not d.ok and d.name ~= "gnb process" then
                table.insert(issues, {
                    level  = "error",
                    title  = d.name .. " missing",
                    detail = d.hint or "",
                })
            end
        end
    end

    -- 2. 防火墙区域未配置
    if not fw_info or not fw_info.mynet_zone_exists then
        table.insert(issues, {
            level        = "error",
            title        = "Firewall zone not configured",
            detail       = "VPN traffic will not be forwarded",
            action_label = "Setup Firewall",
            action_api   = "fw_setup",
        })
    elseif not fw_info.mynet_masq then
        table.insert(issues, {
            level        = "warn",
            title        = "Masquerade disabled",
            detail       = "NAT may not work for VPN traffic",
            action_label = "Apply Masq",
            action_api   = "fw_apply_masq",
        })
    end

    -- 3. 节点未配置
    if not node_id or util.int_str(node_id) == "0" or util.int_str(node_id) == "" then
        table.insert(issues, {
            level  = "error",
            title  = "Node not configured",
            detail = "Go to Setup Wizard or GNB Standalone to configure this device",
        })
        return issues
    end

    -- 4. 配置文件完整性 + 一致性检查（仅在 node_id 已配置时）
    local node_m = require("luci.model.mynet.node")
    local pf = node_m.preflight_check(node_id)
    if not pf.ok then
        -- 分类: 一致性问题 vs 缺失问题
        local missing = {}
        local mismatch = {}
        for _, c in ipairs(pf.checks) do
            if not c.ok then
                if c.name == "node_conf_id_match" or c.name == "route_conf_id_match" then
                    table.insert(mismatch, c)
                elseif c.name ~= "kmod_tun" and c.name ~= "gnb_binary" and c.name ~= "peer_keys" then
                    table.insert(missing, c)
                end
            end
        end

        -- 配置不一致 → 引导 wizard
        if #mismatch > 0 then
            local details = {}
            for _, c in ipairs(mismatch) do
                details[#details + 1] = c.detail
            end
            table.insert(issues, {
                level       = "error",
                title       = "Config ID mismatch",
                detail      = table.concat(details, "; "),
                action_label = "Re-configure",
                action_link = "wizard",
            })
        end

        -- 配置文件缺失 → 引导 node 页
        if #missing > 0 then
            local names = {}
            for _, c in ipairs(missing) do
                names[#names + 1] = c.name
            end
            table.insert(issues, {
                level       = "error",
                title       = "Config files incomplete",
                detail      = "Missing: " .. table.concat(names, ", "),
                action_label = "Fix Config",
                action_link = "node",
                action_query = "tab=config",
            })
        end
    end

    -- 5. VPN 未运行（放在配置检查之后，配置完整才有意义）
    if vpn_status ~= "running" then
        table.insert(issues, {
            level        = "warn",
            title        = "GNB service stopped",
            detail       = "VPN tunnel is not active",
            -- U7：按钮文案全局唯一，避免与主区「Start」撞名（无障碍/自动化）
            action_label = "启动 GNB 服务",
            action_api   = "vpn_start",
        })
    end

    return issues
end

return M
