-- mynet/guest.lua  — GNB 离线（Guest）模式
-- 无需 MyNet 帐号，本地创建节点组网，生成密钥与配置。
-- 配置存储在与在线模式相同的 GNB 目录，兼容已有启停流程。

local M      = {}
local util   = require("luci.model.mynet.util")
local cfg    = require("luci.model.mynet.config")

local GUEST_FILE    = util.CONF_DIR .. "/guest.json"
local GNB_CONF_DIR  = util.GNB_CONF_DIR

local DEFAULT_SUBNET   = "10.1.0"
local DEFAULT_PORT     = 9001
local DEFAULT_START_ID = 1001

-- ─────────────────────────────────────────────────────────────
-- 基础读写
-- ─────────────────────────────────────────────────────────────

function M.load_config()
    return util.load_json_file(GUEST_FILE)
end

function M.save_config(data)
    return util.save_json_file(GUEST_FILE, data)
end

function M.is_initialized()
    local g = M.load_config()
    return g ~= nil and type(g.nodes) == "table" and #g.nodes > 0
end

-- ─────────────────────────────────────────────────────────────
-- 配置文件生成
-- ─────────────────────────────────────────────────────────────

local function gen_node_conf(node_id, listen_port, name)
    local lines = {}
    if name and name ~= "" then
        lines[#lines + 1] = string.format("# %s", name)
    end
    lines[#lines + 1] = string.format("nodeid %d", node_id)
    lines[#lines + 1] = string.format("listen %d", listen_port)
    return table.concat(lines, "\n") .. "\n"
end

local function gen_route_conf(self_id, all_nodes)
    local self_line = nil
    local lines = {}
    for _, n in ipairs(all_nodes) do
        -- GNB 需要 route.conf 包含所有节点（含自身），否则报 "miss local_node is NULL"
        local l = string.format(
            "%d|%s|255.255.255.255", n.node_id, n.virtual_ip)
        if n.node_id == self_id then
            self_line = l
        else
            lines[#lines + 1] = l
        end
    end
    -- 当前节点必须在第一行
    if self_line then
        table.insert(lines, 1, self_line)
    end
    return table.concat(lines, "\n") .. "\n"
end

local function gen_address_conf(self_node, local_node, listen_port, index_addr)
    local idx_host, idx_port = nil, nil
    if index_addr and index_addr ~= "" then
        idx_host, idx_port = index_addr:match("^([%w%.%-]+):(%d+)$")
    end

    if self_node.is_local then
        local lines = {
            "# GNB 离线模式 — 本机节点",
            string.format("# 监听 UDP %d, 等待客户端连接", listen_port),
        }
        if idx_host then
            lines[#lines + 1] = string.format("i|0|%s|%s", idx_host, idx_port)
        else
            lines[#lines + 1] = "# 如需 index server: i|0|<IP>|<端口>"
        end
        return table.concat(lines, "\n") .. "\n"
    end

    local lines = {}
    if idx_host then
        lines[#lines + 1] = "# 通过 Index Server 发现节点，无需手动指定地址"
        lines[#lines + 1] = string.format("i|0|%s|%s", idx_host, idx_port)
    elseif local_node then
        lines[#lines + 1] = "# 将 <ROUTER_IP> 替换为路由器的实际 IP 地址"
        lines[#lines + 1] = string.format(
            "n|%d|<ROUTER_IP>|%d", local_node.node_id, listen_port)
    end
    return table.concat(lines, "\n") .. "\n"
end

-- 写入单个节点的全部配置（目录 + node/route/address/keys）
local function write_node_configs(node, all_nodes, local_node, port, all_keys, index_addr)
    local nid_s    = util.int_str(node.node_id)
    local conf_dir = GNB_CONF_DIR .. "/" .. nid_s

    util.ensure_dir(conf_dir)
    util.ensure_dir(conf_dir .. "/security")
    util.ensure_dir(conf_dir .. "/ed25519")

    util.write_file(conf_dir .. "/node.conf",
        gen_node_conf(node.node_id, port, node.name))
    util.write_file(conf_dir .. "/route.conf",
        gen_route_conf(node.node_id, all_nodes))
    util.write_file(conf_dir .. "/address.conf",
        gen_address_conf(node, local_node, port, index_addr))

    -- 自身密钥
    local kp = all_keys[node.node_id]
    util.write_file_secure(
        conf_dir .. "/security/" .. nid_s .. ".private", kp.priv_hex .. "\n")
    util.write_file(
        conf_dir .. "/security/" .. nid_s .. ".public", kp.pub_hex)

    -- 所有节点公钥（含自身，gnb 运行时需要）
    for _, peer in ipairs(all_nodes) do
        util.write_file(
            conf_dir .. "/ed25519/" .. util.int_str(peer.node_id) .. ".public",
            all_keys[peer.node_id].pub_hex)
    end
end

-- ─────────────────────────────────────────────────────────────
-- 重新生成本机节点的 route.conf（从 guest.json，始终覆盖写入）
-- 每次启动、增删节点后都应调用，确保 route.conf 与 guest.json 一致
-- 返回: true, nil  |  nil, error_string
-- ─────────────────────────────────────────────────────────────
function M.ensure_route_conf(node_id)
    local g = M.load_config()
    if not g or not g.nodes then
        return nil, "guest.json 未初始化"
    end
    local nid_s = util.int_str(node_id)
    local content = gen_route_conf(node_id, g.nodes)
    if not content or content == "" then
        return nil, "无法生成 route.conf（仅一个节点？）"
    end
    util.ensure_dir(GNB_CONF_DIR .. "/" .. nid_s)
    local ok, we = util.write_file(GNB_CONF_DIR .. "/" .. nid_s .. "/route.conf", content)
    if not ok then
        return nil, "写入 route.conf 失败: " .. (we or "")
    end
    -- 同步生成 /etc/mynet/conf/route.conf（<cidr> via <gateway> 格式）
    local node_m = require("luci.model.mynet.node")
    node_m.generate_route_conf(node_id)
    return true, nil
end

-- ─────────────────────────────────────────────────────────────
-- 初始化 Guest 网络
-- opts: { node_count, network_name, subnet, listen_port, local_index, index_addr }
-- 返回: guest_config, nil  |  nil, error_string
-- ─────────────────────────────────────────────────────────────

function M.init_network(opts)
    -- 懒加载 node_m（仅用到 generate_key_pair）
    local node_m = require("luci.model.mynet.node")

    opts = opts or {}
    local count     = math.max(2, math.min(10, tonumber(opts.node_count) or 3))
    local subnet    = opts.subnet or DEFAULT_SUBNET
    local port      = tonumber(opts.listen_port) or DEFAULT_PORT
    local local_idx = tonumber(opts.local_index) or 1
    local netname   = opts.network_name or "MyNetwork"
    local start_id  = tonumber(opts.start_id) or DEFAULT_START_ID
    local index_addr = opts.index_addr or "idx.mynet.club:9016"

    -- 输入校验
    if not subnet:match("^%d+%.%d+%.%d+$") then
        return nil, "子网格式错误，示例: 10.1.0"
    end
    if port < 1024 or port > 65535 then
        return nil, "端口范围: 1024-65535"
    end
    if local_idx < 1 or local_idx > count then local_idx = 1 end

    -- 构建节点列表
    local nodes = {}
    for i = 1, count do
        nodes[i] = {
            node_id    = start_id + (i - 1),
            name       = (i == local_idx) and "Router (本机)" or ("设备 " .. i),
            virtual_ip = string.format("%s.%d", subnet, i),
            is_local   = (i == local_idx),
        }
    end

    -- 为每个节点生成密钥
    local all_keys = {}
    for _, n in ipairs(nodes) do
        local kp, err = node_m.generate_key_pair()
        if err then
            return nil, "密钥生成失败 (node " .. n.node_id .. "): " .. err
        end
        all_keys[n.node_id] = kp
    end

    -- 找到本机节点
    local local_node = nodes[local_idx]

    -- 写入所有节点配置
    for _, n in ipairs(nodes) do
        write_node_configs(n, nodes, local_node, port, all_keys, index_addr)
    end

    -- 保存 guest.json
    local guest_cfg = {
        network_name  = netname,
        subnet        = subnet,
        listen_port   = port,
        local_node_id = local_node.node_id,
        index_addr    = index_addr ~= "" and index_addr or nil,
        nodes         = nodes,
        created_at    = os.date("!%Y-%m-%dT%H:%M:%SZ"),
    }
    M.save_config(guest_cfg)

    -- 设置运行模式 + 写完整 mynet.conf（兼容 shell 脚本）
    cfg.set_mode("guest")
    cfg.generate_mynet_conf(local_node.node_id)

    return guest_cfg, nil
end

-- ─────────────────────────────────────────────────────────────
-- 新增节点（追加到已有网络）
-- 返回: new_node, nil  |  nil, error_string
-- ─────────────────────────────────────────────────────────────

function M.add_node(name, custom_node_id)
    local node_m = require("luci.model.mynet.node")
    local g = M.load_config()
    if not g or not g.nodes then
        return nil, "Guest 模式未初始化"
    end

    -- 计算新 ID 和 IP
    local max_id  = DEFAULT_START_ID - 1
    local max_idx = 0
    for _, n in ipairs(g.nodes) do
        if n.node_id > max_id then max_id = n.node_id end
        local idx = tonumber(n.virtual_ip:match("%.(%d+)$")) or 0
        if idx > max_idx then max_idx = idx end
    end
    local new_id    = tonumber(custom_node_id) or (max_id + 1)
    local new_ip_idx = max_idx + 1

    -- 检查 ID 冲突
    for _, n in ipairs(g.nodes) do
        if n.node_id == new_id then
            return nil, "Node ID " .. new_id .. " 已存在"
        end
    end

    local new_node = {
        node_id    = new_id,
        name       = name or ("设备 " .. (#g.nodes + 1)),
        virtual_ip = string.format("%s.%d", g.subnet, new_ip_idx),
        is_local   = false,
    }

    -- 生成密钥
    local kp, err = node_m.generate_key_pair()
    if err then return nil, "密钥生成失败: " .. err end

    -- 找到本机节点
    local local_node
    for _, n in ipairs(g.nodes) do
        if n.is_local then local_node = n; break end
    end

    -- 收集所有公钥（已有节点从磁盘读、新节点用刚生成的）
    local all_keys = {}
    all_keys[new_id] = kp
    for _, n in ipairs(g.nodes) do
        local pub_path = GNB_CONF_DIR .. "/" .. util.int_str(n.node_id)
            .. "/security/" .. util.int_str(n.node_id) .. ".public"
        local pub = util.trim(util.read_file(pub_path) or "")
        if pub ~= "" then
            all_keys[n.node_id] = { pub_hex = pub }
        end
    end

    -- 合并节点列表（含新节点）
    local all_nodes = {}
    for _, n in ipairs(g.nodes) do all_nodes[#all_nodes + 1] = n end
    all_nodes[#all_nodes + 1] = new_node

    -- 写入新节点完整配置
    local nid_s    = util.int_str(new_id)
    local conf_dir = GNB_CONF_DIR .. "/" .. nid_s
    util.ensure_dir(conf_dir)
    util.ensure_dir(conf_dir .. "/security")
    util.ensure_dir(conf_dir .. "/ed25519")

    util.write_file(conf_dir .. "/node.conf",
        gen_node_conf(new_id, g.listen_port, new_node.name))
    util.write_file(conf_dir .. "/route.conf",
        gen_route_conf(new_id, all_nodes))
    util.write_file(conf_dir .. "/address.conf",
        gen_address_conf(new_node, local_node, g.listen_port))
    util.write_file_secure(
        conf_dir .. "/security/" .. nid_s .. ".private", kp.priv_hex .. "\n")
    util.write_file(conf_dir .. "/security/" .. nid_s .. ".public", kp.pub_hex)

    -- 公钥：写入所有节点的公钥
    for nid, k in pairs(all_keys) do
        util.write_file(
            conf_dir .. "/ed25519/" .. util.int_str(nid) .. ".public", k.pub_hex)
    end

    -- 更新现有节点：追加新节点公钥 + route
    for _, existing in ipairs(g.nodes) do
        local edir = GNB_CONF_DIR .. "/" .. util.int_str(existing.node_id)
        -- 公钥
        util.write_file(edir .. "/ed25519/" .. nid_s .. ".public", kp.pub_hex)
        -- route.conf：追加一行
        local rpath = edir .. "/route.conf"
        local rcontent = util.read_file(rpath) or ""
        local new_line = string.format(
            "%s|%s|255.255.255.255", util.int_str(new_id), new_node.virtual_ip)
        if not rcontent:find(util.int_str(new_id) .. "|") then
            util.write_file(rpath, rcontent .. new_line .. "\n")
        end
    end

    -- 更新 guest.json
    g.nodes[#g.nodes + 1] = new_node
    M.save_config(g)

    return new_node, nil
end

-- ─────────────────────────────────────────────────────────────
-- 删除节点
-- ─────────────────────────────────────────────────────────────

function M.delete_node(node_id)
    local g = M.load_config()
    if not g or not g.nodes then
        return nil, "Guest 模式未初始化"
    end
    node_id = tonumber(node_id) or 0
    if node_id == tonumber(g.local_node_id) then
        return nil, "不能删除本机节点"
    end
    if #g.nodes <= 2 then
        return nil, "至少保留 2 个节点"
    end

    local nid_s = util.int_str(node_id)
    local found = false
    local remaining = {}
    for _, n in ipairs(g.nodes) do
        if n.node_id == node_id then
            found = true
        else
            remaining[#remaining + 1] = n
        end
    end
    if not found then return nil, "节点不存在" end

    -- 删除该节点配置目录
    os.execute("rm -rf '" .. GNB_CONF_DIR .. "/" .. nid_s .. "'")

    -- 从其他节点移除公钥 + 路由
    for _, n in ipairs(remaining) do
        local edir = GNB_CONF_DIR .. "/" .. util.int_str(n.node_id)
        os.execute("rm -f '" .. edir .. "/ed25519/" .. nid_s .. ".public'")
        -- 重写 route.conf（排除已删节点）
        util.write_file(edir .. "/route.conf",
            gen_route_conf(n.node_id, remaining))
    end

    g.nodes = remaining
    M.save_config(g)
    return true, nil
end

-- ─────────────────────────────────────────────────────────────
-- 导出节点配置包（tar.gz），用于远程设备部署
-- 返回: filepath, nil  |  nil, error_string
-- ─────────────────────────────────────────────────────────────

function M.export_node_config(node_id)
    local nid_s    = util.int_str(node_id)
    local conf_dir = GNB_CONF_DIR .. "/" .. nid_s

    if not util.file_exists(conf_dir .. "/node.conf") then
        return nil, "节点配置不存在"
    end

    local tmp = "/tmp/mynet_guest_" .. nid_s .. ".tar.gz"
    local cmd = string.format(
        "tar -czf '%s' -C '%s' node.conf route.conf address.conf security ed25519 2>/dev/null",
        tmp, conf_dir)
    os.execute(cmd)

    if not util.file_exists(tmp) then
        return nil, "打包失败"
    end
    return tmp, nil
end

-- ─────────────────────────────────────────────────────────────
-- 重置（删除所有 guest 节点配置）
-- ─────────────────────────────────────────────────────────────

function M.reset()
    local g = M.load_config()
    if g and g.nodes then
        for _, n in ipairs(g.nodes) do
            os.execute("rm -rf '" .. GNB_CONF_DIR .. "/" .. util.int_str(n.node_id) .. "'")
        end
    end
    os.execute("rm -f '" .. GUEST_FILE .. "'")
    -- 不自动切换模式，让用户选择
    return true
end

-- ─────────────────────────────────────────────────────────────
-- 导入配置包 — 智能解压 + 验证一致性
-- ─────────────────────────────────────────────────────────────

--- 解析配置包（tar.gz / tgz / gz / zip），智能定位配置文件，验证一致性
-- @param tar_path  string  上传的压缩包路径
-- @param filename  string  原始文件名（可选，用于检测格式）
-- @return table preview_data, nil  |  nil, string error
function M.import_preview(tar_path, filename)
    if not util.file_exists(tar_path) then
        return nil, "file not found"
    end

    -- 创建临时解压目录
    local tmp_dir = "/tmp/mynet_import_" .. os.time()
    os.execute("rm -rf '" .. tmp_dir .. "' && mkdir -p '" .. tmp_dir .. "'")

    -- 按格式解压
    local ext = (filename or tar_path):lower()
    local _, code
    if ext:match("%.zip$") then
        _, code = util.exec_status(
            "unzip -o -q '" .. tar_path .. "' -d '" .. tmp_dir .. "' 2>&1")
    else
        _, code = util.exec_status(
            "tar xzf '" .. tar_path .. "' -C '" .. tmp_dir .. "' 2>&1")
    end
    if code ~= 0 then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "failed to extract archive"
    end

    -- 智能查找配置文件：在解压目录中递归搜索
    local function find_file(name)
        local result = util.trim(util.exec(
            "find '" .. tmp_dir .. "' -name '" .. name .. "' -type f 2>/dev/null | head -1") or "")
        if result ~= "" then return result end
        return nil
    end

    local node_conf_path = find_file("node.conf")
    if not node_conf_path then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "node.conf not found in archive"
    end

    -- 解析 node.conf → 提取 nodeid
    local node_content = util.read_file(node_conf_path) or ""
    local nodeid_str = node_content:match("^%s*nodeid%s+(%d+)")
                    or node_content:match("\n%s*nodeid%s+(%d+)")
    if not nodeid_str then
        os.execute("rm -rf '" .. tmp_dir .. "'")
        return nil, "node.conf does not contain a valid 'nodeid' line"
    end

    local route_conf_path   = find_file("route.conf")
    local address_conf_path = find_file("address.conf")
    local route_content     = route_conf_path and (util.read_file(route_conf_path) or "") or ""
    local address_content   = address_conf_path and (util.read_file(address_conf_path) or "") or ""

    -- route.conf 验证：第一条有效记录的 nodeid 应与 node.conf 一致
    local route_first_nid = nil
    local route_peers = {}
    for line in route_content:gmatch("[^\n]+") do
        local stripped = line:gsub("#.*$", ""):match("^%s*(.-)%s*$")
        if stripped ~= "" then
            local fields = {}
            for f in stripped:gmatch("[^|]+") do fields[#fields + 1] = f:match("^%s*(.-)%s*$") end
            if #fields >= 3 then
                if not route_first_nid then route_first_nid = fields[1] end
                route_peers[#route_peers + 1] = {
                    node_id    = fields[1],
                    virtual_ip = fields[2],
                    netmask    = fields[3],
                }
            end
        end
    end

    local route_match = true
    if route_first_nid and route_first_nid ~= nodeid_str then
        route_match = false
    end

    -- 查找密钥文件
    local priv_key_path = find_file(nodeid_str .. ".private")
    local pub_key_path  = find_file(nodeid_str .. ".public")

    local priv_key_hex = priv_key_path and util.trim(util.read_file(priv_key_path) or "") or ""
    local pub_key_hex  = pub_key_path and util.trim(util.read_file(pub_key_path) or "") or ""

    -- 密钥指纹（前8+后8字符）
    local function fingerprint(hex)
        if #hex >= 16 then return hex:sub(1, 8) .. "..." .. hex:sub(-8) end
        return hex
    end

    -- 查找对端公钥（ed25519/ 目录中的 .public 文件，排除自身）
    local peer_keys = {}
    local ed_result = util.exec(
        "find '" .. tmp_dir .. "' -path '*/ed25519/*.public' -type f 2>/dev/null") or ""
    for ed_path in ed_result:gmatch("[^\n]+") do
        local fname = ed_path:match("([^/]+)%.public$")
        if fname and fname ~= nodeid_str then
            local pk_hex = util.trim(util.read_file(ed_path) or "")
            peer_keys[#peer_keys + 1] = {
                peer_id = fname,
                key_fingerprint = fingerprint(pk_hex),
            }
        end
    end

    -- 提取 listen port
    local listen_port = node_content:match("\n%s*listen%s+(%d+)")
                     or node_content:match("^%s*listen%s+(%d+)")

    -- 组装预览数据
    local preview = {
        tmp_dir        = tmp_dir,
        node_id        = nodeid_str,
        listen_port    = listen_port,
        node_conf      = node_content,
        has_route      = route_conf_path ~= nil,
        has_address    = address_conf_path ~= nil,
        route_match    = route_match,
        route_first_nid = route_first_nid,
        route_peers    = route_peers,
        has_private_key = priv_key_hex ~= "",
        has_public_key  = pub_key_hex ~= "",
        priv_fingerprint = fingerprint(priv_key_hex),
        pub_fingerprint  = fingerprint(pub_key_hex),
        peer_keys       = peer_keys,
    }
    return preview, nil
end

--- 确认导入：将临时目录中的文件复制到正式 GNB 配置目录
-- @param tmp_dir   string  import_preview 返回的临时目录
-- @param node_id   string  节点 ID（字符串）
-- @return bool, string|nil
function M.import_apply(tmp_dir, node_id)
    if not tmp_dir or not util.file_exists(tmp_dir) then
        return false, "temp dir not found (expired?)"
    end

    local nid_s    = util.int_str(node_id)
    local conf_dir = GNB_CONF_DIR .. "/" .. nid_s

    -- 创建目标目录
    os.execute("mkdir -p '" .. conf_dir .. "/security'")
    os.execute("mkdir -p '" .. conf_dir .. "/ed25519'")

    -- 智能复制：从临时目录中找到文件并复制到目标
    local function copy_found(name, dest)
        local src = util.trim(util.exec(
            "find '" .. tmp_dir .. "' -name '" .. name .. "' -type f 2>/dev/null | head -1") or "")
        if src ~= "" then
            os.execute("cp -f '" .. src .. "' '" .. dest .. "'")
            return true
        end
        return false
    end

    copy_found("node.conf",    conf_dir .. "/node.conf")
    copy_found("route.conf",   conf_dir .. "/route.conf")
    copy_found("address.conf", conf_dir .. "/address.conf")

    -- 复制密钥
    copy_found(nid_s .. ".private", conf_dir .. "/security/" .. nid_s .. ".private")
    copy_found(nid_s .. ".public",  conf_dir .. "/security/" .. nid_s .. ".public")
    -- 私钥权限
    os.execute("chmod 600 '" .. conf_dir .. "/security/" .. nid_s .. ".private' 2>/dev/null")

    -- 复制对端公钥（ed25519/*.public）
    local ed_files = util.exec(
        "find '" .. tmp_dir .. "' -path '*/ed25519/*.public' -type f 2>/dev/null") or ""
    for ed_path in ed_files:gmatch("[^\n]+") do
        local fname = ed_path:match("([^/]+%.public)$")
        if fname then
            os.execute("cp -f '" .. ed_path .. "' '" .. conf_dir .. "/ed25519/" .. fname .. "'")
        end
    end

    -- 更新 guest.json — 根据 route.conf 中的对端构建节点列表
    local route_content = util.read_file(conf_dir .. "/route.conf") or ""
    local nodes = {}
    local subnet_base = nil
    for line in route_content:gmatch("[^\n]+") do
        local stripped = line:gsub("#.*$", ""):match("^%s*(.-)%s*$")
        if stripped ~= "" then
            local fields = {}
            for f in stripped:gmatch("[^|]+") do fields[#fields + 1] = f:match("^%s*(.-)%s*$") end
            if #fields >= 3 then
                local is_self = (fields[1] == nid_s)
                nodes[#nodes + 1] = {
                    node_id    = tonumber(fields[1]) or 0,
                    name       = is_self and "Router (本机)" or ("设备 " .. fields[1]),
                    virtual_ip = fields[2],
                    is_local   = is_self,
                }
                if not subnet_base then
                    subnet_base = fields[2]:match("^(%d+%.%d+%.%d+)%.")
                end
            end
        end
    end

    local listen_port = 9001
    local nc = util.read_file(conf_dir .. "/node.conf") or ""
    local lp = nc:match("\n%s*listen%s+(%d+)") or nc:match("^%s*listen%s+(%d+)")
    if lp then listen_port = tonumber(lp) or 9001 end

    local g = {
        network_name  = "Imported Network",
        subnet        = subnet_base or "10.1.0",
        listen_port   = listen_port,
        local_node_id = tonumber(nid_s) or 0,
        created_at    = os.date("!%Y-%m-%dT%H:%M:%SZ"),
        nodes         = nodes,
    }
    M.save_config(g)

    -- 生成 mynet.conf
    cfg.generate_mynet_conf(tonumber(nid_s) or 0)

    -- 清理临时目录
    os.execute("rm -rf '" .. tmp_dir .. "'")

    return true, nil
end

return M
