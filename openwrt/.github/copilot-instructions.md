# GitHub Copilot 编码规范 — mynet_lua

本文件基于项目历史错误总结的强制规范。所有 AI 辅助代码必须遵循。

---

## � 跨平台操作契约规范（Spec Sync — 必读）

> mynet_lua 与 mynet_client 是同一业务逻辑的两端实现。  
> 操作契约的 SSOT 在：`../mynet_ctl/docs/knowledge-base/spec/`

### 改动时的强制检查

修改以下任何一项**业务逻辑**前，必须先查阅对应 spec 文件，改动后必须同步更新 spec：

| 涉及模块 | 对应 spec 文件 |
|----------|---------------|
| 登录 / Token / 认证 | [spec/auth.md](../../mynet_ctl/docs/knowledge-base/spec/auth.md) |
| 向导 / Zone / 节点选择 / config-bundle | [spec/wizard.md](../../mynet_ctl/docs/knowledge-base/spec/wizard.md) |
| GNB 启停 / 路由注入 / 防火墙 | [spec/service.md](../../mynet_ctl/docs/knowledge-base/spec/service.md) |
| 代理分流 / DNS 策略 | [spec/proxy.md](../../mynet_ctl/docs/knowledge-base/spec/proxy.md) |
| 插件（heartbeat / updater） | [spec/plugins.md](../../mynet_ctl/docs/knowledge-base/spec/plugins.md) |
| JSON/conf 数据格式 | [spec/data-models.md](../../mynet_ctl/docs/knowledge-base/spec/data-models.md) |

### 判断是否需要更新 spec

```
改的是业务逻辑（步骤、状态机、错误码、数据格式）→ YES，必须更新 spec
改的是内部实现（换库、重构、日志）→ NO，不需要更新 spec
新功能 → YES，spec 先行
```

### 提交惯例

契约变更的 commit 加 `[spec]` 标签，提示 mynet_client 侧需要同步：
```
[spec] proxy: dns_mode 增加 split-forward 选项
```

---

## �🚫 禁止行为（Prohibited Behaviors）

### 1. node_id 科学计数法（Critical Bug）
```lua
-- ❌ 禁止：Lua 大整数用 tostring() 会产生 "3.84e+15" 科学计数法
local endpoint = "/nodes/" .. tostring(node_id)
local path = "/conf/" .. tostring(node_id) .. "/node.conf"

-- ✅ 必须：使用 util.int_str() 或本地 nid() helper
local function nid(v) return util.int_str(v) end
local endpoint = "/nodes/" .. nid(node_id)
```

### 2. JS 中 node_id 作为数字字面量（Critical Bug）
```lua
-- ❌ 禁止：大整数作为 JS 数字会丢失精度并产生科学计数法
window.mnCurrentNodeId = <%=cur_nid_str%>;

-- ✅ 必须：用字符串字面量（加引号）
window.mnCurrentNodeId = "<%=cur_nid_str%>";
```

### 3. API URL 硬编码路径
```javascript
// ❌ 禁止：硬编码路径中遗漏 /services/ 段
var baseUrl = window.location.pathname.replace(/\/admin\/mynet\//, '/admin/mynet/api/');

// ✅ 必须：使用 _mnApiBase() 函数
var url = _mnApiBase() + action;
```

### 4. JSON fallback 编码数字
```lua
-- ❌ 禁止：Lua fallback JSON encoder 直接 tostring(number)
if t == "number" then return tostring(obj) end

-- ✅ 必须：整数用 %.0f 格式
if t == "number" then
    if obj == math.floor(obj) and obj >= -1e15 and obj <= 1e15 then
        return string.format("%.0f", obj)
    end
    return tostring(obj)
end
```

### 5. 引用已废弃的视图/路由
以下文件/路由已删除，禁止引用：
- `luasrc/view/mynet/zones.htm` → 已删除
- `luasrc/view/mynet/nodes.htm` → 已删除
- `luasrc/view/mynet/node_detail.htm` → 已删除
- `/admin/services/mynet/zones` → 重定向到 `/node`
- `/admin/services/mynet/nodes` → 重定向到 `/node`
- `/admin/services/mynet/nodes/detail` → 不再存在

