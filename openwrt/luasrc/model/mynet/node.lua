-- mynet/node.lua  — 节点管理模块
-- 节点列表、配置下载、状态上报、VPN 服务控制。
-- 对应 Go 项目 internal/application/node_app_service.go +
--              internal/application/status_app_service.go

local M    = {}
local api  = require("luci.model.mynet.api")
local auth = require("luci.model.mynet.auth")
local cfg  = require("luci.model.mynet.config")
local util = require("luci.model.mynet.util")

-- 安全整数字符串：避免 Lua 大整数变成科学计数法。
-- ⚠️ v 为 nil/空时**必须回退到当前节点**，否则路径会变成 conf/0/：
-- check_config 找不到文件，stop_gnb 读空 pidfile 还返回 true（静默假成功）。
-- 实测坑：mynetd 分派 service stop 不带 --node-id，全走这条。
local function nid(v)
    if v == nil or v == "" then
        v = cfg.get_node_id()
    end
    return util.int_str(v)
end

-- 鉴权 + zone 上下文（减少重复模板）
local function auth_context()
    local current, err = auth.ensure_valid()
    if err then return nil, nil, err end
    local zone = cfg.load_current_zone()
    local zone_id = zone and zone.zone_id or "0"
    return current, zone_id, nil
end

-- ed25519 公钥格式校验（64 hex chars = 32 bytes）
local function is_valid_ed25519_pub(hex)
    return hex and #hex == 64 and hex:match("^[0-9a-fA-F]+$") ~= nil
end

-- ─────────────────────────────────────────────────────────────
-- 节点状态码（与 Go 项目保持一致）
-- ─────────────────────────────────────────────────────────────
M.STATUS = {
    OFFLINE     = 0,
    ONLINE      = 1,
    CONNECTING  = 2,
    STOPPED     = 3,
    RUNNING     = 4,
    ACTIVE      = 5,
    STARTING    = 6,
    STOPPING    = 7,
    MAINTENANCE = 8,
}

M.STATUS_NAMES = {
    [0] = "offline",
    [1] = "online",
    [2] = "connecting",
    [3] = "stopped",
    [4] = "running",
    [5] = "active",
    [6] = "starting",
    [7] = "stopping",
    [8] = "maintenance",
}

-- ─────────────────────────────────────────────────────────────
-- 获取节点列表（分页）
-- 对应 Go: GET /nodes?page=N&per_page=N
-- 返回: (nodes_data_table, nil) 或 (nil, error_string)
-- nodes_data_table 含 .data (节点数组)、.total、.current_page、.last_page
-- ─────────────────────────────────────────────────────────────
function M.get_nodes(page, per_page)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    if zone_id == "0" then return nil, "no zone selected" end

    local endpoint = string.format("/nodes?page=%d&per_page=%d",
        page or 1, per_page or 20)
    local data, api_err = api.get_json(cfg.get_api_url(), endpoint,
        current.token, zone_id)
    if api_err then return nil, api_err end
    if not data or not data.success then
        return nil, (data and data.message) or "failed to get nodes"
    end

    -- 返回分页结构：{ data=[...], total, per_page, current_page, last_page }
    return (data.data and data.data.nodes) or {}, nil
end

-- ─────────────────────────────────────────────────────────────
-- 获取渲染后的节点配置文本（text/plain）
-- GET /nodes/{id}/config?render_conf=1
-- 直接返回可写入 node.conf 的文本
-- ─────────────────────────────────────────────────────────────
function M.get_node_config_rendered(node_id)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    local endpoint = string.format("/nodes/%s/config?render_conf=1", nid(node_id))
    return api.get_text(cfg.get_api_url(), endpoint, current.token, zone_id)
end

-- ─────────────────────────────────────────────────────────────
-- 获取渲染后的路由配置文本（text/plain）
-- 对应 Go: GET /route/node/{id}?render_conf=1
-- ─────────────────────────────────────────────────────────────
function M.get_route_config_rendered(node_id)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    local endpoint = string.format("/route/node/%s?render_conf=1", nid(node_id))
    return api.get_text(cfg.get_api_url(), endpoint, current.token, zone_id)
end

-- ─────────────────────────────────────────────────────────────
-- 获取渲染后的服务地址索引（text/plain）
-- 后端端点: GET /services/indexes?render_conf=1
-- zone_id 通过 X-Tenant-ID 请求头传递（由 auth_headers 注入）
-- ─────────────────────────────────────────────────────────────
function M.get_services_index()
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    return api.get_text(cfg.get_api_url(),
        "/services/indexes?render_conf=1", current.token, zone_id)
end

