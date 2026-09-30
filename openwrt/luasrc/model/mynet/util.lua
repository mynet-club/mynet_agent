-- mynet/util.lua  — 基础工具模块
-- 提供文件 I/O、Shell 执行、JSON 编解码、日志等公共能力。
-- 所有其他 mynet 模块都依赖本模块；本模块不依赖其他 mynet 模块。

local M = {}

-- ─────────────────────────────────────────────────────────────
-- 路径常量
-- ─────────────────────────────────────────────────────────────

M.MYNET_HOME  = "/etc/mynet"
M.APP_VERSION = "2.1.6"
M.CONF_DIR    = M.MYNET_HOME .. "/conf"
M.CRED_FILE   = M.CONF_DIR  .. "/credential.json"
M.CONFIG_FILE = M.CONF_DIR  .. "/config.json"
M.ZONE_FILE   = M.CONF_DIR  .. "/zone.json"
M.VPN_CONF    = M.CONF_DIR  .. "/mynet.conf"
M.LOG_FILE    = M.MYNET_HOME .. "/logs/luci.log"

-- 脚本目录
M.SCRIPTS_DIR     = M.MYNET_HOME .. "/scripts"
M.ROUTE_SCRIPT    = M.SCRIPTS_DIR .. "/route.mynet"
M.FIREWALL_SCRIPT = M.SCRIPTS_DIR .. "/firewall.mynet"

-- GNB 驱动目录（对应 mynet_tui driver/gnb 结构）
M.GNB_DRIVER_ROOT = M.MYNET_HOME .. "/driver/gnb"
M.GNB_BIN_DIR     = M.GNB_DRIVER_ROOT .. "/bin"
M.GNB_CONF_DIR    = M.GNB_DRIVER_ROOT .. "/conf"

-- L6 执行器（mynet_agent 产物）。路由/防火墙写入唯一入口是 `mynet apply`，
-- 与桌面/Linux 同一命令链（见 knowledge-base/spec/route-apply-unification.md）。
function M.mynet_bin()
    for _, p in ipairs({
        "/usr/sbin/mynet",
        "/usr/bin/mynet",
        M.MYNET_HOME .. "/bin/mynet",
    }) do
        if M.file_exists(p) then
            return p
        end
    end
    return nil
end

-- ─────────────────────────────────────────────────────────────
-- JSON（优先使用 luci.jsonc，失败则降级）
-- ─────────────────────────────────────────────────────────────

local _jsonc_ok, _jsonc = pcall(require, "luci.jsonc")

