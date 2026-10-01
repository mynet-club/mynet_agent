-- mynet/proxy.lua — Proxy 分流模块管理
-- 对齐 client proxy 插件：start/stop/status/diagnose
-- 通过调用 shell 脚本实现，提供结构化 JSON 接口给 LuCI

local M    = {}
local util = require("luci.model.mynet.util")

-- ─────────────────────────────────────────────────────────────
-- 路径常量
-- ─────────────────────────────────────────────────────────────

local PROXY_SCRIPT_DIR = util.MYNET_HOME .. "/scripts/proxy"
local PROXY_SH         = PROXY_SCRIPT_DIR .. "/proxy.sh"
local ROUTE_POLICY_SH  = PROXY_SCRIPT_DIR .. "/route_policy.sh"
local PROXY_CONF_DIR   = util.CONF_DIR .. "/proxy"
local PROXY_ROLE_CONF  = PROXY_CONF_DIR .. "/proxy_role.conf"
local PROXY_STATE_FILE = util.MYNET_HOME .. "/var/proxy_state.json"

-- 参数校验常量（controller/model 共用，避免重复定义）
local VALID_MODES    = { client = true, server = true, both = true }
local VALID_REGIONS  = { domestic = true, international = true }
local VALID_POLICIES = { whitelist = true, blacklist = true }
local VALID_DNS      = { none = true, redirect = true, resolv = true, split = true }

-- 非国内 DNS 服务器列表（始终通过 proxy_peer 路由）
M.FOREIGN_DNS_SERVERS = {
    "8.8.8.8",      -- Google DNS
    "8.8.4.4",      -- Google DNS secondary
    "1.1.1.1",      -- Cloudflare DNS
    "1.0.0.1",      -- Cloudflare DNS secondary
    "9.9.9.9",      -- Quad9 DNS
    "208.67.222.222", -- OpenDNS
    "208.67.220.220", -- OpenDNS secondary
}

-- 国内 DNS 服务器（split 模式默认）
M.DOMESTIC_DNS_SERVERS = { "223.5.5.5", "119.29.29.29" }

-- ─────────────────────────────────────────────────────────────
-- 配置读写
-- ─────────────────────────────────────────────────────────────

-- 校验 proxy 参数（mode/region/proxy_policy/dns_mode）
function M.validate_params(opts)
    if opts.mode and not VALID_MODES[opts.mode] then
        return nil, "invalid mode: " .. tostring(opts.mode)
    end
    if opts.region and not VALID_REGIONS[opts.region] then
        return nil, "invalid region: " .. tostring(opts.region)
    end
    if opts.proxy_policy and not VALID_POLICIES[opts.proxy_policy] then
        return nil, "invalid proxy_policy: " .. tostring(opts.proxy_policy)
    end
    if opts.dns_mode and not VALID_DNS[opts.dns_mode] then
        return nil, "invalid dns_mode: " .. tostring(opts.dns_mode)
    end
    return true
end

-- 读取 proxy_role.conf (bash KEY=VALUE 格式)
function M.load_config()
    local defaults = {
        proxy_enabled = false,
        proxy_mode    = "client",
        node_region   = "domestic",
        proxy_policy  = "whitelist",
        dns_mode      = "none",
        dns_server    = "",
        proxy_peers   = "",
    }
    local result = util.parse_bash_conf(PROXY_ROLE_CONF, { lower_keys = true })
    if not result then return defaults end
    -- 向下兼容：旧版 non_domestic → domestic + blacklist
    local region = result.node_region or "domestic"
    local policy = result.proxy_policy or "whitelist"
    if region == "non_domestic" then
        region = "domestic"
        policy = "blacklist"
    end
    return {
        proxy_enabled        = (result.proxy_enabled == "1" or result.proxy_enabled == "true"),
        proxy_mode           = result.proxy_mode  or "client",
        node_region          = region,
        proxy_policy         = policy,
        dns_mode             = result.dns_mode    or "none",
        dns_server           = result.dns_server  or "",
        dns_domestic_server  = result.dns_domestic_server or "",
        proxy_peers          = result.proxy_peers or "",
    }
end

-- 合并更新配置（load → merge → save，避免调用方重复 load+save）
function M.update_config(partial)
    local cfg = M.load_config()
    for k, v in pairs(partial) do cfg[k] = v end
    return M.save_config(cfg)
end

-- 保存 proxy 配置
function M.save_config(opts)
    util.ensure_dir(PROXY_CONF_DIR)
    local lines = {
        '# MyNet Proxy Configuration',
        '# Updated: ' .. util.format_time(util.time_now()),
        'PROXY_ENABLED="' .. (opts.proxy_enabled and "1" or "0") .. '"',
        'PROXY_MODE="'    .. (opts.proxy_mode   or "client")    .. '"',
        'NODE_REGION="'   .. (opts.node_region  or "domestic")  .. '"',
        'PROXY_POLICY="'  .. (opts.proxy_policy or "whitelist") .. '"',
        'DNS_MODE="'      .. (opts.dns_mode     or "none")      .. '"',
        'DNS_SERVER="'    .. (opts.dns_server   or "")          .. '"',
        'DNS_DOMESTIC_SERVER="' .. (opts.dns_domestic_server or "") .. '"',
        'PROXY_PEERS="'   .. (opts.proxy_peers  or "")          .. '"',
    }
    return util.write_file(PROXY_ROLE_CONF, table.concat(lines, "\n") .. "\n")
