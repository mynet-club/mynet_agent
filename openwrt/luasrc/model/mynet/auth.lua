-- mynet/auth.lua  — 认证模块
-- 提供登录、登出、Token 刷新、Token 验证功能。
-- 对应 Go 项目 internal/application/user_service.go +
--              internal/application/session_service.go

local M    = {}
local api  = require("luci.model.mynet.api")
local cred = require("luci.model.mynet.credential")
local cfg  = require("luci.model.mynet.config")
local util = require("luci.model.mynet.util")

-- ─────────────────────────────────────────────────────────────
-- 登录
-- 对应 Go: UserService.Login()
-- 成功: 返回 (credential_table, nil)
-- 失败: 返回 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.login(email, password)
    if not email or email == "" then return nil, "email is required" end
    if not password or password == "" then return nil, "password is required" end

    local api_url    = cfg.get_api_url()
    local machine_id = util.get_machine_id()
    local fingerprint = util.device_fingerprint()

    local payload    = {
        email       = email,
        password    = password,
        device_name = "openwrt-" .. machine_id:sub(1, 8),
        -- Token Binding：登录时声明设备指纹
        device_fingerprint = fingerprint,
    }

    local data, err  = api.post_json(api_url, "/auth/login", payload)
    if err then return nil, err end
    if not data or not data.success then
        return nil, (data and data.message) or "login failed"
    end
    if not data.data or not data.data.auth_token then
        return nil, "no token in server response"
    end

    local now          = util.time_now()
    local expires_in   = data.data.auth_token_expires_in or (7 * 24 * 3600)
    local user_info    = data.data.user or {}

    local new_cred     = {
        user_id       = util.int_str(user_info.user_id or user_info.id or 0),
        user_email    = email,
        token         = data.data.auth_token,
        refresh_token = data.data.refresh_token or "",
        machine_id    = machine_id,
        device_fingerprint = fingerprint,
        created_at    = util.format_time(now),
        expires_at    = util.format_time(now + expires_in),
        zone_id       = util.int_str(data.data.tenant_id or user_info.zone_id or 0),
    }

    local ok, save_err = cred.save(new_cred)
    if not ok then
        util.log_warn("auth.login: credential save failed: " .. (save_err or ""))
    end

    return new_cred, nil
end

-- ─────────────────────────────────────────────────────────────
-- 登出
-- 对应 Go: UserService.Logout()
-- ─────────────────────────────────────────────────────────────
function M.logout()
    local current = cred.load()
    if current and current.token ~= "" then
        local api_url = cfg.get_api_url()
        -- 尽力通知服务端，忽略错误
        api.post_json(api_url, "/auth/logout", {}, current.token)
    end
    cred.clear()
end

-- ─────────────────────────────────────────────────────────────
-- 刷新 Token
-- 对应 Go: UserService.RefreshToken()
-- 成功: 返回 (updated_credential, nil)
-- 失败: 返回 (nil, error_string)
-- ─────────────────────────────────────────────────────────────
function M.refresh_token()
    local current = cred.load()
    if not current or current.refresh_token == "" then
        return nil, "no refresh token available"
    end

    local api_url = cfg.get_api_url()
    local data, err = api.post_json(api_url, "/auth/refresh-token", {
        refresh_token = current.refresh_token,
    })
    if err then return nil, err end
    if not data or not data.success then
        return nil, (data and data.message) or "token refresh failed"
    end
    if not data.data or not data.data.auth_token then
        return nil, "no token in refresh response"
    end

    local now             = util.time_now()
    local expires_in      = data.data.auth_token_expires_in or (7 * 24 * 3600)

    current.token         = data.data.auth_token
    current.refresh_token = data.data.refresh_token or current.refresh_token
    current.created_at    = util.format_time(now)
    current.expires_at    = util.format_time(now + expires_in)

    local ok, save_err    = cred.save(current)
    if not ok then
        util.log_warn("auth.refresh_token: save failed: " .. (save_err or ""))
    end

    return current, nil
end

-- ─────────────────────────────────────────────────────────────
-- 验证 Token（服务端验证）
-- 对应 Go: Client.VerifyToken()
-- 返回: (valid_bool, user_id_or_error)
-- ─────────────────────────────────────────────────────────────
function M.verify_token()
    local current = cred.load()
    if not current or current.token == "" then
        return false, "not logged in"
    end

    local api_url = cfg.get_api_url()
    local data, err = api.get_json(api_url, "/auth/verify-token", current.token)
    if err then return false, err end
    if not data or not data.success then
        return false, (data and data.message) or "verification failed"
    end

    local d = data.data or {}
    return d.valid == true, d.user_id
end

-- ─────────────────────────────────────────────────────────────
-- 确保凭证有效（过期则自动刷新）
-- 对应 Go: config.LoadAndCreateClient + onTokenRefreshed callback
-- 成功: 返回 (credential, nil)
-- 失败: 返回 (nil, error_string) — 需用户重新登录
-- ─────────────────────────────────────────────────────────────
function M.ensure_valid()
    local current = cred.load()
    if not current then
        return nil, "not logged in"
    end
    -- Token 未过期，直接返回
    if not cred.is_expired(current) then
        return current, nil
    end
    -- 尝试 refresh_token 刷新
    util.log_info("auth.ensure_valid: token near expiry, refreshing...")
    local refreshed, err = M.refresh_token()
    if refreshed then
        return refreshed, nil
    end
    -- refresh 也失败 → 需要用户重新登录
    return nil, "token expired, please re-login: " .. (err or "")
end

return M
