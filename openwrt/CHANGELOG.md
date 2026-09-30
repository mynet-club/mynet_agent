# Changelog

## v2.1.8 (2026-04-21)

### Bug Fixes
- **config: `get_node_id()` 返回 string 而非 number** — 原来 `return tonumber(v)` 在 Lua 中对 > 1e14 的整数调用 `tostring()` / `..` 拼接时产生科学计数法（如 `3.722810996882e+15`），改为 `return util.int_str(tonumber(v))` 始终返回十进制字符串；同步修正所有 `node_id == 0` 比较为 `node_id == "0"`（涉及 config.lua / system.lua / node.lua / controller）
- **proxy: 重启不清除 route.conf 注入标记** — `stop.sh` hook 移除 `sed` marker 清理；新增 `proxy.lua M.pre_start()`，由 `pre_start.sh` hook 在 GNB 启动前调用，确保 route.conf 注入标记在 GNB 每次启动前都存在

## v2.1.7 (2026-04-21)

### Refactor
- **proxy: 明确分离 enable/disable 与 start/stop 语义**
  - `enable/disable`：配置层 — 负责写 proxy_role.conf、注入/移除 GNB route.conf 、安装/移除 plugin hooks
  - `start/stop`：运行层 — 只操作 nftables/ip rules，不走 route.conf
  - `reload`：用已有 IP 文件重生配置，不触发网络下载
- **proxy: start/stop/enable/disable 全面幂等优化**
  - `start()`：已运行 → `"already running"` 直接返回
  - `start()`：未 enable 则拒绝并返回明确错误 `"proxy not enabled — call enable() first"`
  - `stop()`：未运行 → `"already stopped"` 直接返回
  - `disable()`：未 enabled → `"already disabled"` 直接返回
  - `enable()`：已 enabled 且参数未变且 route 已注入 → 跳过 route_inject（避免重复写入 GNB route.conf）

### Added
- **打包内置 IP 列表**：ipk 随包预装 `interip.txt`（17809 条）和 `chinaip.txt`（8324 条），首次安装无需网络下载

### Bug Fixes
- **proxy.sh: 缓存跳过时未检测网关 IP 变更** — 仅比较 IP 文件 mtime 而忽略 peer 变更导致的网关 IP 变更，现在同时比较 `# Gateway:` 注释，不一致则强制重生

### Bug Fixes
- **node.lua: route.conf 路由格式修正** — `generate_route_conf()` 输出从 `cidr via peer_vip`（内核报 "Nexthop has invalid gateway"）改为 `cidr dev vpn_iface`，修复 5 条跨 zone 子网路由无法添加的问题
- **proxy hooks: 统一调用 Lua proxy 模块** — `post_start.sh` / `stop.sh` 从直接调用 shell `proxy.sh` 改为 `lua -e` 调用 `proxy.start()` / `proxy.stop()`

## v2.1.5 (2026-04-21)

### Bug Fixes
- **node.lua: BusyBox sleep 不支持小数** — `wait_for_iface()` 中 `sleep 0.3` × 15 次改为 `sleep 1` × 5 次（等效超时不变），`restart_service()` 中 `sleep 0.5` 同步修正，避免 BusyBox 报 `invalid number`
- **proxy.lua: net_check() shell_escape 逻辑矛盾** — `util.shell_escape(host):gsub("'","")` 等价于不做任何保护（包裹后即剥除），修正为直接使用 `host`（已有 `^[%w%.%-]+$` 入口校验保证安全）

