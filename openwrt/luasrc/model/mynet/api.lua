-- mynet/api.lua  — HTTP REST 客户端
-- 通过 curl 实现所有与 MyNet 服务端的 HTTP 通信。
-- 对应 Go 项目 internal/api/client.go 的核心功能。

local M               = {}
local util            = require("luci.model.mynet.util")

local DEFAULT_TIMEOUT = 30 -- 秒

-- ─────────────────────────────────────────────────────────────
-- 内部：构建 curl 命令并执行
-- 返回: body_string, http_status_code, error_string
-- ─────────────────────────────────────────────────────────────

local function do_curl(method, url, json_body, extra_headers, accept_type, timeout)
    local t = timeout or DEFAULT_TIMEOUT

    -- 使用 -D - 将响应头输出到 stdout，以便解析状态码（更可靠）
    -- -w "\n__STATUS:%{http_code}" 在末尾附加状态码标记
    local parts = {
        "curl", "-s",
        "-w", "'\n__STATUS:%{http_code}'",
        "-m", tostring(t),
        "-X", method,
    }

    -- 请求头
    local headers = {
        ["User-Agent"] = "mynet-luci/2.1.0",
        ["Accept"]     = accept_type or "application/json",
    }
    for k, v in pairs(extra_headers or {}) do
        headers[k] = v
    end
    for k, v in pairs(headers) do
        -- 安全转义单引号
        local safe_v = tostring(v):gsub("'", "'\\''")
        parts[#parts + 1] = string.format("-H '%s: %s'", k, safe_v)
    end

    -- 请求体
    if json_body then
        local safe_body = json_body:gsub("'", "'\\''")
        parts[#parts + 1] = "-H 'Content-Type: application/json'"
        parts[#parts + 1] = "--data '" .. safe_body .. "'"
    end

    -- URL
    local safe_url = url:gsub("'", "'\\''")
    parts[#parts + 1] = "'" .. safe_url .. "'"

    local cmd = table.concat(parts, " ")
    local raw = util.exec(cmd)

    if not raw then
        return nil, 0, "curl execution failed"
    end

    -- 从末尾提取 HTTP 状态码
    local status_str  = raw:match("__STATUS:(%d+)'?%s*$")
    local body        = raw:gsub("\n?%'?__STATUS:%d+'?%s*$", "")
    local status_code = tonumber(status_str) or 0

    return body, status_code, nil
end

-- ─────────────────────────────────────────────────────────────
-- 公开接口
-- ─────────────────────────────────────────────────────────────

-- 构建鉴权请求头
local function auth_headers(token, zone_id)
    local h = {}
    if token and token ~= "" then
        h["Authorization"] = "Bearer " .. token
    end
    local zid = tostring(zone_id or "")
    if zid ~= "" and zid ~= "0" then
        h["X-Tenant-ID"] = zid
    end
    -- Token Binding：指纹随请求；与登录绑定不一致则 back 401（integration-plan §14.4）
    local fp = util.device_fingerprint()
    if fp and fp ~= "" then
        h["X-Device-Fingerprint"] = fp
    end
    return h
end

-- 解析 JSON 响应（公共逻辑，消除 get/post/patch/put 中的重复）
local function parse_json_response(body, status, err)
    if err then return nil, err end
    if status == 0 then return nil, "connection failed (timeout or unreachable)" end
    local data = util.json_decode(body)
    if not data then
        return nil, string.format("invalid JSON (http=%d body=%s)", status, (body or ""):sub(1, 200))
    end
    if status >= 400 then
        return nil, string.format("api error %d: %s", status, tostring(data.message or "unknown"))
    end
    return data, nil
end

-- GET → JSON table
-- returns: (table, nil)  或  (nil, error_string)
function M.get_json(base_url, endpoint, token, zone_id)
    local url = base_url .. endpoint
    local body, status, err = do_curl("GET", url, nil, auth_headers(token, zone_id))
    return parse_json_response(body, status, err)
end

-- POST JSON body → JSON table
function M.post_json(base_url, endpoint, payload, token, zone_id)
    local url               = base_url .. endpoint
    local body_str          = util.json_encode(payload) or "{}"
    local resp, status, err = do_curl("POST", url, body_str, auth_headers(token, zone_id))
    return parse_json_response(resp, status, err)
end

-- PATCH JSON body → JSON table
function M.patch_json(base_url, endpoint, payload, token, zone_id)
    local url               = base_url .. endpoint
    local body_str          = util.json_encode(payload) or "{}"
    local resp, status, err = do_curl("PATCH", url, body_str, auth_headers(token, zone_id))
    return parse_json_response(resp, status, err)
end

-- PUT JSON body → JSON table
function M.put_json(base_url, endpoint, payload, token, zone_id)
    local url               = base_url .. endpoint
    local body_str          = util.json_encode(payload) or "{}"
    local resp, status, err = do_curl("PUT", url, body_str, auth_headers(token, zone_id))
    return parse_json_response(resp, status, err)
end

-- GET plain text（用于 render_conf=1 接口，直接返回配置文本）
-- 对应 Go: GetNodeConfigRendered / GetRouteConfigRendered
function M.get_text(base_url, endpoint, token, zone_id)
    local url               = base_url .. endpoint
    local body, status, err = do_curl("GET", url, nil, auth_headers(token, zone_id), "text/plain")
    if err then return nil, err end
    if status == 0 then return nil, "connection failed" end
    if status >= 300 and status < 400 then
        return nil, string.format("unexpected redirect %d (token expired?)", status)
    end
    if status >= 400 then
        return nil, string.format("api error %d", status)
    end
    return util.trim(body or ""), nil
end

-- 将 base_url 的 API 版本替换为指定版本
-- base_url 格式: https://host/api/v1 → https://host/api/v2
local function versioned_base(base_url, version)
    return (base_url:gsub("/api/v%d+$", "/api/" .. version))
end

-- ─────────────────────────────────────────────────────────────
-- Config Bundle — 单次请求获取全部配置 + 密钥
-- GET /api/v2/nodes/{id}/config-bundle
-- 返回: (bundle_table, nil) 或 (nil, error_string)
-- bundle_table: { files={}, keys={} }
-- fallback: 若 404 则返回 nil, "not_supported"
-- ─────────────────────────────────────────────────────────────
function M.get_config_bundle(base_url, node_id_str, token, zone_id)
    local endpoint = "/nodes/" .. node_id_str .. "/config-bundle"
    local url = versioned_base(base_url, "v2") .. endpoint
    local body, status, err = do_curl("GET", url, nil, auth_headers(token, zone_id))
    if err then return nil, err end
    if status == 0 then return nil, "connection failed" end
    if status >= 300 and status < 400 then
        return nil, string.format("unexpected redirect %d (token expired?)", status)
    end
    if status == 404 then
        -- 区分“后端不支持 bundle API”与“节点未找到”
        local data = util.json_decode(body)
        local msg = data and data.message or ""
        if msg:lower():find("not found") then
            return nil, "node not found (404): " .. msg
        end
        return nil, "not_supported"
    end
    if status >= 400 then
        return nil, string.format("api error %d", status)
    end
    local data = util.json_decode(body)
    if not data then
        return nil, "invalid JSON in config-bundle response"
    end
    return data, nil
end

-- ─────────────────────────────────────────────────────────────
-- 批量获取 peer 公钥
-- POST /nodes/{id}/router-keys (v1)
-- 返回: (keys_table, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.get_router_keys(base_url, node_id_str, token, zone_id)
    local endpoint = "/nodes/" .. node_id_str .. "/router-keys"
    local url = base_url .. endpoint
    local resp, status, err = do_curl("POST", url, "{}", auth_headers(token, zone_id))
    if err then return nil, err end
    if status == 0 then return nil, "connection failed" end
    if status == 404 then return nil, "not_supported" end
    if status >= 400 then
        return nil, string.format("api error %d", status)
    end
    local data = util.json_decode(resp)
    if not data then
        return nil, "invalid JSON in router-keys response"
    end
    return data, nil
end

-- ─────────────────────────────────────────────────────────────
-- Heartbeat — POST /api/v2/monitor/heartbeat（Node-Sig 认证）
-- 使用 X-Node-Id + X-Timestamp + X-Node-Signature 请求头
-- 返回: (response_table, nil) 或 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.post_heartbeat(base_url, node_id_str, timestamp, body_json, signature)
    local url = versioned_base(base_url, "v2") .. "/monitor/heartbeat"
    local headers = {
        ["X-Node-Id"]        = node_id_str,
        ["X-Timestamp"]      = tostring(timestamp),
        ["X-Node-Signature"] = signature,
    }
    local resp, status, err = do_curl("POST", url, body_json, headers)
    if err then return nil, err end
    if status == 0 then return nil, "connection failed" end
    if status >= 400 then
        return nil, string.format("api error %d: %s", status, (resp or ""):sub(1, 200))
    end
    local data = util.json_decode(resp)
    if not data then
        return nil, "invalid JSON in heartbeat response"
    end
    return data, nil
end

return M