end

-- 写入 / 更新 proxy_policy_params.env 中的 KEY="VALUE" 字段（保留其它行）
-- 用于 Lua 向 shell 注入运行参数（FOREIGN_DNS_SERVERS / DNS_DOMESTIC_SERVER 等）
local function _upsert_env(content, key, value)
    local pat = key .. '="[^"]*"'
    if content:find(pat) then
        return (content:gsub(pat, key .. '="' .. value .. '"'))
    end
    if content ~= "" and not content:match("\n$") then content = content .. "\n" end
    return content .. key .. '="' .. value .. '"\n'
end

-- ─────────────────────────────────────────────────────────────
-- 状态查询（解析 JSON 输出或 fallback 到进程检测）
-- ─────────────────────────────────────────────────────────────

function M.get_status()
    -- 尝试 proxy.sh status --json
    if util.file_exists(PROXY_SH) then
        local out = util.exec("MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
            .. " bash " .. util.shell_escape(PROXY_SH)
            .. " status --json 2>/dev/null")
        if out then
            local data = util.json_decode(out)
            if data then return data end
        end
    end

    -- Fallback: 通过系统状态检测
    -- 同时检查 ipset (fw3 / iptables 设备) 和 nftables (fw4 设备)
    local running = false
    local ipset_count = 0
    local nft_count = 0
    local route_table_id = nil

    -- 检查 ipset（fw3 + iptables 设备）
    local ipset_out = util.trim(util.exec(
        "ipset list mynet_proxy 2>/dev/null | grep 'Number of entries' | awk '{print $NF}'") or "")
    if ipset_out ~= "" then
        ipset_count = tonumber(ipset_out) or 0
        if ipset_count > 0 then running = true end
    end

    -- 检测 nftables set（fw4 设备）
    local nft_has = util.trim(util.exec(
        "nft list set inet mynet_proxy mynet_proxy 2>/dev/null | grep -c 'elements'") or "")
    if tonumber(nft_has) and tonumber(nft_has) > 0 then
        running = true
        local cnt_out = util.trim(util.exec(
            "nft list set inet mynet_proxy mynet_proxy 2>/dev/null | grep -oE '[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+' | wc -l") or "")
        nft_count = tonumber(cnt_out) or 0
    end

    -- 获取路由表 ID
    local rt_out = util.trim(util.exec(
        "grep -E '^[[:space:]]*[0-9]+[[:space:]]+mynet_proxy' /etc/iproute2/rt_tables 2>/dev/null | awk '{print $1}'") or "")
    if rt_out ~= "" then route_table_id = tonumber(rt_out) end

    -- 获取配置
    local cfg = M.load_config()

    -- 检查 ip rule 是否有 fwmark 规则
    local has_rule = false
    if route_table_id then
        local rule_out = util.trim(util.exec(
            "ip rule list 2>/dev/null | grep 'lookup mynet_proxy'") or "")
        has_rule = rule_out ~= ""
    end

    -- 计算 uptime（从 state file）
    local uptime_sec = 0
    local state_data = util.load_json_file(PROXY_STATE_FILE)
    if state_data and state_data.start_ts then
        uptime_sec = os.time() - (state_data.start_ts or os.time())
        if uptime_sec < 0 then uptime_sec = 0 end
    end

    -- peer 数量
    local peer_count = 0
    if cfg.proxy_peers ~= "" then
        for _ in cfg.proxy_peers:gmatch("[^,]+") do
            peer_count = peer_count + 1
        end
    end

    -- proxy_route.conf 中的 CIDR 条目数（已配置的规则数）
    local route_conf_count = 0
    local route_conf_path = PROXY_CONF_DIR .. "/proxy_route.conf"
    if util.file_exists(route_conf_path) then
        local cnt_out = util.trim(util.exec(
            "grep -cv '^#\\|^$' " .. util.shell_escape(route_conf_path) .. " 2>/dev/null") or "")
        route_conf_count = tonumber(cnt_out) or 0
    end

    -- 出口策略（gnb proxy.conf）：代理转发路径的声明来源
    local exit_policy = M.exit_policy_status()

    -- 历史 Lua 注入残留条数：期望恒为 0，非 0 说明 route.conf 还没交还 agent
    local legacy_route_inject = M.route_conf_legacy_count()

    -- 国外 DNS 是否已被引入 GNB 隧道（检测首个服务器作为样本）
    local foreign_dns_routed = false
    if #M.FOREIGN_DNS_SERVERS > 0 then
        local sample = M.FOREIGN_DNS_SERVERS[1]
        local rg = util.exec("ip route get " .. sample .. " 2>/dev/null | head -1") or ""
        local cfg_m_local = require("luci.model.mynet.config")
        local iface = cfg_m_local.get_vpn_interface()
        if iface and rg:find("dev " .. iface, 1, true) then
            foreign_dns_routed = true
        end
    end

    -- 匹配模式（由 proxy_policy 决定，向下兼容旧 non_domestic）
    local match_mode = (cfg.proxy_policy == "blacklist") and "inverted" or "normal"

    return {
        running         = running,
        mode            = cfg.proxy_mode,
        region          = cfg.node_region,
        proxy_policy    = cfg.proxy_policy or "whitelist",
        match_mode      = match_mode,
        dns_mode        = cfg.dns_mode,
        layers          = {
            exit_policy        = M.exit_policy_ok(exit_policy),
            policy_routing     = running,
            dns_intercept      = cfg.dns_mode ~= "none" and running,
            foreign_dns_routed = foreign_dns_routed,
        },
        stats           = {
            ipset_count        = ipset_count,
            nft_count          = nft_count,
            route_conf_count   = route_conf_count,
            route_table_id     = route_table_id,
            peer_count         = peer_count,
            uptime_seconds     = uptime_sec,
            has_rule           = has_rule,
            exit_count         = #exit_policy.exits,
            exit_role          = exit_policy.role,
            legacy_route_inject = legacy_route_inject,
        },
    }