### 6. 私钥长度校验错误
```lua
-- ❌ 禁止：GNB 私钥是 64 字节 = 128 hex chars，不是 64
if #key_hex ~= 64 then ...

-- ✅ 正确：
if #key_hex ~= 128 then ...
```

### 7. 直接 require 在函数注册之外加载重模块
```lua
-- ❌ 禁止：顶层 require 重型模块会在每次请求时加载
local gnb_m = require("luci.model.mynet.gnb_installer")

-- ✅ 必须：在函数体内懒加载
function api_gnb_auto_install()
    local gnb_m = require("luci.model.mynet.gnb_installer")
    ...
end
```

### 8. LuCI 模板 `<% else %>` / `<% elseif %>` / `<% end %>` 中使用 `--` 注释（Critical Bug）
```lua
-- ❌ 禁止：C 模板解析器会将注释后的 write() 调用吞掉，导致行号偏移和语法错误
<% else -- tab == "config" %>
<% elseif x then -- fallback %>
<% end -- closing if %>

-- ✅ 必须：else/elseif/end 标签中不加 Lua 注释
<% else %>
<% elseif x then %>
<% end %>

-- ✅ 注释放在 HTML 注释（在 <% %> 标签外面）
<% end %> <!-- closing tab if -->
<% else %>
<!-- tab == "config" -->
```

---

## ⚠️ 开发流程规范（Process Rules）

### 9. 写功能前必须先通读现有代码（Critical Process）
```lua
-- ❌ 禁止：未查看现有模块就创建新的辅助函数
-- 示例：controller 里新建 check_service_preconditions()，
-- 但 validator.validate_config() / node.preflight_check() 已有完整检查

-- ✅ 必须：写新函数前先搜索现有模块
-- 1. 搜索 model/mynet/*.lua 中所有已有函数签名
-- 2. 搜索 util.lua 中已有常量和工具函数
-- 3. 确认无重复后再创建新函数
```
**已有的校验/检查函数速查**（禁止重复实现）：
- `validator.validate_config()` — 10 项配置完整性检查
- `node.preflight_check(node_id)` — 7 项启动前置检查（gnb/node.conf/route.conf/密钥/kmod-tun）
- `system.check_deps(node_id)` — 6 项依赖检查
- `system.run_health_check()` — 聚合健康检查
- `config.load_vpn_conf()` — 加载 mynet.conf
- `config.get_vpn_interface()` — 获取接口名（默认 "gnb_tun"）
- `config.get_node_id()` — 获取 NODE_ID
- `node.get_vpn_service_status()` — "running"/"stopped"

### 10. 脚本路径使用固定常量，不猜测搜索
```lua
-- ❌ 禁止：多路径猜测搜索脚本
local function find_fw_script()
    local paths = { p1, p2, p3 }
    for _, p in ipairs(paths) do ... end
end

-- ✅ 必须：使用 util.lua 中的固定路径常量
util.ROUTE_SCRIPT     -- /etc/mynet/scripts/route.mynet
util.FIREWALL_SCRIPT  -- /etc/mynet/scripts/firewall.mynet
```
ipk 安装时已将脚本部署到 `scripts/` 目录，无需运行时搜索。

---

## ✅ 必须遵守的规范

### node_id 处理
- **所有 node_id 都在 JavaScript 安全整数范围内（≤ 2^53），Lua number 可精确表示**
- 但 Lua `tostring()` 对大于 1e14 的数仍可能产生科学计数法（如 `"3.84e+15"`）
- 因此：Lua 侧所有 node_id 用于路径/API/字符串拼接时**必须**经过 `util.int_str(v)` 或 `nid(v)` 转换
- **禁止**在任何文件中使用 `tostring(node_id)` / `tostring(n.node_id)` 拼接路径或消息
- node.lua 顶部定义：`local function nid(v) return util.int_str(v) end`
- JS 侧 `window.mnCurrentNodeId` 必须是字符串

### API URL 构造
- JS 侧所有 API 调用必须使用 `_mnApiBase()` 函数
- `_mnApiBase()` 定义：`window.location.pathname.replace(/\/admin\/services\/mynet.*$/, '/admin/services/mynet/api/')`

### 依赖自动安装
- `gnb_ctl`：通过 `gnb_installer.start_auto_install()` 后台安装
- `kmod-tun`：`opkg install kmod-tun && modprobe tun`
- `bash`：`opkg install bash`
- `libcurl-gnutls4`：解决 mbedTLS TLS 握手失败
- `ca-bundle`：CA 证书包

