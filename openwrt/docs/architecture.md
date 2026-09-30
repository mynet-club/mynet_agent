# 架构设计说明

---

## 整体架构

```
浏览器
  │  HTTP (LuCI CGI)
  ▼
┌─────────────────────────────────────────┐
│  LuCI 框架 (luci-base)                  │
│  ┌───────────────────────────────────┐  │
│  │  controller/mynet.lua             │  │  路由 + Action/API handler
│  │  ┌─────────────────────────────┐  │  │
│  │  │  model/mynet/               │  │  │  业务逻辑层
│  │  │  ├─ api.lua         (REST)  │  │  │
│  │  │  ├─ auth.lua       (认证)   │  │  │
│  │  │  ├─ config.lua     (配置)   │  │  │
│  │  │  ├─ credential.lua (凭证)   │  │  │
│  │  │  ├─ gnb_installer  (安装)   │  │  │
│  │  │  ├─ guest.lua      (离线)   │  │  │
│  │  │  ├─ node.lua       (节点)   │  │  │
│  │  │  ├─ proxy.lua      (分流)   │  │  │
│  │  │  ├─ system.lua     (系统)   │  │  │
│  │  │  ├─ util.lua       (工具)   │  │  │
│  │  │  ├─ validator.lua  (校验)   │  │  │
│  │  │  └─ zone.lua       (Zone)   │  │  │
│  │  └─────────────────────────────┘  │  │
│  └───────────────────────────────────┘  │
│  ┌───────────────────────────────────┐  │
│  │  view/mynet/*.htm                 │  │  模板渲染
│  └───────────────────────────────────┘  │
│  ┌───────────────────────────────────┐  │
│  │  js/mynet.js + css/mynet.css      │  │  前端资源
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
  │  curl HTTP/HTTPS            │  shell exec
  ▼                             ▼
MyNet API 服务端           GNB VPN + 系统工具
```

---

## 模块职责

### controller/mynet.lua (~2500 行)
- 注册 LuCI 菜单和 URL 路由（10+ 页面 + 50+ API）
- 统一认证中间件：`require_auth()` / `require_api_auth()` / `require_local_api()`
- 频率限制：`check_rate_limit()` (Login 10/min, API 60/min)
- 所有 AJAX API 返回 JSON 格式

### model/mynet/

| 模块 | 行数 | 职责 |
|------|------|------|
| `util.lua` | ~400 | 文件 I/O、Shell 执行、JSON 编解码、日志、路径常量 |
| `api.lua` | ~190 | curl HTTP 客户端（统一 GET/POST/PUT，TLS fallback） |
| `auth.lua` | ~165 | 登录、登出、Token 刷新、凭证恢复链（refresh → re-login） |
| `config.lua` | ~325 | 本地配置读写（config.json、zone.json、mynet.conf 生成） |
| `credential.lua` | ~76 | 凭证持久化（/etc/mynet/conf/credential.json） |
| `node.lua` | ~1450 | 节点管理、配置同步（bundle + legacy fallback）、密钥管理、GNB 启停 |
| `zone.lua` | ~60 | Zone 区域列表获取 |
| `guest.lua` | ~580 | 离线模式：网络创建/导入/导出、节点 CRUD、路由生成 |
| `proxy.lua` | ~620 | 代理分流：策略路由注入/恢复、nftables nft set、DNS 劫持（redirect 模式）|
| `system.lua` | ~620 | 系统检测、防火墙管理、依赖检查、心跳 |
| `gnb_installer.lua` | ~580 | GNB 平台检测、manifest 解析、后台下载安装 |
| `validator.lua` | ~180 | 10 项配置完整性检查 + 自动修复 |

### view/mynet/ — 页面模板

| 模板 | 用途 |
|------|------|
| `index.htm` | Dashboard 控制台（状态、依赖、GNB 安装） |
| `wizard.htm` | 安装向导（Landing → Zone → Node 三步） |
| `login.htm` | MyNet 账号登录 |
| `guest.htm` | 离线网络配置管理（节点 CRUD + 导入/导出） |
| `node.htm` | 节点配置（config 查看/同步 + 密钥管理 + 节点列表） |
| `service.htm` | 运维中心（状态/网络/Peers/日志/诊断 5 个 Tab） |
| `plugin.htm` | 插件列表入口 |
| `proxy.htm` | 代理分流配置（模式/区域/DNS + 诊断） |
| `settings.htm` | 系统设置（GNB 二进制选择 + 依赖安装） |

### 兼容重定向（已合并的旧路由）

| 旧路由 | 重定向到 |
|--------|----------|
| `/status` | `/index` |
| `/zones` | `/node` |
| `/nodes` | `/node?tab=list` |
| `/node/manager` | `/node?tab=list` |
| `/diagnose` | `/service?tab=diagnose` |
| `/log` | `/service?tab=log` |
| `/network` | `/service?tab=network` |
| `/gnb` | `/service?tab=peers` |

### htdocs/luci-static/resources/mynet/
- `mynet.css`：应用样式（~260 行）——委托 Argon 主题变量
- `mynet.js`：前端逻辑（~550 行）——API 调用、Toast、Tab 切换、GNB 安装轮询