end

-- ─────────────────────────────────────────────────────────────
-- Enable/Disable（自启开关，与 start/stop 分离）
-- enable  = PROXY_ENABLED=1 + 若 GNB 已运行则立即 start
-- disable = PROXY_ENABLED=0 + 若 proxy 运行中则立即 stop + route_restore
-- ─────────────────────────────────────────────────────────────

function M.enable(opts)
    opts = opts or {}
    -- 校验参数（避免非法 dns_mode/region/mode 写入 conf）
    local v_ok, v_err = M.validate_params(opts)
    if not v_ok then return nil, v_err end

    local fields = { proxy_enabled = true }
    if opts.mode        then fields.proxy_mode  = opts.mode end
    if opts.region      then fields.node_region  = opts.region end
    if opts.dns_mode    then fields.dns_mode     = opts.dns_mode end
    if opts.dns_server  then fields.dns_server   = opts.dns_server end
    if opts.proxy_peers then fields.proxy_peers  = opts.proxy_peers end
    M.update_config(fields)

    -- 安装 plugin hook symlink / 部署
    M.install_plugin_hooks()

    -- 若 GNB 已运行，立即启动 proxy
    local cfg_m = require("luci.model.mynet.config")
    local node_id = cfg_m.get_node_id()
    local node_m = require("luci.model.mynet.node")
    if node_id and node_m.gnb_is_running(node_id) then
        local ok, msg = M.start(opts)
        if not ok then
            return nil, "enabled but start failed: " .. (msg or "")
        end
        return true, "enabled and started"
    end

    return true, "enabled (GNB not running, will auto-start with GNB)"
end

function M.disable()
    -- 若 proxy 运行中，先停止
    local st = M.get_status()
    if st and st.running then
        M.stop()
    end

    -- 清理 GNB route.conf 中的历史注入段
    M.route_restore()

    -- 设 PROXY_ENABLED=0
    M.update_config({ proxy_enabled = false })

    -- 移除 plugin hooks
    M.remove_plugin_hooks()

    return true, "disabled"
end

-- ─────────────────────────────────────────────────────────────
-- Plugin hook 管理
-- ─────────────────────────────────────────────────────────────

local PLUGIN_PROXY_DIR = util.MYNET_HOME .. "/scripts/plugin/proxy"

function M.install_plugin_hooks()
    util.ensure_dir(PLUGIN_PROXY_DIR)
    local hooks_src = PROXY_SCRIPT_DIR .. "/hooks"
    for _, hook in ipairs({ "pre_start.sh", "post_start.sh", "stop.sh" }) do
        local src = hooks_src .. "/" .. hook
        local dst = PLUGIN_PROXY_DIR .. "/" .. hook
        if util.file_exists(src) then
            util.exec("cp -f " .. util.shell_escape(src) .. " " .. util.shell_escape(dst)
                .. " && chmod +x " .. util.shell_escape(dst))
        end
    end
end

function M.remove_plugin_hooks()
    if util.file_exists(PLUGIN_PROXY_DIR) then
        util.exec("rm -rf " .. util.shell_escape(PLUGIN_PROXY_DIR))
    end
end

-- ─────────────────────────────────────────────────────────────
-- 启动 proxy（即时控制，不改 PROXY_ENABLED）
-- ─────────────────────────────────────────────────────────────