### GNB 密钥格式
- 私钥：128 hex chars（64字节）= SHA512(32-byte-seed)
- 公钥：64 hex chars（32字节）
- `gnb_crypto -c` 总是生成新密钥对，不会复用已有私钥文件
- 上传公钥：`PUT /nodes/{id}/keys` body: `{ public_key: "hex..." }`

### view/model 约定
- 页面 node.htm 中 `nid_fmt()` 辅助函数用于格式化模板内整数
- 所有视图中引用 zone 时检查 `zone ~= nil` 和 `zone.zone_name ~= ""`
- route.conf 更新必须同时下载所有对端节点公钥写入 ed25519 目录

### Proxy 分流架构（Critical — 不要改错层）
代理流量**不走内核路由表**，而是走 nftables + 策略路由：
1. **route.conf 注入**：`proxy.route_inject()` 在 route.conf 末尾添加 `/8` 大段路由（`#----proxy begin----` ... `#----proxy end----`），告诉 GNB 数据层"这些目标 IP 通过 VPN 隧道转发到 proxy peer"
2. **nftables set**：`route_policy.sh` 将 domestic/international IP 列表加载到 nft set `mynet_proxy`
3. **fwmark + 策略路由**：匹配 nft set 的包标记 `0xc8` → `ip rule fwmark 0xc8 lookup mynet_proxy` → `default via {peer_vip} dev gnb_tun_XX`
4. **只处理转发流量**：PREROUTING chain 标记，OUTPUT chain 不标记（避免 GNB 隧道流量循环）
5. **GNB 不支持热重载 route.conf**：route.conf 更新后需重启 GNB 才能加载新路由条目
- route.conf 写入统一走 `node.write_local_config(nid, "route", content)` → `apply_local_config_side_effects` → `proxy.route_inject()`
- **禁止**直接 `util.write_file()` 写 route.conf 绕过 side effects

### 部署
- 同步命令：`bash debug/sync.sh all`
- VM 文件删除需手动：`ssh openwrt-qemu "rm -f /path"`
- 语法验证：`ssh openwrt-qemu "lua /path/to/file.lua 2>&1"`（空输出=无错误）

---

## 项目架构速查

```
luasrc/
  controller/mynet.lua    路由注册 + Action handlers + API handlers
  model/mynet/
    api.lua               HTTP REST 客户端（curl封装）
    auth.lua              登录/Token刷新
    config.lua            本地配置读写
    credential.lua        凭证持久化
    gnb_installer.lua     GNB 自动检测安装
    node.lua              节点管理（nid() helper 在顶部）
    system.lua            系统信息/依赖检查
    util.lua              基础工具（int_str/json/exec/file IO/路径常量）
    validator.lua         配置完整性校验（10项检查）
    zone.lua              Zone 管理
  view/mynet/
    index.htm             Dashboard
    login.htm             登录
    node.htm              节点配置（主要操作页）
    wizard.htm            首次配置向导
    service.htm / settings.htm / guest.htm / diagnose.htm

htdocs/luci-static/resources/mynet/
  css/mynet.css
  js/mynet.js             前端：mnApi / mnNodeSwitch / mnNodeGenKey 等

scripts/
  runtime/                OpenWrt 运行时脚本源码（开发时编辑这里）
    rc.mynet              → /etc/init.d/mynet（init 脚本）
    route.mynet           → /etc/mynet/scripts/route.mynet（路由管理）
    firewall.mynet        → /etc/mynet/scripts/firewall.mynet（防火墙管理）

root/etc/mynet/conf/config.json   设备本地配置
```

### util.lua 路径常量（运行时）
```
MYNET_HOME        = /etc/mynet
CONF_DIR          = /etc/mynet/conf
VPN_CONF          = /etc/mynet/conf/mynet.conf
SCRIPTS_DIR       = /etc/mynet/scripts
ROUTE_SCRIPT      = /etc/mynet/scripts/route.mynet
FIREWALL_SCRIPT   = /etc/mynet/scripts/firewall.mynet
GNB_DRIVER_ROOT   = /etc/mynet/driver/gnb
GNB_CONF_DIR      = /etc/mynet/driver/gnb/conf
```