---

## 关键数据流

### 节点配置同步（Bundle API 优先）

```
用户点击「同步配置」
  │
  ▼ api_node_refresh_config()
  │
  ├─ node.refresh_configs_bundle(node_id)
  │     ├─ api.get_config_bundle()          ← 单请求获取全部
  │     │     └─ 写入 node.conf / route.conf / address.conf
  │     │     └─ 写入 ed25519/*.public (对端公钥)
  │     │     └─ 写入 security/*.private (自身密钥)
  │     │
  │     └─ [Fallback] refresh_configs()     ← 3 次独立请求
  │           ├─ api.get("/nodes/{id}/config")
  │           ├─ api.get("/route/node/{id}")
  │           └─ api.get("/nodes/{id}/keys")
  │
  └─ 返回 JSON { success, files[], errors[], method }
```

### 服务启停

```
启动：/etc/init.d/mynet start
  → 读取 mynet.conf（NODE_ID、路径）
  → modprobe tun
  → 执行 proxy/pre_start.sh hook（预注入 GNB 数据路由）
  → 启动 gnb -c {conf_dir} -d
  → route.mynet apply（注入路由表项）
  → firewall.mynet apply（配置防火墙 zone）
  → 执行 proxy/post_start.sh hook（若 PROXY_ENABLED=1 则调用 proxy.start()）

停止：/etc/init.d/mynet stop
  → 执行 proxy/stop.sh hook
      → route_policy.sh stop（flush nft chain、删除 ip rule/table 200、恢复 dnsmasq）
      → dns_intercept chain 清除（若 DNS_MODE=redirect）
  → 终止 gnb 进程
  → route.mynet clear（清除路由）
```

---

## 安全设计

| 层面 | 措施 |
|------|------|
| **认证** | JWT Token + refresh + 凭证恢复链 |
| **API 频率限制** | 按 IP hash 限流（/tmp/mynet_rate/） |
| **参数校验** | `parse_node_id()` 统一校验 node_id 参数 |
| **Shell 安全** | `util.shell_escape()` 转义所有 Shell 参数 |
| **文件权限** | 敏感文件（凭证/私钥）使用 `write_file_secure()` (0600) |
| **大整数安全** | Lua 侧 `util.int_str()`，JS 侧字符串传递 |
| **输入过滤** | HTML 使用 `pcdata()` 转义输出 |

---

## 认证流程

1. 用户在登录页提交邮箱/密码
2. `auth.login()` → `POST /auth/login` → 获取 `access_token` + `refresh_token` + `expires_at`
3. Token 写入 `/etc/mynet/conf/credential.json` (chmod 0600)
4. 后续 API 请求自动注入 `Authorization: Bearer` Header
5. Token 过期时自动恢复链: refresh_token 续期 → 失败则重登录 → 全部失败跳登录页

---

## 运行时路径

> 所有路径定义在 `util.lua` 常量中

```
/etc/mynet/                 MYNET_HOME
├── conf/                   CONF_DIR
│   ├── config.json         设备配置
│   ├── credential.json     登录凭证 (0600)
│   ├── mynet.conf          GNB 主配置
│   ├── zone.json           当前 Zone
│   └── proxy/
│       └── proxy_role.conf     代理持久化配置（PROXY_ENABLED/DNS_MODE/PROXY_MODE 等）
├── driver/gnb/             GNB_DRIVER_ROOT
│   ├── bin/gnb             GNB 二进制
│   ├── bin/gnb_ctl         GNB 控制工具
│   └── conf/{node_id}/     GNB_CONF_DIR
│       ├── node.conf
│       ├── route.conf
│       ├── address.conf
│       ├── ed25519/*.public
│       └── security/*.private
├── scripts/                SCRIPTS_DIR
│   ├── route.mynet         路由管理脚本
│   ├── firewall.mynet      防火墙管理脚本
│   └── proxy/              代理分流脚本
│       ├── route_policy.sh     策略路由/nftables 主脚本（proxy.lua 调用此路径）
│       ├── proxy.sh            分流规则生成（IP 列表 → nft set）
│       └── hooks/
│           ├── pre_start.sh    GNB 启动前预注入代理路由
│           ├── post_start.sh   GNB 启动后启动代理（读 proxy_role.conf）
│           └── stop.sh         停止代理 + 清除 nft 策略路由
├── var/                    运行时状态（自动建立）
│   ├── proxy_state.json    代理状态（mode/dns_mode/start_ts/region）
│   └── proxy_policy_params.env  bash 参数文件；proxy.lua start() 在调 route_policy.sh 前更新
│                           DNS_MODE/DNS_SERVER 字段，route_policy.sh 只读取不生成
└── logs/                   运行日志
```

---

## 流程图

详细的 Mermaid 流程图见 [FLOWS.md](FLOWS.md)，包括：
- 安装向导流程
- 节点切换流程
- GNB 自动安装流程
- VPN 服务启动流程
- 代理分流运行流程
- 认证时序图
- 页面导航总览