function M.start(opts)
    opts = opts or {}
    local saved_cfg = M.load_config()
    local mode       = opts.mode         or saved_cfg.proxy_mode   or "client"
    local region     = opts.region       or saved_cfg.node_region  or "domestic"
    local policy     = opts.proxy_policy or saved_cfg.proxy_policy or "whitelist"
    local dns_mode   = opts.dns_mode     or saved_cfg.dns_mode     or "none"
    local dns_server = opts.dns_server   or saved_cfg.dns_server   or ""

    -- 前置检查：GNB 必须已运行
    local cfg_m = require("luci.model.mynet.config")
    local node_id = cfg_m.get_node_id()
    if not node_id then
        return nil, "node_id not configured"
    end
    local node_m = require("luci.model.mynet.node")
    if not node_m.gnb_is_running(node_id) then
        return nil, "GNB is not running — start GNB first"
    end

    -- 校验参数（使用模块级常量）
    local ok, err = M.validate_params({ mode = mode, region = region, proxy_policy = policy, dns_mode = dns_mode })
    if not ok then return nil, err end

    -- 保存运行参数（不改 proxy_enabled）
    local fields = { proxy_mode = mode, node_region = region, proxy_policy = policy, dns_mode = dns_mode, dns_server = dns_server }
    if opts.dns_domestic_server then fields.dns_domestic_server = opts.dns_domestic_server end
    if opts.proxy_peers then fields.proxy_peers  = opts.proxy_peers end
    M.update_config(fields)

    -- 生成 IP 列表配置（proxy.sh generate）
    if not util.file_exists(PROXY_SH) then
        return nil, "proxy.sh not found: " .. PROXY_SH
    end
    if not util.file_exists(ROUTE_POLICY_SH) then
        return nil, "route_policy.sh not found: " .. ROUTE_POLICY_SH
    end

    -- route.conf 交还 agent：清理历史注入残留（现网存量节点一次性收敛）
    M.route_restore()

    -- 转发策略由 gnb proxy.conf 承载（Go agent 投影），Lua 侧只验证在场
    local ep = M.exit_policy_status()
    local proxy_warn = nil
    if not M.exit_policy_ok(ep) then
        proxy_warn = ep.present
            and "proxy.conf 已存在但出口策略不可用（proxy on / exit 缺失）"
            or  ("缺少 proxy.conf（" .. (ep.path or "未知路径")
                 .. "），gnb 数据层无出口策略，需带 P2 补丁的 gnb")
        util.log_warn("proxy start: " .. proxy_warn)
    end

    local gen_out, gen_code = util.exec_status(
        "MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
        .. " bash " .. util.shell_escape(PROXY_SH) .. " generate 2>&1")
    if gen_code ~= 0 then
        return nil, "proxy generate failed: " .. util.trim(gen_out or "")
    end

    -- 写入 proxy_policy_params.env（route_policy.sh start 读取此文件）
    -- 必须在 proxy.sh generate 之后，因为网关 IP 从 proxy_route.conf 中读取
    local params_file = util.MYNET_HOME .. "/var/proxy_policy_params.env"
    util.ensure_dir(util.MYNET_HOME .. "/var")
    -- 由 Lua 持有的 DNS 常量（单一真相，不依赖 shell 端硬编码）
    local foreign_dns_str  = table.concat(M.FOREIGN_DNS_SERVERS, " ")
    local domestic_dns_str = opts.dns_domestic_server
        or table.concat(M.DOMESTIC_DNS_SERVERS, ",")
    local match_mode = (policy == "blacklist") and "inverted" or "normal"
    if util.file_exists(params_file) then
        -- 仅更新动态字段，保留其他已有字段（如 FW_TYPE/GNB_INTERFACE）
        local pdata = util.read_file(params_file) or ""
        pdata = _upsert_env(pdata, "NODE_REGION",         region)
        pdata = _upsert_env(pdata, "MATCH_MODE",          match_mode)
        pdata = _upsert_env(pdata, "DNS_MODE",            dns_mode)
        pdata = _upsert_env(pdata, "DNS_SERVER",          dns_server)
        pdata = _upsert_env(pdata, "DNS_DOMESTIC_SERVER", domestic_dns_str)
        pdata = _upsert_env(pdata, "FOREIGN_DNS_SERVERS", foreign_dns_str)
        util.write_file(params_file, pdata)
    else
        -- 首次创建：从 proxy_route.conf 读取网关，从系统检测防火墙类型
        local route_conf_path = PROXY_CONF_DIR .. "/proxy_route.conf"
        local gateway = ""
        if util.file_exists(route_conf_path) then
            local rc = util.read_file(route_conf_path) or ""
            gateway = rc:match("# Gateway: (%S+)") or ""
        end
        local gnb_iface = cfg_m.get_vpn_interface()
        local fw_type = util.trim(util.exec("command -v nft >/dev/null 2>&1 && echo nftables || echo iptables") or "nftables")
        local lines = {
            "# Auto-generated by proxy.lua — do not edit",
            "FW_TYPE=" .. fw_type,
            "TABLE_ID=200",
            "TABLE_NAME=mynet_proxy",
            "FWMARK=0xc8",
            "RULE_PRIORITY=31800",
            "GNB_INTERFACE=" .. gnb_iface,
            "PROXY_GATEWAYS=" .. gateway,
            'PROXY_MODE="' .. mode .. '"',
            'NODE_REGION="' .. region .. '"',
            'PROXY_POLICY="' .. policy .. '"',
            'MATCH_MODE="' .. match_mode .. '"',
            'DNS_MODE="' .. dns_mode .. '"',
            'DNS_SERVER="' .. dns_server .. '"',
            'DNS_DOMESTIC_SERVER="' .. domestic_dns_str .. '"',
            'FOREIGN_DNS_SERVERS="' .. foreign_dns_str .. '"',
            'ROUTE_CONFIG="' .. PROXY_CONF_DIR .. '/proxy_route.conf"',
        }
        util.write_file(params_file, table.concat(lines, "\n") .. "\n")
    end

    local out, code = util.exec_status(
        "MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
        .. " bash " .. util.shell_escape(ROUTE_POLICY_SH) .. " start 2>&1")

    -- 始终路由非国内 DNS 服务器到 GNB 隧道，并保存状态
    -- 必须在 code 判断之前执行：dns_split 失败时 route_policy.sh 可能非零，
    -- 但核心策略路由（nft/ip rule/route table）已生效
    util.ensure_dir(util.MYNET_HOME .. "/var")
    util.save_json_file(PROXY_STATE_FILE, {
        start_ts  = os.time(),
        mode      = mode,
        region    = region,
        policy    = policy,
        dns_mode  = dns_mode,
    })
    M.route_foreign_dns()

    if code ~= 0 then
        -- dns_split 失败不影响路由核心，记录警告并返回部分成功
        util.log_warn("proxy start partial: " .. util.trim(out or ""))
        return true, "started (dns split warning: " .. util.trim(out or "") .. ")"
            .. (proxy_warn and ("；" .. proxy_warn) or "")
    end

    -- 注册防火墙 include（防火墙重启后恢复策略路由）
    M.register_firewall_include()

    return true, util.trim(out or "started")
        .. (proxy_warn and ("；" .. proxy_warn) or "")
