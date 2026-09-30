-- mynet/credential.lua  — 凭证持久化管理
-- 读写 /etc/mynet/conf/credential.json，格式与 Go 项目一致。
-- 对应 Go 项目 types/credential.go + infrastructure/credential_repo.go

local M    = {}
local util = require("luci.model.mynet.util")

M.CRED_FILE = util.CRED_FILE  -- /etc/mynet/conf/credential.json

-- ─────────────────────────────────────────────────────────────
-- 加载凭证（从 JSON 文件）
-- 返回 credential table 或 nil（文件不存在/无效）
-- ─────────────────────────────────────────────────────────────
function M.load()
    local data = util.load_json_file(M.CRED_FILE)
    if not data then return nil end
    return {
        user_id       = util.int_str(data.user_id    or 0),
        user_email    = data.user_email    or "",
        token         = data.token         or "",
        refresh_token = data.refresh_token or "",
        machine_id    = data.machine_id    or "",
        device_fingerprint = data.device_fingerprint or "",
        created_at    = data.created_at    or "",
        expires_at    = data.expires_at    or "",
        zone_id       = util.int_str(data.zone_id    or 0),
    }
end

-- ─────────────────────────────────────────────────────────────
-- 保存凭证
-- 返回 true 或 (false, error_string)
-- ─────────────────────────────────────────────────────────────
function M.save(c)
    if not c or not c.token or c.token == "" then
        return false, "empty token"
    end
    local json_str = util.json_encode({
        user_id       = util.int_str(c.user_id  or 0),
        user_email    = c.user_email    or "",
        token         = c.token,
        refresh_token = c.refresh_token or "",
        machine_id    = c.machine_id    or "",
        device_fingerprint = c.device_fingerprint or "",
        created_at    = c.created_at    or "",
        expires_at    = c.expires_at    or "",
        zone_id       = util.int_str(c.zone_id  or 0),
    })
    if not json_str then return false, "json encode failed" end
    return util.write_file_secure(M.CRED_FILE, json_str)
end

-- ─────────────────────────────────────────────────────────────
-- 清除凭证（登出时调用）
-- ─────────────────────────────────────────────────────────────
function M.clear()
    os.remove(M.CRED_FILE)
end

-- ─────────────────────────────────────────────────────────────
-- 判断 token 是否已过期（含 30 秒提前量，与 Go 保持一致）
-- ─────────────────────────────────────────────────────────────
function M.is_expired(c)
    if not c or not c.token or c.token == "" then return true end
    local exp = util.parse_time(c.expires_at)
    if exp == 0 then return false end          -- 未知过期时间 → 假设有效
    return os.time() + 30 >= exp
end

-- ─────────────────────────────────────────────────────────────
-- 判断凭证是否有效（token 存在且未过期）
-- ─────────────────────────────────────────────────────────────
function M.is_valid(c)
    if not c or not c.token or c.token == "" then return false end
    return not M.is_expired(c)
end

return M
