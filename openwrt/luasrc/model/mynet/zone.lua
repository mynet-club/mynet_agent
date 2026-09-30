-- mynet/zone.lua  — 区域管理模块
-- 获取用户区域列表、读写当前选中区域。
-- 后端已迁移至 Tenant，接口层做字段映射，交互层保持 zone 命名不变。

local M    = {}
local api  = require("luci.model.mynet.api")
local auth = require("luci.model.mynet.auth")
local cfg  = require("luci.model.mynet.config")
local util = require("luci.model.mynet.util")

-- ─────────────────────────────────────────────────────────────
-- 获取用户可访问的区域列表
-- 后端端点: GET /user/tenants/  返回: { tenant_id, name, ... }
-- 接口层映射为 zone 兼容结构
-- ─────────────────────────────────────────────────────────────
function M.get_zones()
    local current, err = auth.ensure_valid()
    if err then return nil, err end

    local data, api_err = api.get_json(cfg.get_api_url(), "/user/tenants/", current.token)
    if api_err then return nil, api_err end
    if not data or not data.success then
        return nil, (data and data.message) or "failed to get zones"
    end

    -- 将 tenant 响应字段映射为 zone 兼容结构
    local raw = data.data or {}
    local zones = {}
    for i, t in ipairs(raw) do
        zones[i] = {
            tenant_id       = t.tenant_id,
            zone_id         = t.tenant_id,
            name            = t.name,
            zone_name       = t.name,
            description     = t.description,
            zone_remarks    = t.description or "",
            domain          = t.domain,
            zone_domain     = t.domain or "",
            status          = t.status,
            zone_status     = t.status == "active" and 1 or 0,
            admin_name      = t.admin_name,
            zone_admin_name = t.admin_name or "",
            pivot           = t.pivot,
        }
    end
    return zones, nil
end

-- ─────────────────────────────────────────────────────────────
-- 获取当前选中区域（本地存储）
-- 返回: { zone_id, zone_name } 或 nil
-- ─────────────────────────────────────────────────────────────
function M.get_current_zone()
    return cfg.load_current_zone()
end

-- ─────────────────────────────────────────────────────────────
-- 设置当前区域
-- ─────────────────────────────────────────────────────────────
function M.set_current_zone(zone_id, zone_name)
    return cfg.save_current_zone(zone_id, zone_name)
end

-- ─────────────────────────────────────────────────────────────
-- 在区域列表中查找指定 zone_id 的区域对象
-- ─────────────────────────────────────────────────────────────
function M.find_zone(zones, zone_id)
    for _, z in ipairs(zones or {}) do
        local zid = z.zone_id or z.id
        if util.int_str(zid) == util.int_str(zone_id) then
            return z
        end
    end
    return nil
end

return M
