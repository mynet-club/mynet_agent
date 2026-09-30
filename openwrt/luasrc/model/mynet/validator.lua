-- mynet/validator.lua — 配置校验与自动修复
-- 对齐 client config/validator.js
-- 检查项：config.json / credential.json / zone.json / gnb 目录结构
-- 返回结构化结果，Dashboard 可展示每项检查状态

local M    = {}
local util = require("luci.model.mynet.util")

-- ─────────────────────────────────────────────────────────────
-- 校验结果常量
-- ─────────────────────────────────────────────────────────────

local SEV = { OK = "ok", WARN = "warn", ERROR = "error" }

-- ─────────────────────────────────────────────────────────────
-- 内部：单项检查 helper
-- ─────────────────────────────────────────────────────────────

local function check(name, ok, severity, detail, repairable)
    return {
        name       = name,
        ok         = ok,
        severity   = ok and SEV.OK or (severity or SEV.ERROR),
        detail     = detail or "",
        repairable = repairable or false,
    }
end

-- ─────────────────────────────────────────────────────────────
-- 校验配置完整性
-- 返回: { ok=bool, checks=[{name, ok, severity, detail, repairable}] }
-- ─────────────────────────────────────────────────────────────

function M.validate_config()
    local checks = {}
    local all_ok = true

    local function add(c)
        checks[#checks + 1] = c
        if not c.ok and c.severity == SEV.ERROR then all_ok = false end
    end

    -- 1. config.json 存在且可解析
    local cfg_data = util.load_json_file(util.CONFIG_FILE)
    add(check("config_json",
        cfg_data ~= nil,
        SEV.ERROR,
        cfg_data and "ok" or "missing or invalid: " .. util.CONFIG_FILE,
        true))

    -- 2. api_base_url 格式正确
    local api_url = cfg_data and cfg_data.server_config
        and cfg_data.server_config.api_base_url or ""
    local url_ok = api_url:match("^https?://[^%s]+") ~= nil
    add(check("api_base_url",
        url_ok,
        SEV.ERROR,
        url_ok and api_url or "invalid or missing api_base_url",
        true))

    -- 3. credential.json 存在
    local cred_data = util.load_json_file(util.CRED_FILE)
    add(check("credential_json",
        cred_data ~= nil,
        SEV.WARN,
        cred_data and "ok" or "missing: " .. util.CRED_FILE .. " (login required)",
        false))

    -- 4. credential.json 权限 0600
    if cred_data then
        local stat_out = util.trim(util.exec(
            "stat -c '%a' '" .. util.CRED_FILE .. "' 2>/dev/null") or "")
        local perm_ok = stat_out == "600"
        add(check("credential_permissions",
            perm_ok,
            SEV.WARN,
            perm_ok and "0600" or ("current: " .. (stat_out ~= "" and stat_out or "unknown")),
            true))
    end

    -- 5. zone.json 有 zone_id
    local zone_data = util.load_json_file(util.ZONE_FILE)
    local has_zone = zone_data and zone_data.zone_id
        and tostring(zone_data.zone_id) ~= "0"
    add(check("zone_selected",
        has_zone,
        SEV.WARN,
        has_zone and ("zone_id=" .. tostring(zone_data.zone_id)) or "no zone selected",
        false))

    -- 6. node_id 已配置（mynet.conf）
    local vpn_conf = util.read_file(util.VPN_CONF) or ""
    local node_id_val = vpn_conf:match('NODE_ID="?(%d+)"?')
    local has_node = node_id_val and node_id_val ~= "0"
    add(check("node_id_configured",
        has_node,
        SEV.WARN,
        has_node and ("node_id=" .. node_id_val) or "no NODE_ID in " .. util.VPN_CONF,
        false))

    -- 7. gnb binary 存在
    local gnb_bin = (cfg_data and cfg_data.gnb and cfg_data.gnb.gnb_bin_path)
        or (util.GNB_BIN_DIR .. "/gnb")
    local gnb_ok = util.file_exists(gnb_bin)
    add(check("gnb_binary",
        gnb_ok,
        SEV.ERROR,
        gnb_ok and gnb_bin or "not found: " .. gnb_bin,
        false))

    -- 8. gnb conf 目录存在
    local gnb_conf_dir = util.GNB_CONF_DIR
    local conf_dir_ok = util.file_exists(gnb_conf_dir)
    add(check("gnb_conf_dir",
        conf_dir_ok,
        SEV.ERROR,
        conf_dir_ok and gnb_conf_dir or "missing: " .. gnb_conf_dir,
        true))

    -- 9. logs 目录可写
    local log_dir = util.MYNET_HOME .. "/logs"
    util.ensure_dir(log_dir)
    local log_test = log_dir .. "/.write_test"
    local log_ok = util.write_file(log_test, "t")
    if log_ok then os.remove(log_test) end
    add(check("log_dir_writable",
        log_ok,
        SEV.WARN,
        log_ok and log_dir or "not writable: " .. log_dir,
        true))

    -- 10. var 目录存在（状态文件存储）
    local var_dir = util.MYNET_HOME .. "/var"
    local var_ok = util.file_exists(var_dir)
    add(check("var_dir",
        var_ok,
        SEV.WARN,
        var_ok and var_dir or "missing: " .. var_dir,
        true))

    return { ok = all_ok, checks = checks }
end

-- ─────────────────────────────────────────────────────────────
-- 自动修复可修复的问题
-- 返回: { repaired=[], failed=[] }
-- ─────────────────────────────────────────────────────────────

function M.auto_repair(issues)
    issues = issues or M.validate_config()
    local repaired = {}
    local failed   = {}

    for _, c in ipairs(issues.checks or {}) do
        if not c.ok and c.repairable then
            local name = c.name
            local ok = false

            if name == "config_json" then
                -- 创建最小 config.json
                if not util.file_exists(util.CONFIG_FILE) then
                    ok = util.save_json_file(util.CONFIG_FILE, {
                        server_config = { api_base_url = "https://api.mynet.club/api/v2" },
                    })
                end

            elseif name == "api_base_url" then
                local data = util.load_json_file(util.CONFIG_FILE) or {}
                if not data.server_config then data.server_config = {} end
                if not data.server_config.api_base_url
                    or data.server_config.api_base_url == "" then
                    data.server_config.api_base_url = "https://api.mynet.club/api/v2"
                    ok = util.save_json_file(util.CONFIG_FILE, data)
                end

            elseif name == "credential_permissions" then
                os.execute("chmod 600 '" .. util.CRED_FILE .. "' 2>/dev/null")
                ok = true

            elseif name == "gnb_conf_dir" then
                util.ensure_dir(util.GNB_CONF_DIR)
                ok = util.file_exists(util.GNB_CONF_DIR)

            elseif name == "log_dir_writable" then
                util.ensure_dir(util.MYNET_HOME .. "/logs")
                ok = true

            elseif name == "var_dir" then
                util.ensure_dir(util.MYNET_HOME .. "/var")
                ok = true
            end

            if ok then
                repaired[#repaired + 1] = name
            else
                failed[#failed + 1] = name
            end
        end
    end

    return { repaired = repaired, failed = failed }
end

return M