end

-- ─────────────────────────────────────────────────────────────
-- 路由非国内 DNS 服务器到 GNB 隧道（始终生效，无论代理/DNS 模式）
-- 本函数为唯一执行点，route_policy.sh 不再重复处理
-- ─────────────────────────────────────────────────────────────

function M.route_foreign_dns(gnb_interface)
    if not gnb_interface or gnb_interface == "" then
        local cfg_m = require("luci.model.mynet.config")
        gnb_interface = cfg_m.get_vpn_interface()
    end
    if not gnb_interface then return nil, "GNB interface not found" end

    local routed, verified = 0, 0
    for _, dns_ip in ipairs(M.FOREIGN_DNS_SERVERS) do
        -- GUARD-OK 代理 DNS /32 走隧道，属产品面策略，不是对端路由写入
        -- （对端路由唯一入口 = mynet apply，spec/route-apply-unification.md）
        local ok = util.exec("ip route replace " .. dns_ip .. "/32 dev "
            .. util.shell_escape(gnb_interface) .. " 2>/dev/null && echo ok || echo fail")
        if ok:find("ok") then
            routed = routed + 1
            -- 验证实际出入接口是否为隐道（GNB 未起时会 fallback 到默认路由）
            local route_get = util.exec("ip route get " .. dns_ip
                .. " 2>/dev/null | head -1") or ""
            if route_get:find("dev " .. gnb_interface, 1, true) then
                verified = verified + 1
            end
        end
    end
    util.log_info(string.format(
        "route_foreign_dns: routed=%d/%d verified=%d via %s",
        routed, #M.FOREIGN_DNS_SERVERS, verified, gnb_interface))
    return { routed = routed, verified = verified, total = #M.FOREIGN_DNS_SERVERS }, nil
end

function M.unroute_foreign_dns()
    for _, dns_ip in ipairs(M.FOREIGN_DNS_SERVERS) do
        util.exec("ip route delete " .. dns_ip .. "/32 2>/dev/null || true")
    end
    util.log_info("unroute_foreign_dns: cleaned")
end

-- ─────────────────────────────────────────────────────────────
-- 停止 proxy
-- ─────────────────────────────────────────────────────────────

function M.stop()
    if not util.file_exists(ROUTE_POLICY_SH) then
        return nil, "route_policy.sh not found"
    end

    -- 清理非国内 DNS 路由
    M.unroute_foreign_dns()

    local out, code = util.exec_status(
        "MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
        .. " bash " .. util.shell_escape(ROUTE_POLICY_SH) .. " stop 2>&1")

    -- Route restore (Lua): 在 proxy.sh 之后恢复
    M.route_restore()

    -- 清除状态 + 防火墙 include
    os.remove(PROXY_STATE_FILE)
    M.unregister_firewall_include()

    if code ~= 0 then
        return nil, "proxy stop: " .. util.trim(out or "")
    end
    return true, util.trim(out or "stopped")
end

-- ─────────────────────────────────────────────────────────────
-- 重载 IP 列表
-- ─────────────────────────────────────────────────────────────

function M.reload()
    if not util.file_exists(PROXY_SH) then
        return nil, "proxy.sh not found"
    end
    if not util.file_exists(ROUTE_POLICY_SH) then
        return nil, "route_policy.sh not found"
    end
    local gen_out, gen_code = util.exec_status(
        "MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
        .. " bash " .. util.shell_escape(PROXY_SH) .. " generate --force 2>&1")
    if gen_code ~= 0 then
        return nil, "proxy reload (generate): " .. util.trim(gen_out or "")
    end
    local out, code = util.exec_status(
        "MYNET_HOME=" .. util.shell_escape(util.MYNET_HOME)
        .. " bash " .. util.shell_escape(ROUTE_POLICY_SH) .. " restart 2>&1")
    if code ~= 0 then
        return nil, "proxy reload (restart): " .. util.trim(out or "")
    end
    return true, util.trim(out or "reloaded")
end

-- ─────────────────────────────────────────────────────────────
-- 防火墙 include 注册/注销（防火墙重启后恢复策略路由）
-- ─────────────────────────────────────────────────────────────

local FW_INCLUDE_SCRIPT = util.SCRIPTS_DIR .. "/proxy.firewall.include"
local FW_INCLUDE_NAME   = "mynet_proxy"

function M.register_firewall_include()
    -- 生成 include 脚本：检查 proxy_state.json 存在 → 幂等启动 route_policy.sh
    local script = table.concat({
        "#!/bin/sh",
        "# Auto-generated by mynet proxy — do not edit",
        'STATE="' .. PROXY_STATE_FILE .. '"',
        'ROUTE_POLICY="' .. ROUTE_POLICY_SH .. '"',
        '[ -f "$STATE" ] || exit 0',
        '[ -f "$ROUTE_POLICY" ] || exit 0',
        'MYNET_HOME="' .. util.MYNET_HOME .. '" bash "$ROUTE_POLICY" start 2>/dev/null || true',
    }, "\n") .. "\n"
    util.write_file(FW_INCLUDE_SCRIPT, script)
    util.exec("chmod +x " .. util.shell_escape(FW_INCLUDE_SCRIPT))

    -- 注册 UCI firewall include
    util.exec(string.format(
        "uci -q delete firewall.%s 2>/dev/null; "
        .. "uci set firewall.%s=include; "
        .. "uci set firewall.%s.path='%s'; "
        .. "uci set firewall.%s.reload='1'; "
        .. "uci commit firewall 2>/dev/null",
        FW_INCLUDE_NAME, FW_INCLUDE_NAME, FW_INCLUDE_NAME,
        FW_INCLUDE_SCRIPT, FW_INCLUDE_NAME))
end

function M.unregister_firewall_include()
    util.exec("uci -q delete firewall." .. FW_INCLUDE_NAME .. " 2>/dev/null; uci commit firewall 2>/dev/null")
    os.remove(FW_INCLUDE_SCRIPT)
end

-- ─────────────────────────────────────────────────────────────
-- 诊断指定 IP 的路由路径
-- ─────────────────────────────────────────────────────────────

function M.diagnose_ip(ip)
    if not ip or not ip:match("^%d+%.%d+%.%d+%.%d+$") then
        return nil, "invalid IPv4 address"
    end

    local result = { ip = ip, checks = {} }

    -- 1. ip route get
    local route_out = util.trim(util.exec(
        "ip route get " .. util.shell_escape(ip) .. " 2>/dev/null") or "")
    result.checks[#result.checks + 1] = {
        name = "ip_route_get",
        output = route_out,
    }

    -- 2. ipset test
    local ipset_out, ipset_code = util.exec_status(
        "ipset test mynet_proxy " .. util.shell_escape(ip) .. " 2>&1")
    result.checks[#result.checks + 1] = {
        name    = "ipset_test",
        matched = ipset_code == 0,
        output  = util.trim(ipset_out or ""),
    }

    -- 3. nftables lookup
    local nft_out = util.trim(util.exec(
        "nft get element inet mynet_proxy mynet_proxy '{ "
        .. ip .. " }' 2>/dev/null") or "")
    result.checks[#result.checks + 1] = {
        name    = "nft_lookup",
        matched = nft_out ~= "",
        output  = nft_out,
    }

    -- 4. traceroute (quick, 3 hops max)
    local trace = util.trim(util.exec(
        "traceroute -n -m 3 -w 1 " .. util.shell_escape(ip) .. " 2>/dev/null") or "")
    result.checks[#result.checks + 1] = {
        name   = "traceroute",
        output = trace,
    }

    return result, nil
end

-- ─────────────────────────────────────────────────────────────
-- route.conf 所有权：唯一写入方是 Go agent（mynet apply / 投影）
--
-- gnb 的 route.conf 是 **L5 地址声明**（哪个 peer 认领哪个网段），
-- 不承担转发策略。代理分流的转发路径由两处负责：
--   · gnb 数据层出口：driver/gnb/conf/<nid>/proxy.conf（P2 补丁，
--     role client|server|both / proxy on / [default] exit <nid> w=<n>），
--     由 Go 的 plugin_proxy.go:ApplyProxyToGnb 投影写入；
--   · OS 层：bundle.network.routes → mynet apply（策略路由 / IP 集合 / DNS）。
--
-- 所以 Lua 不再向 route.conf 注入 /8、/16 大段。历史上的注入有两条硬伤：
--   1) gnb 里 route.conf 命中**优先于** proxy 策略，注入的几百条公网段
--      会把「未登记目的地」死绑到某个 peer，exit 权重与降级完全失效；
--   2) 注入段由 Lua 追加、Go 不知道它的存在，重启与重投影都清不掉，
--      现网表现为一个 14 行的 route.conf 被灌成上千行。
-- 参见 spec/route-apply-unification.md：route.conf 不装路由，
-- OpenWrt 也禁止再从 route.conf 反推 OS 路由。
--
-- 本模块只保留**清理**能力：识别并剔除历史注入段（marker 段），
-- 供 start / disable / stop / pre_start 以及 node.lua 生成系统路由前调用。
-- ─────────────────────────────────────────────────────────────

-- Marker（与 scripts/proxy/hooks/stop.sh 统一）
M.INJECT_MARKER_BEGIN        = "#----proxy start----"
M.INJECT_MARKER_END          = "#----proxy end----"
local INJECT_SERVER_MARKER_BEGIN = "#----proxy-server start----"
local INJECT_SERVER_MARKER_END   = "#----proxy-server end----"

-- 驱动 conf 目录：driver/gnb/conf/{nid}，route.conf 与 proxy.conf 同处一地
local function get_driver_conf_dir()
    local cfg_m = require("luci.model.mynet.config")
    local node_id = cfg_m.get_node_id()
    if not node_id then return nil, "node_id not configured" end
    local nid_str = util.int_str(node_id)
    return cfg_m.get_gnb_conf_root() .. "/" .. nid_str, nid_str
end

-- 获取 GNB route.conf 路径（driver/gnb/conf/{nid}/route.conf）
local function get_route_conf_path()
    local conf_dir, nid_str = get_driver_conf_dir()
    if not conf_dir then return nil, nid_str end
    return conf_dir .. "/route.conf", nid_str
end

-- 从文件内容中移除两个 proxy marker 段（含 marker 行本身）
-- 判断是否为 proxy marker 开始行，兼容新旧格式：
--   新格式: #----proxy start----  /  #----proxy-server start----
--   旧格式: #---------proxy start {nodeId}--------
local function is_proxy_marker_begin(line)
    if line:sub(1,1) ~= "#" then return false end
    return line:find(M.INJECT_MARKER_BEGIN, 1, true)
        or line:find(INJECT_SERVER_MARKER_BEGIN, 1, true)
        or line:find("proxy start", 1, true)
end
local function is_proxy_marker_end(line)
    if line:sub(1,1) ~= "#" then return false end
    return line:find(M.INJECT_MARKER_END, 1, true)
        or line:find(INJECT_SERVER_MARKER_END, 1, true)
        or line:find("proxy end", 1, true)
end

local function strip_marker_section(content)
    if not content then return content end
    -- 快速判断是否需要处理
    if not content:find("proxy start", 1, true) then return content end
    local result = {}
    local skipping = false
    for line in (content .. "\n"):gmatch("([^\n]*)\n") do
        if is_proxy_marker_begin(line) then
            skipping = true
        elseif is_proxy_marker_end(line) then
            skipping = false
        elseif not skipping then
            result[#result + 1] = line
        end
    end
    while #result > 0 and result[#result] == "" do
        result[#result] = nil
    end
    return table.concat(result, "\n") .. "\n"
end

-- 导出给 node.lua 使用：生成系统路由前剔除 proxy 注入段
M.strip_route_injections = strip_marker_section

-- 剔除 GNB route.conf 中的历史 proxy 注入段（幂等）
-- 注入本身已废弃（见上方所有权说明），这里只负责存量清理：
-- marker 段一旦被清掉，本函数永久 no-op，route.conf 交还给 Go agent。
function M.route_restore()
    local route_conf = get_route_conf_path()
    if not route_conf then return nil, "node_id not configured" end

    local content = util.read_file(route_conf) or ""
    if not content:find("proxy start", 1, true) then
        return true, "nothing to restore"
    end

    util.write_file(route_conf, strip_marker_section(content))
    util.log_info("route_restore: removed legacy proxy injection from " .. route_conf)
    return true, "removed injected section"
end

-- 历史注入残留条数（诊断用，不是健康指标：期望恒为 0）
function M.route_conf_legacy_count()
    local route_conf = get_route_conf_path()
    if not route_conf or not util.file_exists(route_conf) then return 0 end

    local content = util.read_file(route_conf) or ""
    if not content:find("proxy start", 1, true) then return 0 end

    local count = 0
    for line in content:gmatch("[^\n]+") do
        -- 匹配三种 CIDR：/8 (255.0.0.0)、/16 (255.255.0.0)、/24 (255.255.255.0)
        if line:match("^%d+|%d+%.0%.0%.0|255%.0%.0%.0$")
            or line:match("^%d+|%d+%.%d+%.0%.0|255%.255%.0%.0$")
            or line:match("^%d+|%d+%.%d+%.%d+%.0|255%.255%.255%.0$") then
            count = count + 1
        end
    end
    return count
end

-- ─────────────────────────────────────────────────────────────
-- 出口策略 —— gnb proxy.conf（P2 补丁）状态读取
--
-- proxy.conf 由 Go agent 投影（mynet/plugin_proxy.go:ApplyProxyToGnb），
-- Lua 只读不写：这是 route.conf 注入废弃后代理转发语义的接替者。
-- ─────────────────────────────────────────────────────────────

function M.exit_policy_status()
    local conf_dir, nid_str = get_driver_conf_dir()
    local res = {
        present   = false,
        path      = nil,
        node_id   = nid_str,
        role      = nil,
        proxy_on  = false,
        exits     = {},
    }
    if not conf_dir then
        res.error = "node_id not configured"
        return res
    end
    res.path = conf_dir .. "/proxy.conf"
    res.present = util.file_exists(res.path)
    if not res.present then return res end

    for line in ((util.read_file(res.path) or "") .. "\n"):gmatch("([^\n]*)\n") do
        local v = line:match("^%s*role%s+(%S+)")
        if v then res.role = v end
        if line:match("^%s*proxy%s+on%s*$") then res.proxy_on = true end
        v = line:match("^%s*exit%s+(%d+)")
        if v then res.exits[#res.exits + 1] = v end
    end
    return res
end

-- 出口策略是否可用：server/both 只需 proxy on（准入由 ingress_open 负责），
-- client 必须 proxy on 且至少一个出口，否则未登记目的地直接丢包。
function M.exit_policy_ok(st)
    if not st or not st.present or not st.proxy_on then return false end
    if st.role == "server" then return true end
    return #st.exits > 0
end

-- ─────────────────────────────────────────────────────────────
-- 网络检测（从 controller 移入 model，统一业务逻辑层）
-- ─────────────────────────────────────────────────────────────

function M.net_detect(dtype)
    local cmd
    if dtype == "domestic" then
        cmd = "curl -s --connect-timeout 5 --max-time 10 'https://myip.ipip.net' 2>/dev/null"
            .. " || curl -s --connect-timeout 5 --max-time 10 'https://ip.cn/api/index?type=0' 2>/dev/null"
    elseif dtype == "proxy" then
        cmd = "curl -s --connect-timeout 8 --max-time 15 'https://api.ipify.org?format=json' 2>/dev/null"
            .. " || curl -s --connect-timeout 8 --max-time 15 'https://ifconfig.me/ip' 2>/dev/null"
    else
        return nil, "invalid type: " .. tostring(dtype)
    end
    local out = util.trim(util.exec(cmd) or "")
    if out ~= "" then
        return { ip = out:match("%d+%.%d+%.%d+%.%d+") or out, raw = out }
    end
    return nil, dtype .. " IP detection failed"
end

function M.net_check(host)
    if not host or host == "" or not host:match("^[%w%.%-]+$") then
        return nil, "invalid host"
    end
    local cmd = string.format(
        "curl -s -o /dev/null -w '%%{http_code} %%{time_total} %%{remote_ip}' "
        .. "--connect-timeout 8 --max-time 15 'https://%s/' 2>/dev/null",
        util.shell_escape(host):gsub("'", ""))
    local out = util.trim(util.exec(cmd) or "")
    local code, time_s, ip = out:match("^(%d+)%s+([%d%.]+)%s+([%d%.]+)")
    if code then
        return {
            host      = host,
            reachable = tonumber(code) > 0 and tonumber(code) < 500,
            http_code = tonumber(code),
            time_ms   = math.floor(tonumber(time_s) * 1000),
            ip        = ip,
        }
    end
    return {
        host      = host,
        reachable = false,
        error     = out ~= "" and out or "connection failed",
    }
end

return M