### Code Quality
- **controller.lua: 提取 `gnb_ctl_query()` 辅助函数** — `action_node()` / `action_service()` / `api_dashboard_stats()` / `api_gnb_monitor_data()` 中 4 处相同的 `cd ... && ./bin/gnb_ctl -s -b` 命令合并为单一私有函数
- **controller.lua: 删除 `action_proxy()` 局部变量遮蔽** — 函数内重复 `local node_m/cfg_m = require(...)` 与模块级同名变量冲突，已删除冗余声明
- **controller.lua: 删除死代码** — `action_service_op()` 的 `network` 分支中 `svc_status/svc_start/svc_stop/svc_restart` 操作不可达（已被 `mynet` 分支覆盖），予以移除
- **system.lua: 合并 `_handle_heartbeat_commands()` 重复 require** — 循环内 6 个分支各自 `require("luci.model.mynet.node")` 合并为循环外一次加载
- **版本号对齐** — `util.APP_VERSION` 从 `2.1.0` 更新至 `2.1.5`

### Removed
- `docs/UPGRADE_PLAN.md` — 升级阶段均已完成
- `docs/UPGRADE-v2.md` — v1→v2 迁移已完成
- `docs/TEST_REPORT.md` — 已被 TEST_REPORT_E2E_20260419.md 取代
- `docs/MACOS_GATEWAY_PLAN.md` / `docs/MacOS_Openwrt_IPSET_Proxy.md` — macOS 开发环境文档与 OpenWrt 插件无关

## v2.1.4 (2026-04-20)

### Bug Fixes
- **删除 network.conf 中间层** — `generate_network_conf()` 重命名为 `generate_route_conf()`，不再写入冗余的 per-node `network.conf`，只保留 `/etc/mynet/conf/route.conf` 输出
- **route.conf 生成过滤所有 proxy 标记段** — `parse_gnb_route_conf()` 同时跳过 client (`#----proxy start/end----`) 和 server (`#----proxy-server start/end----`) marker 段，防止策略路由条目泄漏到 OS 内核路由表导致路由爆炸（714 条）
- **route.conf 空内容保护** — `refresh_single_config("route")` 和 `refresh_configs_bundle()` 拒绝写入空的 API 响应

## v2.1.3 (2026-04-20)

### Bug Fixes
- **api.lua: 3xx 重定向穿透** — `get_text()` 和 `get_config_bundle()` 新增 HTTP 3xx 检测，token 过期 302 跳转不再被当作成功响应写入配置文件
- **api.lua: config-bundle 404 区分** — 明确区分"后端不支持 bundle API"与"节点未找到"（后者给出明确错误提示而非静默 fallback）
- **node.lua: address.conf 空内容保护** — `refresh_single_config("address")` 收到空响应时返回错误而非写入空文件

## v2.1.2 (2026-04-20)

### Features
- **proxy route_inject server/both 模式自身放行** — `route_inject()` 升级：
  - `server`/`both` 模式下为自身节点注入全网放行路由（`#----proxy-server start----` 段）
  - `client`/`both` 模式出口路由逻辑同步升级：对标 mynet_tui `GenerateFixedProxyRoutes`，172/192 局部段改用 `/16` 拆分排除私有子网
  - `route_restore()` 同时清除 client + server 两个 marker 段
  - `route_inject_status()` 新增 `has_client_routes`/`has_server_routes` 字段

## v2.1.1 (2026-04-20)

### Chore
- **清理未使用脚本** — 删除 Makefile 未引用的 5 个冗余脚本文件（共 4664 行）：
  - `scripts/install/install.sh`（已被 ipk/Makefile 替代）
  - `scripts/_src/common/{common,route,vpn}.sh`（未使用的跨平台库）
  - `scripts/_src/openwrt/service-manager.sh`（手动运维脚本）

## v2.1.0 (2026-04-19)

### Features
- **心跳 v2 协议** — `submit_heartbeat()` 和 `run_daemon_heartbeat()` 升级到 `POST /api/v2/monitor/heartbeat`，使用 HMAC-SHA256 Node-Sig 认证（移除旧 PATCH + Bearer 模式）
- **心跳命令响应** — 处理服务端下发的 `commands` 数组：`config.refresh` / `service.restart` / `gnb.restart` / `gnb.start` / `gnb.stop` / `route.refresh` 自动执行
- **config-bundle warnings** — 解析 back v2.2.0 新增的 `warnings` 字段（如缺失对等节点公钥），日志记录并返回前端
- **心跳 body v2 格式** — `vpn_interface` (单数) → `vpn_interfaces` (数组)，新增 `status`/`uptime` 字段