-- ─────────────────────────────────────────────────────────────
-- config-bundle: 一次性获取全部配置+密钥（v2 API）
-- GET /nodes/{id}/config-bundle
-- 返回: (bundle_table, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.get_config_bundle(node_id)
    local current, zone_id, err = auth_context()
    if err then return nil, err end

    -- config-bundle 返回裸对象（无 {success,data} 包装），使用 api.get_config_bundle
    local data, api_err = api.get_config_bundle(cfg.get_api_url(), nid(node_id),
        current.token, zone_id)
    if api_err then return nil, api_err end
    if not data then
        return nil, "empty config-bundle response"
    end
    return data, nil
end

-- ─────────────────────────────────────────────────────────────
-- 更新节点状态（上报到服务端）
-- 对应 Go: PATCH /nodes/{id}/status
-- ─────────────────────────────────────────────────────────────
function M.update_node_status(node_id, status_code)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    local endpoint = string.format("/nodes/%s/status", nid(node_id))

    local data, api_err = api.patch_json(cfg.get_api_url(), endpoint,
        { status = status_code }, current.token, zone_id)
    if api_err then return nil, api_err end
    if not data or not data.success then
        return nil, (data and data.message) or "status update failed"
    end
    return true, nil
end

-- Guest 模式：从 guest.json 的 index_addr 生成 address.conf 内容
local function guest_address_text()
    if cfg.get_mode() ~= "guest" then return nil end
    local guest_m = require("luci.model.mynet.guest")
    local g = guest_m.load_config()
    if not g or not g.index_addr or g.index_addr == "" then return nil end
    local host, port = g.index_addr:match("^([%w%.%-]+):(%d+)$")
    if not host then return nil end
    return string.format("i|0|%s|%s", host, port)
end

local function normalize_config_content(content)
    content = content or ""
    if content == "" or content:sub(-1) == "\n" then
        return content
    end
    return content .. "\n"
end

local function resolve_local_config_path(node_id, config_type)
    local self_nid = nid(node_id)
    if config_type == "node" then
        return string.format("%s/%s/node.conf", cfg.get_gnb_conf_root(), self_nid)
    end
    if config_type == "route" then
        return string.format("%s/%s/route.conf", util.GNB_CONF_DIR, self_nid)
    end
    if config_type == "address" then
        return string.format("%s/%s/address.conf", util.GNB_CONF_DIR, self_nid)
    end
    return nil, "unknown config type: " .. tostring(config_type)
end

local function apply_local_config_side_effects(node_id, config_type)
    if config_type == "node" then
        local ok, err = cfg.generate_mynet_conf(tonumber(node_id))
        if not ok then
            return false, "generate mynet.conf: " .. (err or "unknown")
        end
        return true, nil
    end

    if config_type == "route" then
        local _, route_err = M.generate_route_conf(node_id)
        if route_err then
            return false, route_err
        end

        local proxy_m = require("luci.model.mynet.proxy")
        local pcfg = proxy_m.load_config()
        if pcfg.proxy_enabled then
            local _, inject_err = proxy_m.route_inject()
            if inject_err and inject_err ~= "no proxy peers configured" then
                util.log_warn("route.conf post-write hook: " .. inject_err)
            end
        end
    end

    return true, nil
end

function M.write_local_config(node_id, config_type, content)
    local target_path, path_err = resolve_local_config_path(node_id, config_type)
    if not target_path then
        return false, path_err, nil
    end

    local ok, write_err = util.write_file(target_path, normalize_config_content(content))
    if not ok then
        return false, write_err or "write failed", target_path
    end

    local hook_ok, hook_err = apply_local_config_side_effects(node_id, config_type)
    if not hook_ok then
        return false, hook_err or "post-write hook failed", target_path
    end

    return true, nil, target_path
end

-- ─────────────────────────────────────────────────────────────
-- 刷新配置：下载并写入 node.conf、route.conf、address.conf
-- 对应 Go: cmd/refresh_config.go 的核心流程
-- 返回: { ok, files=[], errors=[] }
-- ─────────────────────────────────────────────────────────────
function M.refresh_configs(node_id)
    local results = { ok = true, files = {}, errors = {} }

    local vpn_type = cfg.get_vpn_type()

    -- 1. node.conf
    local node_text, err1 = M.get_node_config_rendered(node_id)
    if err1 then
        results.ok = false
        results.errors[#results.errors + 1] = "node.conf: " .. err1
    elseif node_text and node_text ~= "" then
        local ok, we, node_conf_path = M.write_local_config(node_id, "node", node_text)
        if ok then
            results.files[#results.files + 1] = node_conf_path
        else
            results.ok = false
            results.errors[#results.errors + 1] = "write node.conf: " .. (we or "")
        end
    end

    -- 2. route.conf
    local route_text, err2 = M.get_route_config_rendered(node_id)
    if err2 then
        results.ok = false
        results.errors[#results.errors + 1] = "route.conf: " .. err2
    elseif route_text and route_text ~= "" then
        local ok, we, route_path = M.write_local_config(node_id, "route", route_text)
        if ok then
            results.files[#results.files + 1] = route_path
        else
            results.ok = false
            results.errors[#results.errors + 1] = "write route.conf: " .. (we or "")
        end
    end

    -- 3. address.conf（guest 模式用本地 index_addr，否则从 API 拉取）
    local svc_text = guest_address_text()
    if not svc_text then
        local err3
        svc_text, err3 = M.get_services_index()
        if err3 then
            util.log_warn("refresh_configs: address.conf: " .. err3)
            svc_text = nil
        end
    end
    if svc_text and svc_text ~= "" then
        local ok, we, addr_path = M.write_local_config(node_id, "address", svc_text)
        if ok then
            results.files[#results.files + 1] = addr_path
        else
            util.log_warn("refresh_configs: write address.conf: " .. (we or ""))
        end
    end
    -- 4. 同步对端公钥（优先批量 API）
    local pk_result = M.refresh_peer_keys_batch(node_id)
    if pk_result.count > 0 then
        results.files[#results.files + 1] = string.format("peer_keys(%d)", pk_result.count)
    end
    for _, e in ipairs(pk_result.errors or {}) do
        util.log_warn("refresh_configs: peer key: " .. e)
    end
    return results
end

-- ─────────────────────────────────────────────────────────────
-- Config Bundle：单次请求获取全部配置 + 密钥，然后写入磁盘
-- 优先使用 bundle API，若后端不支持（404）则回退到 refresh_configs()
-- 返回: { ok, files=[], errors=[], method="bundle"|"legacy" }
-- ─────────────────────────────────────────────────────────────
function M.refresh_configs_bundle(node_id)
    local current, zone_id, err = auth_context()
    if err then
        return { ok = false, files = {}, errors = { "auth: " .. err }, method = "legacy" }
    end
    local self_nid = nid(node_id)

    -- 尝试 bundle API
    local bundle, berr = api.get_config_bundle(
        cfg.get_api_url(), self_nid, current.token, zone_id)

    if berr == "not_supported" or not bundle then
        -- 后端不支持 bundle → 回退
        util.log_info("node", "config-bundle not supported, fallback to legacy refresh")
        local r = M.refresh_configs(node_id)
        r.method = "legacy"
        return r
    end

    local results = { ok = true, files = {}, errors = {}, warnings = {}, method = "bundle" }
    local gnb_conf_root = cfg.get_gnb_conf_root()

    -- 处理 warnings（back v2.2.0: 对等节点公钥获取失败等）
    local bw = bundle.warnings or (bundle.data and bundle.data.warnings) or {}
    for _, w in ipairs(bw) do
        local msg = w.message or (w.type and ("type=" .. w.type) or "unknown warning")
        results.warnings[#results.warnings + 1] = msg
        util.log_warn("config-bundle warning: " .. msg)
    end

    -- 写入配置文件
    local files = bundle.files or (bundle.data and bundle.data.files) or {}
    local file_map = {
        ["node.conf"]    = "node",
        ["route.conf"]   = "route",
        ["address.conf"] = "address",
    }
    -- Guest 模式：address.conf 用本地 index_addr，不用 API 返回的空内容
    local guest_addr = guest_address_text()
    if guest_addr then
        files["address.conf"] = guest_addr
    end
    for fname, config_type in pairs(file_map) do
        local content = files[fname]
        if config_type == "route" and (not content or util.trim(content) == "") then
            results.ok = false
            results.errors[#results.errors + 1] = "write route.conf: bundle returned empty content"
        elseif content and content ~= "" then
            local ok, we, dest = M.write_local_config(node_id, config_type, content)
            if ok then
                results.files[#results.files + 1] = dest
            else
                results.ok = false
                results.errors[#results.errors + 1] = "write " .. fname .. ": " .. (we or "")
            end
        end
    end

    -- 写入密钥
    local keys = bundle.keys or (bundle.data and bundle.data.keys) or {}
    local ed_keys = keys.ed25519 or {}
    local ed_dir = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, self_nid)
    util.ensure_dir(ed_dir)
    local key_count = 0
    for peer_id, pub_hex in pairs(ed_keys) do
        if type(pub_hex) == "table" then pub_hex = pub_hex.public_key end
        if pub_hex and pub_hex ~= "" then
            pub_hex = util.trim(pub_hex)
            if is_valid_ed25519_pub(pub_hex) then
                util.write_file(string.format("%s/%s.public", ed_dir, peer_id), pub_hex)
                key_count = key_count + 1
            end
        end
    end
    if key_count > 0 then
        results.files[#results.files + 1] = string.format("peer_keys(%d)", key_count)
    end

    -- 写入自身密钥（如果 bundle 提供了）
    local sec = keys.security or {}
    if sec.private and sec.private ~= "" then
        M.save_private_key(node_id, sec.private)
    end
    if sec.public and sec.public ~= "" then
        M.save_public_key(node_id, sec.public)
    end

    -- 补全 route.conf 中缺失的 peer 公钥（bundle 通常只含少量）
    local pk_result = M.refresh_peer_keys_batch(node_id)
    if pk_result.count > 0 then
        results.files[#results.files + 1] = string.format("peer_keys_api(%d)", pk_result.count)
    end

    return results
end

-- ─────────────────────────────────────────────────────────────
-- 获取单个节点详情
-- 对应 Go: GET /nodes/{id}
-- 返回: (node_table, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.get_single_node(node_id)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    local endpoint = string.format("/nodes/%s", nid(node_id))

    local data, api_err = api.get_json(cfg.get_api_url(), endpoint, current.token, zone_id)
    if api_err then return nil, api_err end
    if not data or not data.success then
        return nil, (data and data.message) or "failed to get node"
    end
    return data.data, nil
end

-- ─────────────────────────────────────────────────────────────
-- 读取本地配置文件（从磁盘，不调用 API）
-- 返回: { node_conf, route_conf, address_conf }
-- ─────────────────────────────────────────────────────────────
function M.read_local_configs(node_id)
    local gnb_conf_root  = cfg.get_gnb_conf_root()
    local node_conf_path = string.format("%s/%s/node.conf", gnb_conf_root, nid(node_id))
    local route_path     = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, nid(node_id))
    local addr_path      = string.format("%s/%s/address.conf", util.GNB_CONF_DIR, nid(node_id))

    local node_conf      = util.trim(util.read_file(node_conf_path) or "")
    local route_conf     = util.trim(util.read_file(route_path) or "")
    local address_conf   = util.trim(util.read_file(addr_path) or "")
    return {
        node_conf       = node_conf,
        route_conf      = route_conf,
        address_conf    = address_conf,
        node_conf_path  = node_conf_path,
        route_path      = route_path,
        addr_path       = addr_path,
        node_conf_ok    = node_conf ~= "",
        route_conf_ok   = route_conf ~= "",
        address_conf_ok = address_conf ~= "",
    }
end

-- ─────────────────────────────────────────────────────────────
-- 私钥管理（ed25519 hex 格式，存于 {GNB_CONF_DIR}/{node_id}/security/{node_id}.private）
-- ─────────────────────────────────────────────────────────────

function M.get_private_key(node_id)
    local path = string.format("%s/%s/security/%s.private",
        util.GNB_CONF_DIR, nid(node_id), nid(node_id))
    local raw = util.read_file(path)
    if not raw then return nil, "file not found: " .. path end
    return util.trim(raw), nil
end

-- 保存私钥（校验 hex 格式，写文件 chmod 600）
-- 返回: (true, nil) 或 (nil, error_string)
function M.save_private_key(node_id, key_hex)
    if not key_hex or key_hex == "" then
        return nil, "key is empty"
    end
    -- 去除空白
    key_hex = key_hex:gsub("%s+", "")
    -- 校验：只含 0-9a-fA-F，长度 128（GNB ed25519 私钥 64 字节 = 128 hex chars）
    if not key_hex:match("^[0-9a-fA-F]+$") then
        return nil, "invalid hex characters in key"
    end
    if #key_hex ~= 128 then
        return nil, string.format("key length error: expected 128 hex chars (GNB private key), got %d", #key_hex)
    end

    local dir  = string.format("%s/%s/security", util.GNB_CONF_DIR, nid(node_id))
    local path = string.format("%s/%s.private", dir, nid(node_id))
    util.ensure_dir(dir)
    local ok, werr = util.write_file_secure(path, key_hex .. "\n")
    if not ok then return nil, werr end
    return true, nil
end

-- 保存公鑰到 security 目录（写入 {GNB_CONF_DIR}/{node_id}/security/{node_id}.public）
-- 同时写入 ed25519 目录中自身公鑰
function M.save_public_key(node_id, pub_hex)
    if not pub_hex or pub_hex == "" then return nil, "pub_hex is empty" end
    pub_hex = pub_hex:gsub("%s+", "")
    if not pub_hex:match("^[0-9a-fA-F]+$") then
        return nil, "invalid hex in public key"
    end

    local sec_dir = string.format("%s/%s/security", util.GNB_CONF_DIR, nid(node_id))
    util.ensure_dir(sec_dir)
    local sec_path = string.format("%s/%s.public", sec_dir, nid(node_id))
    local ok, werr = util.write_file(sec_path, pub_hex)
    if not ok then return nil, werr end

    -- 同时写入 ed25519 目录（gnb 运行时从此处读取公钥）
    local ed_dir = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, nid(node_id))
    util.ensure_dir(ed_dir)
    local ed_path = string.format("%s/%s.public", ed_dir, nid(node_id))
    util.write_file(ed_path, pub_hex)

    return true, nil
end

-- 返回: [{ peer_id, key_hex }]
-- ─────────────────────────────────────────────────────────────
function M.get_peer_keys(node_id)
    local dir     = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, nid(node_id))
    local self_id = nid(node_id)
    local result  = {}

    -- 枚举目录下的 .public 文件
    local ls_out  = util.exec("ls " .. dir .. "/*.public 2>/dev/null") or ""
    for path in ls_out:gmatch("[^\n]+") do
        path = util.trim(path)
        if path ~= "" then
            local fname = path:match("([^/]+)$") or ""
            if fname:match("%.public$") then
                local peer_id = fname:gsub("%.public$", "")
                -- 排除自身节点
                if peer_id ~= self_id then
                    local raw = util.read_file(path)
                    if raw then
                        result[#result + 1] = {
                            peer_id = peer_id,
                            key_hex = util.trim(raw),
                        }
                    end
                end
            end
        end
    end
    return result
end

-- Proxy marker constants（与 proxy.lua / hooks/stop.sh 统一）
local PROXY_MARKER_BEGIN        = "#----proxy start----"
local PROXY_MARKER_END          = "#----proxy end----"
local PROXY_SERVER_MARKER_BEGIN = "#----proxy-server start----"
local PROXY_SERVER_MARKER_END   = "#----proxy-server end----"

-- 兼容新旧两种 proxy marker 格式：
--   新格式: #----proxy start----
--   旧格式: #---------proxy start {nodeId}--------
local function is_proxy_begin(line)
    if line:sub(1, 1) ~= "#" then return false end
    return line:find(PROXY_MARKER_BEGIN, 1, true)
        or line:find(PROXY_SERVER_MARKER_BEGIN, 1, true)
        or line:find("proxy start", 1, true)
end
local function is_proxy_end(line)
    if line:sub(1, 1) ~= "#" then return false end
    return line:find(PROXY_MARKER_END, 1, true)
        or line:find(PROXY_SERVER_MARKER_END, 1, true)
        or line:find("proxy end", 1, true)
end

local function update_route_skip_state(line, skipping)
    if is_proxy_begin(line) then return true end
    if is_proxy_end(line) then return false end
    return skipping
end

-- ─────────────────────────────────────────────────────────────
-- 从 route.conf 内容中提取对端节点 ID 列表（格式: nodeID|network|netmask）
-- 排除自身节点 ID，去重；跳过 proxy marker 段
-- ─────────────────────────────────────────────────────────────
local function extract_peer_ids_from_route(route_content, self_nid)
    local seen     = {}
    local peers    = {}
    local skipping = false
    for line in (route_content .. "\n"):gmatch("([^\n]*)\n") do
        line = line:match("^%s*(.-)%s*$") -- trim
        local next_skipping = update_route_skip_state(line, skipping)
        if next_skipping ~= skipping then
            skipping = next_skipping
        elseif not skipping and line ~= "" and not line:match("^#") then
            local peer_id = line:match("^([^|]+)|")
            if peer_id then
                peer_id = peer_id:match("^%s*(.-)%s*$")
                if peer_id ~= "" and peer_id ~= self_nid and not seen[peer_id] then
                    seen[peer_id]     = true
                    peers[#peers + 1] = peer_id
                end
            end
        end
    end
    return peers
end

-- ─────────────────────────────────────────────────────────────
-- 解析 GNB route.conf → 结构化条目
-- 格式: nodeId|network|netmask 或 n|nodeId|network|netmask
-- ─────────────────────────────────────────────────────────────
local function parse_gnb_route_conf(content)
    local entries = {}
    local skipping = false
    for line in (content .. "\n"):gmatch("([^\n]*)\n") do
        line = line:match("^%s*(.-)%s*$")
        local next_skipping = update_route_skip_state(line, skipping)
        if next_skipping ~= skipping then
            skipping = next_skipping
        elseif not skipping and line ~= "" and not line:match("^#") then
            local parts = {}
            for p in line:gmatch("[^|]+") do
                parts[#parts + 1] = p:match("^%s*(.-)%s*$")
            end
            local node_id, network, netmask
            if #parts >= 4 then
                node_id = parts[2]; network = parts[3]; netmask = parts[4]
            elseif #parts == 3 then
                node_id = parts[1]; network = parts[2]; netmask = parts[3]
            end
            if node_id and network and netmask then
                entries[#entries + 1] = {
                    node_id = node_id, network = network, netmask = netmask
                }
            end
        end
    end
    return entries
end

-- ─────────────────────────────────────────────────────────────
-- 从 route.conf 获取本地对端节点列表
-- 返回: { { node_id=str, virtual_ip=str, is_self=bool }, ... }
-- ─────────────────────────────────────────────────────────────
function M.get_local_peers(node_id)
    local self_nid = nid(node_id)
    local route_path = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, self_nid)
    local route_content = util.trim(util.read_file(route_path) or "")
    if route_content == "" then return {} end

    local entries = parse_gnb_route_conf(route_content)
    -- 按 node_id 分组，取每组第一条记录（VPN IP）
    local seen = {}
    local result = {}
    for _, e in ipairs(entries) do
        if not seen[e.node_id] then
            seen[e.node_id] = true
            result[#result + 1] = {
                node_id    = e.node_id,
                virtual_ip = e.network,
                is_self    = (e.node_id == self_nid),
            }
        end
    end
    return result
end

-- ─────────────────────────────────────────────────────────────
-- 从服务器拉取对端节点公钥并写入 ed25519 目录
-- 只拉取 route.conf 中出现的对端节点（而非全部节点），与 mynet_tui 行为一致
-- 写法：{GNB_CONF_DIR}/{node_id}/ed25519/{peer_id}.public（内容不含换行）
-- 返回: { ok, count, errors=[] }
-- ─────────────────────────────────────────────────────────────
function M.refresh_peer_keys(node_id)
    local current, zone_id, err = auth_context()
    if err then return { ok = false, count = 0, errors = { "auth: " .. err } } end
    local self_nid = nid(node_id)

    -- 从本地 route.conf 提取对端 node ID
    local route_path = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, self_nid)
    local route_content = util.trim(util.read_file(route_path) or "")
    local peer_ids = extract_peer_ids_from_route(route_content, self_nid)

    local ed25519_dir = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, self_nid)
    util.ensure_dir(ed25519_dir)

    local count  = 0
    local errors = {}
    for _, peer_id in ipairs(peer_ids) do
        local keys_ep = string.format("/nodes/%s/keys", peer_id)
        local kdata, kerr = api.get_json(cfg.get_api_url(), keys_ep, current.token, zone_id)
        local pub_hex = nil
        if not kerr and kdata then
            local d = (kdata.data) or {}
            pub_hex = (d.key and d.key.public_key)
                or d.public_key
                or (d.keys and #d.keys > 0 and d.keys[1].public_key)
        end
        if pub_hex and pub_hex ~= "" then
            pub_hex = util.trim(pub_hex)
            -- 验证：64 hex chars（GNB ed25519 公钥 32 字节）
            if is_valid_ed25519_pub(pub_hex) then
                local key_path = string.format("%s/%s.public", ed25519_dir, peer_id)
                -- 写入时不加换行，与 mynet_tui 行为一致
                util.write_file(key_path, pub_hex)
                count = count + 1
            else
                errors[#errors + 1] = "peer " .. peer_id .. ": invalid public key (len=" .. #pub_hex .. ")"
            end
        else
            errors[#errors + 1] = "peer " .. peer_id .. ": " .. (kerr or "no public_key in response")
        end
    end
    return { ok = true, count = count, errors = errors }
end

-- ─────────────────────────────────────────────────────────────
-- 批量获取 peer 公钥（POST /nodes/{id}/router-keys）
-- 优先使用批量 API，若后端不支持则回退到逐个获取
-- 返回: { ok, count, errors=[], method="batch"|"legacy" }
-- ─────────────────────────────────────────────────────────────
function M.refresh_peer_keys_batch(node_id)
    local current, zone_id, err = auth_context()
    if err then return { ok = false, count = 0, errors = { "auth: " .. err }, method = "legacy" } end
    local self_nid = nid(node_id)

    local kdata, kerr = api.get_router_keys(
        cfg.get_api_url(), self_nid, current.token, zone_id)

    if kerr == "not_supported" or not kdata then
        util.log_info("node", "router-keys API not supported, fallback to per-peer fetch")
        local r = M.refresh_peer_keys(node_id)
        r.method = "legacy"
        return r
    end

    -- 解析响应：兼容 {peer_id: "hex"} 和 {peer_id: {public_key: "hex"}}
    local keys = kdata.data or kdata.keys or kdata
    if type(keys) ~= "table" then
        local r = M.refresh_peer_keys(node_id)
        r.method = "legacy"
        return r
    end

    local ed25519_dir = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, self_nid)
    util.ensure_dir(ed25519_dir)

    local count  = 0
    local errors = {}
    for peer_id, v in pairs(keys) do
        local pub_hex = (type(v) == "string") and v or (type(v) == "table" and v.public_key or nil)
        if pub_hex and pub_hex ~= "" then
            pub_hex = util.trim(pub_hex)
            if is_valid_ed25519_pub(pub_hex) then
                util.write_file(string.format("%s/%s.public", ed25519_dir, peer_id), pub_hex)
                count = count + 1
            else
                errors[#errors + 1] = "peer " .. tostring(peer_id) .. ": invalid key (len=" .. #pub_hex .. ")"
            end
        end
    end
    return { ok = true, count = count, errors = errors, method = "batch" }
end

-- ─────────────────────────────────────────────────────────────
-- 刷新单个配置文件（按类型：node / route / address）
-- 返回: { ok, file, error }
-- ─────────────────────────────────────────────────────────────
function M.refresh_single_config(node_id, config_type)
    if config_type == "node" then
        local text, err = M.get_node_config_rendered(node_id)
        if err then
            local node_conf_path = resolve_local_config_path(node_id, "node")
            return { ok = false, file = node_conf_path, error = err }
        end
        local ok, we, node_conf_path = M.write_local_config(node_id, "node", text)
        return { ok = ok, file = node_conf_path, error = we }
    elseif config_type == "route" then
        local text, err = M.get_route_config_rendered(node_id)
        if err then
            local route_path = resolve_local_config_path(node_id, "route")
            return { ok = false, file = route_path, error = err }
        end
        if not text or util.trim(text) == "" then
            local route_path = resolve_local_config_path(node_id, "route")
            return {
                ok = false,
                file = route_path,
                error = "API returned empty route.conf content"
            }
        end
        local ok, we, route_path = M.write_local_config(node_id, "route", text)
        if ok then
            -- route 更新后批量同步对端公钥
            local pk_result = M.refresh_peer_keys_batch(node_id)
            util.log_warn(string.format("refresh_single_config(route): synced %d peer keys", pk_result.count))
        end
        return { ok = ok, file = route_path, error = we }
    elseif config_type == "address" then
        local text = guest_address_text()
        if not text then
            local err
            text, err = M.get_services_index()
            if err then
                local addr_path = resolve_local_config_path(node_id, "address")
                return { ok = false, file = addr_path, error = err }
            end
        end
        if not text or text == "" then
            local addr_path = resolve_local_config_path(node_id, "address")
            return {
                ok = false,
                file = addr_path,
                error = "API returned empty content (zone may have no index servers configured)"
            }
        end
        local ok, we, addr_path = M.write_local_config(node_id, "address", text)
        return { ok = ok, file = addr_path, error = we }
    else
        return { ok = false, file = "", error = "unknown config type: " .. tostring(config_type) }
    end
end

-- ─────────────────────────────────────────────────────────────
-- VPN 服务控制（/etc/init.d/mynet）
-- ─────────────────────────────────────────────────────────────

function M.get_vpn_service_status()
    -- 通过 pidfile + cmdline 验证（多实例安全，不使用全局 pidof）
    local node_id = cfg.get_node_id()
    if node_id and node_id ~= "0" then
        if M.gnb_is_running(node_id) then return "running" end
    end
    return "stopped"
end

function M.start_vpn()
    local output, code = util.exec_status("/etc/init.d/mynet start 2>&1")
    return output, code
end

function M.stop_vpn()
    local output, code = util.exec_status("/etc/init.d/mynet stop 2>&1")
    return output, code
end

function M.restart_vpn()
    local output, code = util.exec_status("/etc/init.d/mynet restart 2>&1")
    return output, code
end

-- ─────────────────────────────────────────────────────────────
-- GNB 服务状态机
-- 状态: STOPPED → STARTING → RUNNING → STOPPING → STOPPED / ERROR
-- 状态持久化到 /etc/mynet/var/gnb_state.json（跨请求保持）
-- ─────────────────────────────────────────────────────────────

M.SVC_STATE = {
    STOPPED  = "stopped",
    STARTING = "starting",
    RUNNING  = "running",
    STOPPING = "stopping",
    ERROR    = "error",
}

local GNB_STATE_FILE = util.MYNET_HOME .. "/var/gnb_state.json"

local function load_svc_state()
    return util.load_json_file(GNB_STATE_FILE) or { state = M.SVC_STATE.STOPPED }
end

local function save_svc_state(state, detail)
    util.ensure_dir(util.MYNET_HOME .. "/var")
    util.save_json_file(GNB_STATE_FILE, {
        state   = state,
        detail  = detail or "",
        updated = util.format_time(util.time_now()),
    })
end

function M.get_svc_state()
    local s = load_svc_state()
    -- 若声称 running 则验证进程确实存在
    if s.state == M.SVC_STATE.RUNNING then
        local node_id = cfg.get_node_id()
        if node_id and not M.gnb_is_running(node_id) then
            s.state = M.SVC_STATE.ERROR
            s.detail = "process not found (crashed?)"
            save_svc_state(s.state, s.detail)
        end
    end
    return s
end

-- ─────────────────────────────────────────────────────────────
-- Pre-flight 校验器（对齐 client checker.js）
-- 返回: { ok=bool, checks=[{name, ok, detail}] }
-- ─────────────────────────────────────────────────────────────
function M.preflight_check(node_id)
    local checks = {}
    local all_ok = true
    local n = nid(node_id)

    local function add(name, ok, detail)
        checks[#checks + 1] = { name = name, ok = ok, detail = detail or "" }
        if not ok then all_ok = false end
    end

    -- 1. gnb 二进制
    local gnb_bin = cfg.get_gnb_bin()
    add("gnb_binary", util.file_exists(gnb_bin),
        util.file_exists(gnb_bin) and gnb_bin or "not found: " .. gnb_bin)

    -- 2. node.conf 存在且含 nodeid
    local node_conf_path = string.format("%s/%s/node.conf", cfg.get_gnb_conf_root(), n)
    local node_conf = util.read_file(node_conf_path) or ""
    local has_nodeid = node_conf:match("nodeid%s") ~= nil
    add("node_conf", node_conf ~= "" and has_nodeid,
        node_conf == "" and "missing: " .. node_conf_path
        or (not has_nodeid and "no 'nodeid' in node.conf" or "ok"))

    -- 2b. node.conf nodeid 与当前 node_id 一致性
    local conf_nid_str = node_conf:match("nodeid%s+(%d+)")
    if conf_nid_str and conf_nid_str ~= n then
        add("node_conf_id_match", false,
            "node.conf nodeid=" .. conf_nid_str .. " but current node is #" .. n)
    end

    -- 3. route.conf 存在（非空）
    local route_path = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, n)
    local route_conf = util.read_file(route_path) or ""
    add("route_conf", route_conf ~= "",
        route_conf == "" and "missing: " .. route_path or "ok")

    -- 3b. route.conf 第一条记录的 nodeid 与当前 node_id 一致性
    if route_conf ~= "" then
        for line in route_conf:gmatch("[^\n]+") do
            local stripped = line:gsub("#.*$", ""):match("^%s*(.-)%s*$")
            if stripped ~= "" then
                local first_field = stripped:match("^([^|]+)")
                if first_field then
                    first_field = first_field:match("^%s*(.-)%s*$")
                    if first_field ~= n then
                        add("route_conf_id_match", false,
                            "route.conf first node=" .. first_field .. " but current node is #" .. n)
                    end
                end
                break
            end
        end
    end

    -- 4. address.conf（空内容为 warning，不阻塞启动）
    local addr_path = string.format("%s/%s/address.conf", util.GNB_CONF_DIR, n)
    local addr_conf = util.read_file(addr_path) or ""
    local addr_has_record = false
    for line in addr_conf:gmatch("[^\r\n]+") do
        local trimmed = line:match("^%s*(.-)%s*$")
        if trimmed ~= "" and trimmed:sub(1, 1) ~= "#" then
            addr_has_record = true
            break
        end
    end
    if not addr_has_record then
        checks[#checks + 1] = {
            name = "address_conf",
            ok = true,
            detail = "warning: no records in " .. addr_path
        }
    else
        checks[#checks + 1] = { name = "address_conf", ok = true, detail = "ok" }
    end

    -- 5. 私钥存在（128 hex chars）
    local priv_path = string.format("%s/%s/security/%s.private", util.GNB_CONF_DIR, n, n)
    local priv_hex  = util.trim(util.read_file(priv_path) or "")
    local priv_ok   = #priv_hex == 128 and priv_hex:match("^[0-9a-fA-F]+$") ~= nil
    add("private_key", priv_ok,
        not priv_ok and "missing or invalid: " .. priv_path or "ok")

    -- 5b. 公钥存在（64 hex chars）
    local pub_path = string.format("%s/%s/security/%s.public", util.GNB_CONF_DIR, n, n)
    local pub_hex  = util.trim(util.read_file(pub_path) or "")
    local pub_ok   = is_valid_ed25519_pub(pub_hex)
    add("public_key", pub_ok,
        not pub_ok and "missing or invalid: " .. pub_path or "ok")

    -- 6. 至少一个 peer 公钥
    local ed_dir = string.format("%s/%s/ed25519", util.GNB_CONF_DIR, n)
    local pk_out = util.exec("ls " .. ed_dir .. "/*.public 2>/dev/null | wc -l") or "0"
    local pk_count = tonumber(util.trim(pk_out)) or 0
    add("peer_keys", pk_count > 0,
        pk_count > 0 and pk_count .. " peer(s)" or "no peer keys in " .. ed_dir)

    -- 7. kmod-tun
    local tun_ok = util.file_exists("/dev/net/tun")
    if not tun_ok then
        local lsmod = util.trim(util.exec("lsmod 2>/dev/null | grep -w tun") or "")
        tun_ok = lsmod ~= ""
    end
    add("kmod_tun", tun_ok,
        tun_ok and "ok" or "/dev/net/tun missing — opkg install kmod-tun")

    return { ok = all_ok, checks = checks }
end

-- ─────────────────────────────────────────────────────────────
-- IP 工具函数（对齐 mynet_client network-routes.js）
-- ─────────────────────────────────────────────────────────────
local function ip_to_int(ip)
    local a, b, c, d = ip:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
    if not a then return 0 end
    a, b, c, d = tonumber(a), tonumber(b), tonumber(c), tonumber(d)
    return a * 16777216 + b * 65536 + c * 256 + d
end

local function int_to_ip(n)
    return string.format("%d.%d.%d.%d",
        math.floor(n / 16777216) % 256,
        math.floor(n / 65536) % 256,
        math.floor(n / 256) % 256,
        n % 256)
end

local function netmask_to_cidr(mask)
    if not mask or mask == "" then return 0 end
    if mask:sub(1, 1) == "/" then return tonumber(mask:sub(2)) or 0 end
    local bits = 0
    for octet in mask:gmatch("(%d+)") do
        local n = tonumber(octet) or 0
        while n > 0 do
            bits = bits + (n % 2)
            n = math.floor(n / 2)
        end
    end
    return bits
end

local function network_addr(ip, prefix)
    local n = ip_to_int(ip)
    if prefix == 0 then return "0.0.0.0" end
    -- Lua 不支持 32 位左移，用乘法模拟
    local mask = 0
    for i = 31, 32 - prefix, -1 do
        mask = mask + 2 ^ i
    end
    -- bit-and via floor
    local result = math.floor(n / (2 ^ (32 - prefix))) * (2 ^ (32 - prefix))
    return int_to_ip(result)
end

-- ─────────────────────────────────────────────────────────────
-- 构建路由表（对齐 mynet_client buildRouteTable）
-- 返回: { peer_routes, gw_routes, self_vpn_ip, vpn_subnet }
-- ─────────────────────────────────────────────────────────────
local function build_route_table(entries, self_nid)
    local self_entry
    for _, e in ipairs(entries) do
        if e.node_id == self_nid then
            self_entry = e; break
        end
    end
    local self_vpn_ip = self_entry and self_entry.network or nil
    local vpn_mask = 0xFFFFFF00 -- /24
    local vpn_net = self_vpn_ip and math.floor(ip_to_int(self_vpn_ip) / 256) * 256 or 0
    local vpn_subnet = self_vpn_ip
        and (int_to_ip(vpn_net) .. "/24") or nil

    -- 按 node_id 分组（保持插入顺序）
    local by_node = {}
    local node_order = {}
    for _, e in ipairs(entries) do
        if e.node_id ~= self_nid then
            if not by_node[e.node_id] then
                by_node[e.node_id] = {}
                node_order[#node_order + 1] = e.node_id
            end
            local t = by_node[e.node_id]
            t[#t + 1] = e
        end
    end

    local peer_routes = {} -- cross-zone /32 via selfVpnIp
    local gw_routes = {}   -- subnet via peerVpnIp

    for _, nid_key in ipairs(node_order) do
        local node_entries = by_node[nid_key]
        local peer_vpn_ip = node_entries[1].network

        -- 同子网判断
        local in_same_subnet = self_vpn_ip and
            (math.floor(ip_to_int(peer_vpn_ip) / 256) * 256 == vpn_net)

        if not in_same_subnet and self_vpn_ip then
            peer_routes[#peer_routes + 1] = {
                node_id = nid_key,
                network = peer_vpn_ip,
                cidr    = peer_vpn_ip .. "/32",
                gateway = self_vpn_ip,
            }
        end

        -- 后续条目 = peer 背后的子网
        for i = 2, #node_entries do
            local e = node_entries[i]
            local prefix = netmask_to_cidr(e.netmask)
            local net = network_addr(e.network, prefix)
            gw_routes[#gw_routes + 1] = {
                node_id = nid_key,
                network = net,
                cidr    = net .. "/" .. prefix,
                gateway = peer_vpn_ip,
            }
        end
    end

    return {
        peer_routes = peer_routes,
        gw_routes   = gw_routes,
        self_vpn_ip = self_vpn_ip,
        vpn_subnet  = vpn_subnet,
    }
end

-- ─────────────────────────────────────────────────────────────
-- 生成 /etc/mynet/conf/route.conf（OS 级路由配置）
-- 从 GNB route.conf 解析路由条目，过滤 proxy 段，输出 <cidr> via <gw>
-- ─────────────────────────────────────────────────────────────
function M.generate_route_conf(node_id)
    local n = nid(node_id)
    local route_path = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, n)
    local route_content = util.trim(util.read_file(route_path) or "")
    if route_content == "" then
        return nil, "route.conf is empty"
    end

    -- 剔除 proxy 注入段（route_inject 已将策略路由追加到此文件，
    -- 必须在生成 OS 级 route.conf 前移除，否则 /8、/16 大段路由
    -- 会被误认为子网路由写入 conf/route.conf 并由 route.mynet 添加）
    local proxy_m = require("luci.model.mynet.proxy")
    route_content = proxy_m.strip_route_injections(route_content)

    local entries = parse_gnb_route_conf(route_content)
    if #entries == 0 then
        return nil, "no routes parsed from route.conf"
    end

    local vpn_iface = cfg.get_vpn_interface()

    local rt = build_route_table(entries, n)
    local lines = {
        "# MyNet Network Routes — auto-generated",
        "# Node: " .. n .. " (" .. (rt.self_vpn_ip or "unknown") .. ")",
        "# VPN Subnet: " .. (rt.vpn_subnet or "unknown"),
        "# Updated: " .. os.date("!%Y-%m-%dT%H:%M:%SZ"),
        "# Format: peer /32 = dev <iface>; subnet = via <peer_vpn_ip>",
        "#!self_vpn_ip=" .. (rt.self_vpn_ip or ""),
        "",
    }

    -- Peer /32 routes: 直连 dev，建立 peer VPN IP 的可达性
    if #rt.peer_routes > 0 then
        lines[#lines + 1] = "# Cross-zone peer relay routes (dev — establish reachability)"
        for _, r in ipairs(rt.peer_routes) do
            lines[#lines + 1] = "# Peer " .. r.node_id .. " (" .. r.network .. ")"
            lines[#lines + 1] = r.cidr .. " dev " .. vpn_iface
        end
        lines[#lines + 1] = ""
    end

    -- Subnet routes: via peer_vpn_ip，内核通过已有的 /32 dev 路由找到下一跳
    if #rt.gw_routes > 0 then
        lines[#lines + 1] = "# Subnet routes (via peer gateway)"
        local last_nid = ""
        for _, r in ipairs(rt.gw_routes) do
            if r.node_id ~= last_nid then
                lines[#lines + 1] = "# Node " .. r.node_id .. " (" .. r.gateway .. ")"
                last_nid = r.node_id
            end
            lines[#lines + 1] = r.cidr .. " via " .. r.gateway
        end
    end

    local content = table.concat(lines, "\n") .. "\n"

    -- 写入 /etc/mynet/conf/route.conf（供 rc.mynet + service tab 使用）
    util.ensure_dir(util.CONF_DIR)
    local route_conf_path = util.CONF_DIR .. "/route.conf"
    local ok, we = util.write_file(route_conf_path, content)
    if not ok then return nil, "write route.conf: " .. (we or "") end

    return route_conf_path, nil
end

-- ─────────────────────────────────────────────────────────────
-- 直接启动/停止 GNB 进程（带状态机 + pre-flight）
-- 命令：{GNB_BIN_DIR}/gnb -c {GNB_CONF_DIR}/{nid}
-- ─────────────────────────────────────────────────────────────
local function gnb_pidfile(node_id)
    return string.format("%s/%s/gnb.pid", util.GNB_CONF_DIR, nid(node_id))
end

-- 查找 gnb 主进程的 gnb_es 子进程 PID（通过 PPID 匹配）
local function find_child_gnb_es(gnb_pid)
    local pids = {}
    local out = util.exec("pidof gnb_es 2>/dev/null") or ""
    for pid_str in out:gmatch("%d+") do
        local ppid = util.trim(util.exec(
            "awk '{print $4}' /proc/" .. pid_str .. "/stat 2>/dev/null") or "")
        if ppid == tostring(gnb_pid) then
            pids[#pids + 1] = tonumber(pid_str)
        end
    end
    return pids
end

-- 通过二进制路径查找属于本 mynet 安装的 gnb_es（兜底：gnb 已崩溃时 PPID=1）
local function find_our_gnb_es_by_bin()
    local es_bin = util.GNB_BIN_DIR .. "/gnb_es"
    local pids = {}
    local out = util.exec("pidof gnb_es 2>/dev/null") or ""
    for pid_str in out:gmatch("%d+") do
        local exe = util.trim(util.exec(
            "readlink /proc/" .. pid_str .. "/exe 2>/dev/null") or "")
        if exe == es_bin then
            pids[#pids + 1] = tonumber(pid_str)
        end
    end
    return pids
end

-- 等待指定 gnb_es PID 列表退出，超时后仅 kill 这些特定进程
local function wait_and_kill_gnb_es(pids, timeout_sec)
    if #pids == 0 then return end
    for _ = 1, timeout_sec do
        local alive = false
        for _, pid in ipairs(pids) do
            local _, code = util.exec_status("kill -0 " .. pid)
            if code == 0 then
                alive = true; break
            end
        end
        if not alive then return end
        util.exec("sleep 1")
    end
    for _, pid in ipairs(pids) do
        local _, code = util.exec_status("kill -0 " .. pid)
        if code == 0 then
            util.log_warn("node", "gnb_es pid " .. pid .. " did not exit naturally, killing")
            util.exec("kill " .. pid .. " 2>/dev/null; true")
        end
    end
end

-- 按 conf 目录精确找 gnb 主进程 PID（多实例安全）。
-- 为什么必须有：pidfile 会失步（$! 写的是短命父进程、或上次 start 失败残留），
-- 只信 pidfile 会让 stop_gnb 空杀、start_gnb 误判「没在跑」再起第二个抢端口。
-- 安全红线：不用 pkill/killall，只按 /proc/*/cmdline 匹配本实例 conf 路径。
local function find_gnb_pid_by_conf(node_id)
    local conf_dir = string.format("%s/%s", util.GNB_CONF_DIR, nid(node_id))
    local out = util.exec("pidof gnb 2>/dev/null") or ""
    for pid_str in out:gmatch("%d+") do
        local cmdline = util.read_file("/proc/" .. pid_str .. "/cmdline") or ""
        if cmdline:find(conf_dir, 1, true) then
            return tonumber(pid_str)
        end
    end
    return nil
end

function M.gnb_is_running(node_id)
    local pidfile = gnb_pidfile(node_id)
    local pid_str = util.trim(util.read_file(pidfile) or "")
    if pid_str ~= "" then
        local pid = tonumber(pid_str)
        if pid then
            -- 验证 /proc/{pid}/cmdline 包含该节点的 conf 目录（多实例安全）
            local cmdline = util.read_file("/proc/" .. pid .. "/cmdline")
            if cmdline and cmdline ~= "" then
                local conf_dir = string.format("%s/%s", util.GNB_CONF_DIR, nid(node_id))
                if cmdline:find(conf_dir, 1, true) then
                    return true
                end
            end
        end
    end
    -- pidfile 失步时按 conf 路径扫描（见 find_gnb_pid_by_conf 注释）
    return find_gnb_pid_by_conf(node_id) ~= nil
end

function M.start_gnb(node_id)
    local n     = nid(node_id)

    -- Pre-flight config check
    local check = M.check_config(node_id)
    if not check.ok then
        return nil, "config check failed: " .. table.concat(check.errors, "; ")
    end

    local bin  = util.GNB_BIN_DIR .. "/gnb"
    local conf = string.format("%s/%s", util.GNB_CONF_DIR, n)
    local log  = conf .. "/gnb.log"
    local pid  = gnb_pidfile(node_id)

    if M.gnb_is_running(node_id) then
        save_svc_state(M.SVC_STATE.RUNNING, "already running")
        return nil, "gnb already running for node " .. n
    end

    -- route.conf 缺失时自动尝试刷新配置（在线模式从 API 拉取）
    local route_path = string.format("%s/%s/route.conf", util.GNB_CONF_DIR, n)
    local route_content = util.trim(util.read_file(route_path) or "")
    if route_content == "" then
        util.log_info("node", "route.conf missing for " .. n .. ", attempting refresh_configs")
        M.refresh_configs(node_id)
    end

    -- Pre-flight check
    local pf = M.preflight_check(node_id)
    if not pf.ok then
        local failed = {}
        for _, c in ipairs(pf.checks) do
            if not c.ok then failed[#failed + 1] = c.name .. ": " .. c.detail end
        end
        local reason = "pre-flight failed: " .. table.concat(failed, "; ")
        save_svc_state(M.SVC_STATE.ERROR, reason)
        return nil, reason
    end

    -- 状态 → STARTING
    save_svc_state(M.SVC_STATE.STARTING, "spawning gnb")

    -- 生成 /etc/mynet/conf/route.conf
    local rconf, rconf_err = M.generate_route_conf(node_id)
    if rconf then
        util.log_info("node", "generated " .. rconf)
    elseif rconf_err then
        util.log_warn("node", "route.conf generation: " .. rconf_err)
    end

    util.ensure_dir(conf)
    local cmd = string.format(
        "'%s' -c '%s' </dev/null >> '%s' 2>&1 & echo $! > '%s'",
        bin, conf, log, pid)
    util.exec(cmd)

    -- ⚠️ `$!` 可能不是最终 gnb PID（父进程退出/二次 fork 时会失步）。
    -- 等进程起来后按 conf 路径回写真实 PID，否则 stop_gnb 会空杀。
    for _ = 1, 5 do
        local real = find_gnb_pid_by_conf(node_id)
        if real then
            util.write_file(pid, tostring(real))
            break
        end
        util.exec("sleep 1")
    end

    -- 等待 TUN 接口就绪（最多 5 秒）
    local iface = cfg.get_vpn_interface()
    local tun_ready = false
    for _ = 1, 5 do
        util.exec("sleep 1")
        if not M.gnb_is_running(node_id) then
            save_svc_state(M.SVC_STATE.ERROR, "gnb exited immediately, check " .. log)
            return nil, "gnb process exited immediately, check " .. log
        end
        local link = util.exec("ip link show " .. iface .. " 2>/dev/null") or ""
        if link ~= "" then
            tun_ready = true
            break
        end
    end

    if tun_ready then
        save_svc_state(M.SVC_STATE.RUNNING, "gnb running, tun ready")
        util.log_info("node", "gnb started for node " .. n .. ", tun interface ready")
    else
        save_svc_state(M.SVC_STATE.RUNNING, "gnb running, tun not yet detected")
        util.log_warn("node", "gnb started but tun interface not detected within 5s")
    end

    return true, nil
end

function M.stop_gnb(node_id)
    save_svc_state(M.SVC_STATE.STOPPING, "killing gnb")
    local pidfile = gnb_pidfile(node_id)

    -- 先信 pidfile（cmdline 校验），失步则按 conf 路径扫——见 find_gnb_pid_by_conf
    local target = nil
    local pid_str = util.trim(util.read_file(pidfile) or "")
    if pid_str ~= "" then
        local pid_num = tonumber(pid_str)
        if pid_num then
            local cmdline = util.read_file("/proc/" .. pid_num .. "/cmdline") or ""
            local conf_dir = string.format("%s/%s", util.GNB_CONF_DIR, nid(node_id))
            if cmdline:find(conf_dir, 1, true) then
                target = pid_num
            end
        end
    end
    if not target then
        target = find_gnb_pid_by_conf(node_id)
    end

    local es_pids = {}
    if target then
        es_pids = find_child_gnb_es(target)
        util.exec("kill " .. target .. " 2>/dev/null; true")
        util.exec("sleep 1")
        util.exec("kill -9 " .. target .. " 2>/dev/null; true")
    end
    util.exec("rm -f '" .. pidfile .. "'")
    -- gnb_es 由 gnb fork；优先等待自然退出，超时后仅 kill 本实例的 gnb_es
    -- 若 PPID 方式未找到（gnb 已崩溃），按二进制路径匹配
    if #es_pids == 0 then
        es_pids = find_our_gnb_es_by_bin()
    end
    wait_and_kill_gnb_es(es_pids, 3)
    -- 注意：不删除 TUN 接口（接口在安装时创建，持久存在，start/stop 不影响）
    save_svc_state(M.SVC_STATE.STOPPED, "gnb stopped")
    return true, nil
end

function M.restart_gnb(node_id)
    M.stop_gnb(node_id)
    util.exec("sleep 1")
    return M.start_gnb(node_id)
end

-- ─────────────────────────────────────────────────────────────
-- 获取 VPN 网络接口状态（ip link）
-- 返回: { interface, state, mtu, flags } 或 nil
-- ─────────────────────────────────────────────────────────────
function M.get_vpn_interface_status()
    local iface = cfg.get_vpn_interface()
    if not iface or iface == "" then return nil end

    local out = util.exec("ip link show " .. iface .. " 2>/dev/null")
    if not out or out == "" then return nil end

    -- TUN/TAP 设备的 operstate 始终是 UNKNOWN（无物理链路层），
    -- 但 flags 含 UP 时实际工作正常，对用户显示为 "UP"
    local raw_state     = out:match("state%s+(%w+)") or "unknown"
    local flags         = out:match("<(.-)>") or ""
    local display_state = raw_state
    if raw_state == "UNKNOWN" and flags:find("UP") then
        display_state = "UP"
    end

    return {
        interface = iface,
        state     = display_state,
        mtu       = tonumber(out:match("mtu%s+(%d+)")),
        flags     = flags,
        rx_bytes  = nil,
        tx_bytes  = nil,
    }
end

-- ─────────────────────────────────────────────────────────────
-- 生成新 GNB 密钥对（调用 gnb_crypto -c，写入临时目录后读取）
-- 返回: { priv_hex, pub_hex }, nil  或  nil, error_string
-- ─────────────────────────────────────────────────────────────
function M.generate_key_pair()
    local gnc = util.GNB_BIN_DIR .. "/gnb_crypto"
    if not util.file_exists(gnc) then
        return nil, "gnb_crypto not found: " .. gnc
    end

    -- 创建临时目录
    local tmpd = util.trim(util.exec("mktemp -d 2>/dev/null") or "")
    if tmpd == "" then return nil, "mktemp failed" end

    local priv_f  = tmpd .. "/k.private"
    local pub_f   = tmpd .. "/k.public"

    local _, code = util.exec_status(
        string.format("'%s' -c -p '%s' -k '%s' 2>/dev/null", gnc, priv_f, pub_f))

    if code ~= 0 then
        os.execute("rm -rf '" .. tmpd .. "'")
        return nil, "gnb_crypto failed (exit=" .. tostring(code) .. ")"
    end

    local priv_hex = util.trim(util.read_file(priv_f) or "")
    local pub_hex  = util.trim(util.read_file(pub_f) or "")
    os.execute("rm -rf '" .. tmpd .. "'")

    if priv_hex == "" or pub_hex == "" then
        return nil, "key generation produced empty output"
    end
    return { priv_hex = priv_hex, pub_hex = pub_hex }, nil
end

-- ─────────────────────────────────────────────────────────────
-- 从服务器下载本节点公钥并写入本地（GET /nodes/{id}/keys）
-- 返回: pub_hex, nil  或  nil, error_string
-- ─────────────────────────────────────────────────────────────
function M.fetch_server_public_key(node_id)
    local current, zone_id, err = auth_context()
    if err then return nil, err end
    local endpoint = string.format("/nodes/%s/keys", nid(node_id))

    local kdata, kerr = api.get_json(cfg.get_api_url(), endpoint, current.token, zone_id)
    if kerr then return nil, kerr end

    local d = (kdata and kdata.data) or {}
    local pub_hex = (d.key and d.key.public_key)
        or d.public_key
        or (d.keys and #d.keys > 0 and d.keys[1].public_key)
    if not pub_hex or pub_hex == "" then
        return nil, "no public_key in server response"
    end
    pub_hex = util.trim(pub_hex)
    if not is_valid_ed25519_pub(pub_hex) then
        return nil, string.format("invalid public key from server (len=%d)", #pub_hex)
    end
    -- 写入 security/ 和 ed25519/ 目录
    local ok, werr = M.save_public_key(node_id, pub_hex)
    if not ok then return nil, werr end
    return pub_hex, nil
end

-- 上传公钥到服务器（POST /nodes/{id}/keys）
-- 返回: true, nil  或  nil, error_string
-- ─────────────────────────────────────────────────────────────────────────────
function M.upload_public_key(node_id, pub_hex)
    local current, zone_id, err = auth_context()
    if err then return nil, err end

    -- 若未传入 pub_hex，读取本地公钥文件
    if not pub_hex or pub_hex == "" then
        local pub_path = string.format("%s/%s/ed25519/%s.public",
            util.GNB_CONF_DIR, nid(node_id), nid(node_id))
        local raw = util.read_file(pub_path)
        if not raw or raw == "" then
            return nil, "public key file not found: " .. pub_path
        end
        pub_hex = raw:gsub("%s+", "")
    end

    local endpoint = string.format("/nodes/%s/keys", nid(node_id))

    local data, api_err = api.post_json(cfg.get_api_url(), endpoint,
        { public_key = pub_hex },
        current.token, zone_id)
    if api_err then return nil, api_err end
    if not data or not data.success then
        return nil, (data and data.message) or "upload public key failed"
    end
    return true, nil
end

-- ─────────────────────────────────────────────────────────────
-- 导入配置包 — 支持 tar.gz / tgz / gz / zip
-- ─────────────────────────────────────────────────────────────

--- 解压配置包到临时目录并验证内容
-- @param archive_path string  上传的压缩包路径（/tmp/ 下）
-- @param filename     string  原始文件名（用于检测格式）
-- @return table preview, nil | nil, string error
function M.import_preview(archive_path, filename)
    if not util.file_exists(archive_path) then
        return nil, "file not found"
    end

    local ext = (filename or ""):lower()
    local tmp_dir = "/tmp/mynet_node_import_" .. os.time()
    os.execute("rm -rf '" .. tmp_dir .. "' && mkdir -p '" .. tmp_dir .. "'")

    -- 按后缀解压
    local _, code
    if ext:match("%.zip$") then
        _, code = util.exec_status(
            "unzip -o -q '" .. archive_path .. "' -d '" .. tmp_dir .. "' 2>&1")
    else
        -- tar.gz / tgz / gz 统一用 tar xzf
        _, code = util.exec_status(
            "tar xzf '" .. archive_path .. "' -C '" .. tmp_dir .. "' 2>&1")
    end
    if code ~= 0 then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "failed to extract archive"
    end

    -- 递归搜索配置文件
    local function find_file(name)
        local r = util.trim(util.exec(
            "find '" .. tmp_dir .. "' -name '" .. name .. "' -type f 2>/dev/null | head -1") or "")
        return r ~= "" and r or nil
    end

    local node_conf_path = find_file("node.conf")
    if not node_conf_path then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "node.conf not found in archive"
    end

    -- 从 node.conf 提取 nodeid
    local node_content = util.read_file(node_conf_path) or ""
    local nodeid_str = node_content:match("^%s*nodeid%s+(%d+)")
        or node_content:match("\n%s*nodeid%s+(%d+)")
    if not nodeid_str then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "node.conf missing 'nodeid' line"
    end

    local has_route   = find_file("route.conf") ~= nil
    local has_address = find_file("address.conf") ~= nil
    local has_private = find_file(nodeid_str .. ".private") ~= nil
    local has_public  = find_file(nodeid_str .. ".public") ~= nil

    -- 统计对端公钥
    local peer_count  = 0
    local ed_result   = util.exec(
        "find '" .. tmp_dir .. "' -path '*/ed25519/*.public' -type f 2>/dev/null") or ""
    for _ in ed_result:gmatch("[^\n]+") do
        peer_count = peer_count + 1
    end

    -- 是否与当前节点不同
    local cur_nid = cfg.get_node_id()
    local cur_nid_str = cur_nid and util.int_str(cur_nid) or "0"
    local is_different_node = (cur_nid_str ~= "0" and cur_nid_str ~= nodeid_str)

    return {
        tmp_dir           = tmp_dir,
        node_id           = nodeid_str,
        has_node_conf     = true,
        has_route         = has_route,
        has_address       = has_address,
        has_private_key   = has_private,
        has_public_key    = has_public,
        peer_key_count    = peer_count,
        is_different_node = is_different_node,
        current_node_id   = cur_nid_str,
    }, nil
end

--- 确认导入：将临时目录中的文件复制到 GNB 配置目录
-- @param tmp_dir  string  import_preview 返回的临时目录
-- @param node_id  string  节点 ID（字符串）
-- @return bool, string|nil
function M.import_apply(tmp_dir, node_id)
    if not tmp_dir or not util.file_exists(tmp_dir) then
        return false, "temp dir not found (expired?)"
    end
    local nid_s    = util.int_str(node_id)
    local conf_dir = util.GNB_CONF_DIR .. "/" .. nid_s

    os.execute("mkdir -p '" .. conf_dir .. "/security'")
    os.execute("mkdir -p '" .. conf_dir .. "/ed25519'")

    local function copy_found(name, dest)
        local src = util.trim(util.exec(
            "find '" .. tmp_dir .. "' -name '" .. name .. "' -type f 2>/dev/null | head -1") or "")
        if src ~= "" then
            os.execute("cp -f '" .. src .. "' '" .. dest .. "'")
            return true
        end
        return false
    end

    copy_found("node.conf", conf_dir .. "/node.conf")
    copy_found("route.conf", conf_dir .. "/route.conf")
    copy_found("address.conf", conf_dir .. "/address.conf")
    copy_found(nid_s .. ".private", conf_dir .. "/security/" .. nid_s .. ".private")
    copy_found(nid_s .. ".public", conf_dir .. "/security/" .. nid_s .. ".public")
    os.execute("chmod 600 '" .. conf_dir .. "/security/" .. nid_s .. ".private' 2>/dev/null")

    -- 复制自身公钥到 ed25519/ 目录
    copy_found(nid_s .. ".public", conf_dir .. "/ed25519/" .. nid_s .. ".public")

    -- 复制对端公钥
    local ed_files = util.exec(
        "find '" .. tmp_dir .. "' -path '*/ed25519/*.public' -type f 2>/dev/null") or ""
    for ed_path in ed_files:gmatch("[^\n]+") do
        local fname = ed_path:match("([^/]+%.public)$")
        if fname then
            os.execute("cp -f '" .. ed_path .. "' '" .. conf_dir .. "/ed25519/" .. fname .. "'")
        end
    end

    -- 更新 mynet.conf（设置 NODE_ID）
    cfg.generate_mynet_conf(tonumber(node_id))

    -- 清理临时目录
    os.execute("rm -rf '" .. tmp_dir .. "'")
    return true, nil
end

-- ═════════════════════════════════════════════════════════════
-- 三层服务管理：GNB 进程 + 路由 + 防火墙
-- ═════════════════════════════════════════════════════════════

-- ── 辅助：从 node.conf 提取 tun 接口名 ──────────────────────

local function parse_tun_iface(node_id)
    local node_conf = string.format("%s/%s/node.conf", cfg.get_gnb_conf_root(), nid(node_id))
    local content = util.read_file(node_conf) or ""
    return content:match("ifname%s+(%S+)")
end

-- ── 辅助：等待 tun 接口出现 ──────────────────────────────────

local function wait_for_iface(ifname, timeout_s)
    timeout_s = timeout_s or 5
    for _ = 1, timeout_s do
        local out = util.trim(util.exec("ip link show " .. ifname .. " 2>/dev/null") or "")
        if out ~= "" then return true end
        os.execute("sleep 1")
    end
    return false
end

-- ── Layer 2: 路由管理 ────────────────────────────────────────

-- IP + Netmask → 网络基地址（纯整数运算，无需 bit 库）
local function ip_network_base(ip, netmask)
    local a1, a2, a3, a4 = ip:match("(%d+)%.(%d+)%.(%d+)%.(%d+)")
    local m1, m2, m3, m4 = netmask:match("(%d+)%.(%d+)%.(%d+)%.(%d+)")
    if not a1 or not m1 then return nil end
    local function band(a, m)
        a, m = tonumber(a), tonumber(m)
        return a - (a % (256 - m))
    end
    return string.format("%d.%d.%d.%d", band(a1, m1), band(a2, m2), band(a3, m3), band(a4, m4))
end

-- 路由写入统一走 L6 执行器 `mynet apply`（route-apply-unification.md）。
-- 此前这里直接 parse gnb route.conf + `ip route replace`，与桌面的
-- network.routes 路径分叉，且没有所有权/冲突闸 —— 现改为委托，
-- 策略（冲突过滤、只删自己、网关钉扎）只在 mynet/apply_plan.go 一处。
function M.apply_routes(node_id)
    local bin = util.mynet_bin()
    if not bin then
        return { ok = false, count = 0, errors = { "mynet binary not found (expect /usr/sbin/mynet)" } }
    end
    local out, code = util.exec_status(string.format(
        "MYNET_HOME='%s' '%s' apply --auto 2>&1", util.MYNET_HOME, bin))
    local ok = (code == 0)
    if not ok then
        util.log_warn("apply_routes: mynet apply --auto failed: " .. tostring(out))
    else
        util.log_info("apply_routes: mynet apply --auto ok (node " .. tostring(node_id) .. ")")
    end
    return { ok = ok, count = 0, errors = ok and {} or { tostring(out) } }
end

function M.clear_routes(node_id)
    local bin = util.mynet_bin()
    if not bin then
        util.log_warn("clear_routes: mynet binary not found")
        return
    end
    -- 只清隧道口上的路由（apply --clear 内部按 dev 过滤），不碰系统其他路由
    local _, code = util.exec_status(string.format(
        "MYNET_HOME='%s' '%s' apply --clear 2>&1", util.MYNET_HOME, bin))
    if code ~= 0 then
        util.log_warn("clear_routes: mynet apply --clear failed (node " .. tostring(node_id) .. ")")
    end
end

-- ── Layer 3: 防火墙管理（OpenWrt UCI） ───────────────────────

-- 查找 mynet zone 的 UCI 索引
local function find_fw_zone_index()
    for i = 0, 15 do
        local name = util.trim(util.exec(
            string.format("uci get firewall.@zone[%d].name 2>/dev/null", i)) or "")
        if name == "" then break end
        if name == "mynet" then return i end
    end
    return nil
end

function M.apply_firewall(node_id)
    local errors = {}
    local tun = parse_tun_iface(node_id)
    if not tun then
        return { ok = false, errors = { "cannot determine tun interface" } }
    end

    -- 启用 IP 转发
    util.exec("sysctl -w net.ipv4.ip_forward=1 2>/dev/null; true")

    -- 确保 mynet firewall zone 存在
    local zi = find_fw_zone_index()
    if not zi then
        -- 创建 mynet zone
        util.exec("uci add firewall zone 2>/dev/null")
        -- 重新查找索引
        local zones_out = util.trim(util.exec("uci show firewall 2>/dev/null | grep -c '=zone'") or "0")
        zi = (tonumber(zones_out) or 1) - 1
        util.exec(string.format("uci set firewall.@zone[%d].name='mynet'", zi))
    end

    -- 配置 zone
    local cmds = {
        string.format("uci set firewall.@zone[%d].network='mynet'", zi),
        string.format("uci set firewall.@zone[%d].input='ACCEPT'", zi),
        string.format("uci set firewall.@zone[%d].output='ACCEPT'", zi),
        string.format("uci set firewall.@zone[%d].forward='ACCEPT'", zi),
        string.format("uci set firewall.@zone[%d].masq='1'", zi),
        string.format("uci set firewall.@zone[%d].device='%s'", zi, tun),
    }
    for _, cmd in ipairs(cmds) do
        util.exec(cmd .. " 2>/dev/null; true")
    end

    -- 同步 network.mynet.device（确保与防火墙一致）
    util.exec("uci set network.mynet.device='" .. tun .. "' 2>/dev/null; true")

    -- 确保 lan→mynet forwarding 存在
    local has_fwd = false
    for i = 0, 30 do
        local src = util.trim(util.exec(
            string.format("uci get firewall.@forwarding[%d].src 2>/dev/null", i)) or "")
        local dest = util.trim(util.exec(
            string.format("uci get firewall.@forwarding[%d].dest 2>/dev/null", i)) or "")
        if src == "" then break end
        if src == "lan" and dest == "mynet" then
            has_fwd = true; break
        end
    end
    if not has_fwd then
        util.exec("uci add firewall forwarding 2>/dev/null")
        local fwd_out = util.trim(util.exec("uci show firewall 2>/dev/null | grep -c '=forwarding'") or "0")
        local fi = (tonumber(fwd_out) or 1) - 1
        util.exec(string.format("uci set firewall.@forwarding[%d].src='lan'", fi))
        util.exec(string.format("uci set firewall.@forwarding[%d].dest='mynet'", fi))
    end

    -- 提交并重载。后台执行：fw4 reload 在部分环境会挂死，不能拖住
    -- start_service / HTTP 请求（实测 QEMU 上 >45s）。回读状态用 fw4 status。
    util.exec("uci commit firewall 2>&1")
    util.exec("(fw4 reload 2>/dev/null || fw3 reload 2>/dev/null) >/tmp/mynet-fw4-reload.log 2>&1 &")

    util.log_info("apply_firewall: zone mynet configured for " .. tun)
    return { ok = #errors == 0, errors = errors }
end

function M.clear_firewall(node_id)
    -- 不删除 UCI zone（保守策略）。
    -- ⚠️ 不要在这里同步 fw4 reload：本机没改 UCI 时 reload 纯属多余，
    -- 而 fw4 reload 在部分环境（QEMU/规则异常）会挂死，会把 stop_service
    -- 拖到 HTTP 超时。真要刷新防火墙时由 apply_firewall 负责。
    return true
end

-- ── 配置校验 ─────────────────────────────────────────────────

function M.check_config(node_id)
    local n = nid(node_id)
    local errors = {}
    local warnings = {}

    -- node.conf
    local nc_path = string.format("%s/%s/node.conf", cfg.get_gnb_conf_root(), n)
    local nc = util.read_file(nc_path)
    if not nc or nc == "" then
        errors[#errors + 1] = "node.conf not found or empty"
    elseif not nc:find("nodeid") then
        errors[#errors + 1] = "node.conf missing nodeid directive"
    end

    -- address.conf
    local ac_path = string.format("%s/%s/address.conf", util.GNB_CONF_DIR, n)
    local ac = util.read_file(ac_path)
    if not ac or ac == "" then
        errors[#errors + 1] = "address.conf not found or empty"
    elseif not ac:find("^i|", 1) and not ac:find("\ni|") then
        warnings[#warnings + 1] = "address.conf has no index entries"
    end

    -- ed25519 自身公钥
    local pk_path = string.format("%s/%s/ed25519/%s.public", util.GNB_CONF_DIR, n, n)
    if not util.file_exists(pk_path) then
        errors[#errors + 1] = "own ed25519 public key not found"
    end

    -- security 私钥
    local sk_path = string.format("%s/%s/security/%s.private", util.GNB_CONF_DIR, n, n)
    if not util.file_exists(sk_path) then
        errors[#errors + 1] = "security private key not found"
    end

    -- gnb binary
    local bin = util.GNB_BIN_DIR .. "/gnb"
    if not util.file_exists(bin) then
        errors[#errors + 1] = "gnb binary not found"
    end

    return { ok = #errors == 0, errors = errors, warnings = warnings }
end

-- ── 三层联动：start_service / stop_service ──────────────────
-- E-1：进程生命周期归 procd。这里只委托 /etc/init.d/mynet，
-- 不再 Lua spawn/kill gnb（旧路径见 start_gnb/stop_gnb，仅留调试/兼容）。

function M.start_service(node_id)
    -- pre-flight：配置不齐时别让 procd 空转
    local check = M.check_config(node_id)
    if not check.ok then
        return nil, "config check failed: " .. table.concat(check.errors, "; ")
    end
    local out, code = util.exec_status("/etc/init.d/mynet start 2>&1")
    if code == 0 then
        return true, nil
    end
    return nil, (out ~= "" and out) or ("init mynet start failed code=" .. tostring(code))
end

function M.stop_service(node_id)
    local out, code = util.exec_status("/etc/init.d/mynet stop 2>&1")
    if code == 0 then
        return true, nil
    end
    -- stop 在服务未跑时也可能非 0；只要 gnb 已不在就算成功
    if not M.gnb_is_running(node_id) then
        return true, nil
    end
    return nil, (out ~= "" and out) or ("init mynet stop failed code=" .. tostring(code))
end

function M.restart_service(node_id)
    M.stop_service(node_id)
    os.execute("sleep 1")
    return M.start_service(node_id)
end

return M