local function _json_encode(obj)
    if _jsonc_ok then
        local ok, s = pcall(_jsonc.stringify, obj)
        if ok then return s end
    end
    -- 极简降级实现（仅支持简单嵌套对象/数组/字符串/数字/布尔/nil）
    local t = type(obj)
    if t == "nil"     then return "null" end
    if t == "boolean" then return obj and "true" or "false" end
    if t == "number"  then
        -- 避免科学计数法：整数型用 %.0f（双精度最大安全整数 2^53 ≈ 9e15）
        if obj == math.floor(obj) and obj >= -9e15 and obj <= 9e15 then
            return string.format("%.0f", obj)
        end
        return tostring(obj)
    end
    if t == "string"  then
        return '"' .. obj:gsub('\\','\\\\'):gsub('"','\\"')
                         :gsub('\n','\\n'):gsub('\r','\\r')
                         :gsub('\t','\\t') .. '"'
    end
    if t == "table" then
        -- array?
        local is_arr = (#obj > 0)
        if is_arr then
            local parts = {}
            for _, v in ipairs(obj) do parts[#parts+1] = _json_encode(v) end
            return "[" .. table.concat(parts, ",") .. "]"
        else
            local parts = {}
            for k, v in pairs(obj) do
                parts[#parts+1] = _json_encode(tostring(k)) .. ":" .. _json_encode(v)
            end
            return "{" .. table.concat(parts, ",") .. "}"
        end
    end
    return "null"
end

local function _json_decode(str)
    if _jsonc_ok then
        local ok, result = pcall(_jsonc.parse, str)
        if ok then return result end
    end
    return nil
end

-- 对外接口
function M.json_encode(obj) return _json_encode(obj) end
function M.json_decode(str) return _json_decode(str) end

-- 将数字/数字字符串转为整数十进制字符串（无科学计数法）
-- 支持 number、"3.84e+15" 形式的字符串，以及普通数字字符串
function M.int_str(v)
    if type(v) == "number" then
        return string.format("%.0f", v)
    end
    local n = tonumber(v)
    if n then
        return string.format("%.0f", n)
    end
    return tostring(v or 0)
end

-- node_id 格式化（int_str 的别名，模板中使用）
M.nid_fmt = M.int_str

-- ─────────────────────────────────────────────────────────────
-- 云端/API 错误的人话化（U1）
-- 不要把 `api error 401: Unauthenticated.` 这类原文塞进表格行——
-- 用户看不懂该去登录还是坏了。细节进「原始数据」/日志。
-- ─────────────────────────────────────────────────────────────
function M.friendly_api_error(err)
    if not err or err == "" then return nil end
    local s = tostring(err)
    local code = s:match("api error (%d+)")
    code = tonumber(code)
    if code == 401 or s:lower():find("unauthenticated", 1, true)
        or s:lower():find("unauthorized", 1, true) then
        return "云端未登录或凭证已过期，请到「设置 / 登录」重新登录"
    end
    if code == 403 then
        return "无权限访问该资源（请联系管理员或换账号）"
    end
    if code == 404 then
        return "云端暂无此节点/资源"
    end
    if code == 429 then
        return "请求过于频繁，请稍后再试"
    end
    if code and code >= 500 then
        return "云端服务异常（" .. tostring(code) .. "），稍后重试"
    end
    if s:find("timeout", 1, true) or s:find("timed out", 1, true) then
        return "连接云端超时，请检查网络"
    end
    if s:find("Could not resolve", 1, true) or s:find("resolve host", 1, true) then
        return "无法解析云端域名，请检查 DNS / 网络"
    end
    if s:find("Connection refused", 1, true) then
        return "无法连接云端服务"
    end
    -- 其它：保留原文但截断，避免整段 API 噪音占满单元格
    if #s > 80 then
        s = s:sub(1, 77) .. "…"
    end
    return s
end

-- 字节数格式化为人类可读字符串
function M.fmt_bytes(n)
    if not n or n == 0 then return "0 B" end
    local units = { "B", "KB", "MB", "GB", "TB" }
    local i = 1
    while n >= 1024 and i < #units do
        n = n / 1024
        i = i + 1
    end
    if i == 1 then return string.format("%d B", n) end
    return string.format("%.1f %s", n, units[i])
end

-- ─────────────────────────────────────────────────────────────
-- Bash KEY=VALUE 配置文件解析
-- 解析格式: KEY="VALUE" 或 KEY=VALUE（跳过空行和 # 注释）
-- opts.lower_keys: 是否将 key 转为小写（默认 false）
-- ─────────────────────────────────────────────────────────────
function M.parse_bash_conf(path, opts)
    local content = M.read_file(path)
    if not content then return nil end
    opts = opts or {}
    local result = {}
    for line in content:gmatch("[^\n]+") do
        line = M.trim(line)
        if line ~= "" and not line:match("^#") then
            local k, v = line:match('^([%w_]+)%s*=%s*"?(.-)"?%s*$')
            if k then
                if opts.lower_keys then k = k:lower() end
                result[k] = v
            end
        end
    end
    return result
end

-- ─────────────────────────────────────────────────────────────
-- Shell 执行
-- ─────────────────────────────────────────────────────────────

-- 执行命令，返回 stdout（忽略 stderr）
function M.exec(cmd)
    local h = io.popen(cmd .. " 2>/dev/null")
    if not h then return nil end
    local out = h:read("*a")
    h:close()
    return out
end

-- 执行命令，返回 (stdout+stderr, exit_code)
function M.exec_status(cmd)
    local marker = "__MYNET_EXIT__"
    local h = io.popen(cmd .. " 2>&1; echo " .. marker .. ":$?")
    if not h then return nil, -1 end
    local raw = h:read("*a")
    h:close()
    local code_str = raw:match(marker .. ":(%d+)%s*$")
    local output   = raw:gsub("\n?" .. marker .. ":%d+%s*$", "")
    return output, tonumber(code_str) or -1
end

-- ─────────────────────────────────────────────────────────────
-- 文件系统
-- ─────────────────────────────────────────────────────────────

function M.file_exists(path)
    local f = io.open(path, "r")
    if f then f:close(); return true end
    return false
end

function M.ensure_dir(path)
    os.execute("mkdir -p " .. M.shell_escape(path))
end

function M.read_file(path)
    local f = io.open(path, "r")
    if not f then return nil end
    local c = f:read("*a")
    f:close()
    return c
end

-- 写文件（自动创建父目录）
function M.write_file(path, content)
    local dir = path:match("(.+)/[^/]+$")
    if dir then M.ensure_dir(dir) end
    local f = io.open(path, "w")
    if not f then return false, "cannot write: " .. path end
    f:write(content)
    f:close()
    return true
end

-- 写文件并设置权限 0600（敏感文件：credential / private key）
function M.write_file_secure(path, content)
    local ok, err = M.write_file(path, content)
    if ok then
        os.execute("chmod 600 '" .. path .. "' 2>/dev/null")
    end
    return ok, err
end

-- 加载 JSON 文件 → Lua table
function M.load_json_file(path)
    local c = M.read_file(path)
    if not c then return nil end
    return M.json_decode(c)
end

-- 保存 Lua table → JSON 文件
function M.save_json_file(path, obj)
    local s = M.json_encode(obj)
    if not s then return false, "json encode failed" end
    return M.write_file(path, s)
end

-- ─────────────────────────────────────────────────────────────
-- 字符串工具
-- ─────────────────────────────────────────────────────────────

function M.trim(s)
    if not s then return "" end
    return s:match("^%s*(.-)%s*$")
end

-- ─────────────────────────────────────────────────────────────
-- 输入校验（安全加固，防注入）
-- ─────────────────────────────────────────────────────────────

-- 校验 node_id：纯数字字符串或 number
function M.validate_node_id(v)
    if type(v) == "number" then return true end
    if type(v) == "string" and v:match("^%d+$") then return true end
    return false
end

-- 校验 hex 字符串（密钥等）
function M.validate_hex(v, expected_len)
    if type(v) ~= "string" then return false end
    v = v:gsub("%s+", "")
    if not v:match("^[0-9a-fA-F]+$") then return false end
    if expected_len and #v ~= expected_len then return false end
    return true
end

-- 转义 shell 参数（防止命令注入）
-- 用单引号包裹并转义内部单引号
function M.shell_escape(s)
    if not s then return "''" end
    return "'" .. s:gsub("'", "'\\''") .. "'"
end

-- ─────────────────────────────────────────────────────────────
-- 时间
-- ─────────────────────────────────────────────────────────────

function M.time_now()
    return os.time()
end

-- 格式化为本地时间字符串（ISO 8601，不含时区）
function M.format_time(ts)
    return os.date("%Y-%m-%dT%H:%M:%S", ts)
end

-- 解析简单 ISO 8601 字符串 → Unix timestamp（忽略时区偏移）
function M.parse_time(str)
    if not str or str == "" then return 0 end
    local y, mo, d, h, mi, s = str:match("(%d+)-(%d+)-(%d+)T(%d+):(%d+):(%d+)")
    if not y then return 0 end
    return os.time({
        year = tonumber(y), month = tonumber(mo), day = tonumber(d),
        hour = tonumber(h), min   = tonumber(mi),  sec = tonumber(s),
        isdst = false,
    })
end

-- ─────────────────────────────────────────────────────────────
-- 设备 ID
-- ─────────────────────────────────────────────────────────────

function M.get_machine_id()
    local id = M.read_file("/etc/machine-id")
    if id and M.trim(id) ~= "" then return M.trim(id) end
    -- OpenWrt：使用 br-lan 或 eth0 的 MAC 地址
    local mac = M.exec("cat /sys/class/net/br-lan/address 2>/dev/null || cat /sys/class/net/eth0/address 2>/dev/null")
    if mac and M.trim(mac) ~= "" then
        return M.trim(mac):gsub(":", "")
    end
    return "openwrt-device"
end

-- Token Binding 设备指纹：同机稳定、异机不同。
-- 与 agent DeviceFingerprint 同用途（integration-plan §14.4）；OpenWrt 用 machine-id/MAC 即可。
function M.device_fingerprint()
    return M.get_machine_id()
end

-- ─────────────────────────────────────────────────────────────
-- 统一结果包装（对齐 client {success, data, message} 信封格式）
-- 新代码建议使用这些 helper；已有函数逐步迁移。
-- ─────────────────────────────────────────────────────────────

function M.wrap_ok(data)
    return { ok = true, data = data }
end

function M.wrap_err(msg)
    return { ok = false, error = msg or "unknown error" }
end

-- ─────────────────────────────────────────────────────────────
-- 日志（写到文件，非阻断性）
-- 支持模块标签：log(level, module, msg) 或 log(level, msg)
-- 日志级别过滤：M.LOG_LEVEL 控制最低输出级别
-- ─────────────────────────────────────────────────────────────

local _LOG_LEVELS = { DEBUG = 1, INFO = 2, WARN = 3, ERROR = 4 }
M.LOG_LEVEL = "INFO"   -- 默认级别；外部可在运行时覆盖

local function _should_log(level)
    local cur = _LOG_LEVELS[M.LOG_LEVEL] or 2
    local req = _LOG_LEVELS[level]       or 2
    return req >= cur
end

function M.log(level, module_or_msg, msg)
    if not _should_log(level) then return end
    local ts = os.date("%Y-%m-%d %H:%M:%S")
    local line
    if msg then
        -- 三参数形式: log(level, module, msg)
        line = string.format("[%s] [%-5s] [%s] %s\n", ts, level, module_or_msg, msg)
    else
        -- 二参数形式: log(level, msg) — 向后兼容
        line = string.format("[%s] [%-5s] %s\n", ts, level, module_or_msg)
    end
    M.ensure_dir(M.MYNET_HOME .. "/logs")
    -- 轮转检查（每次写前检查，开销极低——仅在文件达 2MB 时触发）
    M.rotate_logs()
    local f = io.open(M.LOG_FILE, "a")
    if f then f:write(line); f:close() end
end

function M.log_debug(msg) M.log("DEBUG", msg) end
function M.log_info(msg)  M.log("INFO",  msg) end
function M.log_warn(msg)  M.log("WARN",  msg) end
function M.log_error(msg) M.log("ERROR", msg) end

-- ─────────────────────────────────────────────────────────────
-- 日志轮转（自包含，不依赖 logrotate 命令）
-- max_size: 最大字节数（默认 2MB），max_files: 保留轮转文件数（默认 3）
-- ─────────────────────────────────────────────────────────────
function M.rotate_logs(max_size, max_files)
    max_size  = max_size  or (2 * 1024 * 1024)  -- 2MB
    max_files = max_files or 3

    local log = M.LOG_FILE
    -- 获取文件大小
    local f = io.open(log, "r")
    if not f then return end
    local size = f:seek("end")
    f:close()
    if not size or size < max_size then return end

    -- 轮转: .3 → 删除, .2 → .3, .1 → .2, log → .1
    os.remove(log .. "." .. tostring(max_files))
    for i = max_files - 1, 1, -1 do
        os.rename(log .. "." .. tostring(i), log .. "." .. tostring(i + 1))
    end
    os.rename(log, log .. ".1")
end

-- ─────────────────────────────────────────────────────────────
-- HMAC-SHA256 签名（纯 Lua XOR + sha256sum，无需 openssl CLI）
-- key_hex: 私钥 hex 字符串（128 chars = 64 bytes）
-- data: 待签名字符串
-- 返回: hex 签名字符串 或 nil
-- ─────────────────────────────────────────────────────────────
function M.hmac_sha256(key_hex, data)
    if not key_hex or key_hex == "" or not data then return nil end

    -- 纯 Lua 字节 XOR（Lua 5.1 兼容，不依赖 bit 库）
    local function xor_byte(a, b)
        local r, v = 0, 1
        for _ = 1, 8 do
            if (a % 2) ~= (b % 2) then r = r + v end
            a = math.floor(a / 2)
            b = math.floor(b / 2)
            v = v * 2
        end
        return r
    end

    -- hex 字符串 → 二进制字符串
    local function hex2bin(hex)
        return (hex:gsub("%x%x", function(h) return string.char(tonumber(h, 16)) end))
    end

    -- 对临时文件执行 sha256sum，返回 64 字符 hex
    local function sha256_file(path)
        local out = M.exec("sha256sum '" .. path .. "' 2>/dev/null")
        return out and out:match("^(%x+)")
    end

    local BLOCK = 64  -- HMAC block size = 64 bytes
    local key_bin = hex2bin(key_hex)

    -- key > BLOCK 时对 key 取 hash（128-hex 私钥 = 64 bytes = 恰好等于 BLOCK，不会触发）
    if #key_bin > BLOCK then
        local t = os.tmpname()
        local f = io.open(t, "wb")
        if not f then return nil end
        f:write(key_bin); f:close()
        local h = sha256_file(t); os.remove(t)
        if not h then return nil end
        key_bin = hex2bin(h)
    end

    -- 用零填充到 BLOCK 字节
    while #key_bin < BLOCK do key_bin = key_bin .. "\0" end

    -- 生成 ipad_bin（key XOR 0x36）和 opad_bin（key XOR 0x5c）
    local ipad_t, opad_t = {}, {}
    for i = 1, BLOCK do
        local b = key_bin:byte(i)
        ipad_t[i] = string.char(xor_byte(b, 0x36))
        opad_t[i] = string.char(xor_byte(b, 0x5c))
    end
    local ipad_bin = table.concat(ipad_t)
    local opad_bin = table.concat(opad_t)

    -- inner = SHA256(ipad_bin || data)
    local t1 = os.tmpname()
    local f1 = io.open(t1, "wb")
    if not f1 then return nil end
    f1:write(ipad_bin); f1:write(data); f1:close()
    local inner_hex = sha256_file(t1)
    os.remove(t1)
    if not inner_hex then return nil end

    -- outer = SHA256(opad_bin || inner_bin)
    local t2 = os.tmpname()
    local f2 = io.open(t2, "wb")
    if not f2 then return nil end
    f2:write(opad_bin); f2:write(hex2bin(inner_hex)); f2:close()
    local result = sha256_file(t2)
    os.remove(t2)
    return result
end

-- 生成 heartbeat 签名（node_id + timestamp + metrics_json）
function M.sign_heartbeat(node_id_str, timestamp, metrics_json, priv_key_hex)
    local payload = node_id_str .. ":" .. timestamp .. ":" .. metrics_json
    return M.hmac_sha256(priv_key_hex, payload)
end

-- hex 字符串 → base64 字符串（用于 X-Node-Signature 请求头）
-- 通过 tmpfile + base64 命令实现，无需额外依赖
function M.hex_to_base64(hex_str)
    if not hex_str or hex_str == "" then return nil end
    local tmp = os.tmpname()
    local f = io.open(tmp, "wb")
    if not f then return nil end
    -- hex → binary
    hex_str:gsub("%x%x", function(h)
        f:write(string.char(tonumber(h, 16)))
    end)
    f:close()
    local b64 = M.exec("base64 < '" .. tmp .. "' | tr -d '\n'")
    os.remove(tmp)
    return b64 and M.trim(b64) or nil
end

return M