### API Changes
- `api.lua`: 新增 `post_heartbeat()` 方法（Node-Sig 认证，X-Node-Id/X-Timestamp/X-Node-Signature 请求头）
- `system.lua`: `submit_heartbeat()` 不再依赖 Bearer Token，改用公钥 HMAC 签名
- Heartbeat HMAC 签名路径从 `/api/v1/` 更新为 `/api/v2/monitor/heartbeat`
- 对齐 `api-versions.json`: min_back_api 2.2.0

## v2.0.2 (2026-04-19)

### Improvements
- **rc.mynet 精简重构** — 从 737 行精简至 243 行（-67%）。GNB 进程管理（pre-flight/spawn/gnb_es 清理）完全委托给 `luci.model.mynet.node`，shell 仅保留配置加载、route/firewall 脚本调用和插件钩子
- **gnb_es PPID 精确清理** — stop_gnb 改用 PPID 匹配（/proc/{pid}/stat 字段 4）定位 gnb 的 gnb_es 子进程，避免错误 kill 其他 GNB 实例的 gnb_es；二进制路径匹配作为兜底
- **T7 智能提前结束** — E2E 测试中等待 P2P 连通性改为每 5s 探测对端 IP，任一对端 ping 通即提前结束，最长等待 120s

### Fixes
- proxy.sh base_url 路径修正（`/d/plugins/mp`）
- nftables inet 表 dnat 添加 ip 地址族限定词，修复地址族不匹配错误

## v2.0.0 (2026-04-06)

### Features
- **安装向导** — Landing 页模式选择（MyNet 在线 / 离线模式），Zone→Node 三步引导
- **MyNet 在线模式** — 登录/Token 自动刷新 → 选区域 → 选节点 → 配置自动下载（Bundle API 优先）
- **离线/Guest 模式** — 无需 MyNet 账号：创建本地 GNB 网络 / 导入 .tar.gz 配置包 / 节点 CRUD
- **节点管理** — node.conf / route.conf / address.conf 配置查看与同步，ed25519 密钥生成/导入/上传
- **GNB 服务控制** — 启动/停止/重启，实时状态监控，GNB Peers 连接状态
- **代理分流** — 客户端/服务端模式，国内/海外路由策略，DNS 劫持，网络诊断
- **GNB 自动安装** — 平台检测 + manifest 解析 + 后台下载安装，支持 arm64/x86_64
- **运维中心** — 统一 Operations 页面（状态/网络/Peers/日志/诊断 5 个 Tab）
- **插件系统** — 插件列表入口（Proxy Traffic Split + Remote Monitoring 预留）
- **系统诊断** — 依赖检查、10 项配置校验 + 自动修复、防火墙管理
- **完整中文化** — 380+ 条 PO 翻译，LuCI 标准 i18n
- **安全加固** — API 频率限制、参数校验（防注入）、敏感文件 0600 权限

### Architecture
- 12 个 Model 模块 + 50+ API 端点 + 9 个活跃页面
- Bundle API 配置同步（单请求获取全部配置+密钥），自动 fallback 到 legacy 模式
- GitHub Actions CI/CD — push tag 自动构建 ipk 并发布 GitHub Release

### Documentation
- 8 个 Mermaid 流程图（向导/切换/安装/启动/代理/导航/认证/同步）
- 完整架构设计文档（模块职责/数据流/安全设计/运行时路径）

### i18n
- 完整 i18n 覆盖：463 条翻译（JS + 内联模板字符串）
- 新增 `_i18n_js.htm` 翻译桥接（静态 JS 文件 i18n 支持）
