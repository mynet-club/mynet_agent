# OpenWrt 部署测试报告 — 交互逻辑 + 用户界面

> 日期：2026-09-29
> 载体：老 `mynet_lua/debug` QEMU VM · OpenWrt 24.10.1 armsr-armv8 · `openwrt-qemu` / 192.168.101.2
> 计划：`TEST_PLAN_OPENWRT_UI_INTERACTION.md`
> 部署：`mynet_openwrt/debug/build-install.sh all` + LuCI 壳 scp 同步

---

## 1. 环境与部署结果

| 项 | 部署前 | 部署后 | 判定 |
|---|---|---|---|
| SSH / OpenWrt 24.10.1 | OK | OK | ✅ |
| mynetd + mynet 二进制 | 旧版 | v0.0.0-952011d-dirty | ✅ |
| `mynetd.init` 含 `MYNET_EXECUTOR=lua` | 无 | 有，进程 env 已生效 | ✅ |
| LuCI menu.d/luci-app-mynet.json | **缺失** | 已同步 | ✅ |
| model/mynet 12 模块语法 | — | 全部 `loadfile` OK | ✅ |
| 静态资源 mynet.js/css | — | 200 | ✅ |
| mynetd localapi `:8096` | OK | OK | ✅ |
| ubus `mynet.*` | 无 | 仍无（mynet.uc 未装） | ⚠️ 已知 |

---

## 2. 交互逻辑（P1）

### T1.3 命令分派（mynetd → lua -e）— 部分通过

| 用例 | 结果 | 证据 |
|---|---|---|
| `metrics --json` | ✅ 200，有 stdout | `bin=lua`，指标 JSON |
| `apply --clear` | ✅ 走 lua | `bin=lua` |
| `apply --auto` | ❌ 失败 | `读 system.json 失败`——缺 detector |
| 非法 argv `rm` | ✅ 400 拒绝 | `subcommand not allowed` |
| `command-result` 回报 | ✅ 200 | E-4 端点可用 |

**缺陷 D-1（高）**：`metrics` 响应体带完整 `script` 字段（lua 源码全量泄露到 API）。
应只回 stdout/stderr，不回脚本。

**缺陷 D-2（高）**：lua 执行器**拒绝 `detector`**，但 `mynet apply` 依赖 `system.json`。
→ OpenWrt 上「lua 执行器 + L6 apply」闭环断：要么 detector 搬 Lua，要么 apply 时自动 detector。

### T1.1 启停链 — 失败（阻断）

| 用例 | 结果 | 根因 |
|---|---|---|
| `stop_gnb(显式 node_id)` | ✅ 真停掉 | — |
| `stop_service(nil)` / `stop_gnb(nil)` | ❌ **静默假成功** | **D-3** |
| `start_service(nil)` | ❌ check 失败 | **D-3**（查 conf/0/） |
| `service stop` 经 mynetd | ❌ 20s 超时 | **D-3 + D-4** |

**缺陷 D-3（严重）**：`node.lua:13` `nid(v) = util.int_str(v)`，**nil 不回退 `cfg.get_node_id()`**：

```text
nid(nil) → "0"
→ 路径变成 /etc/mynet/driver/gnb/conf/0/
→ check_config 报「文件不存在」
→ stop_gnb 读空 pidfile，返回 true（空操作成功）
```

实测对照：

```text
cfg.get_node_id()          = "3764021915150630"
check_config(nil)          = false（查 conf/0）
check_config(3764021915150630) = true
gnb_is_running(nil)        = false
gnb_is_running(3764021915150630) = true
```

**影响**：mynetd → `lua -e` 的 `service start/stop`（不带 `--node-id`）全部空转或报错。
必须修 `nid()`：`v==nil 时 return util.int_str(cfg.get_node_id())`。

**缺陷 D-4（严重）**：`clear_firewall` → `fw4 reload` 在本 VM **挂死**（>45s 无返回）。
连带 `stop_service`/`apply_firewall` 卡死 → mynetd 60s 超时 / HTTP 20s 超时。

```text
fw4 check   → 0.05s 通过
fw4 reload  → 挂死（/var/run/fw4.lock 残留）
警告：Section mynet_proxy option 'reload' is not supported by fw4
```

### T1.4 认证 / 会话 — 部分通过

| 用例 | 结果 |
|---|---|
| LuCI 未登录 | 403 + `x-luci-login-required: yes`（预期） |
| credential.json | 存在，0600 ✅ |
| localapi Host 校验 | `evil.example` → `invalid host` ✅ |
| guest / token 过期恢复 | 本轮未单测（需登录态） |

### T1.5 危险操作确认 — 未单测（依赖 UI 登录）

---

## 3. 用户界面（P2）

### T2.1 资源与语法 — 通过

| 项 | 结果 |
|---|---|
| mynet.js 200 / 32KB | ✅ |
| mynet.css 200 / 12KB | ✅ |
| menu.d 一级菜单 MyNet + Dashboard/Login/… | ✅ |
| 13 个 Lua 文件 loadfile | ✅ 全过 |

### T2.2/T2.3 反馈与 WebUI — 有条件通过

| 项 | 结果 |
|---|---|
| API 错误可读 | ✅（如 system.json 缺失原因清晰） |
| lua 响应过噪 | ❌ 带整段 script（D-1） |
| `/ui/` embed | 瘦构建无 WebUI（OpenWrt 目标态，符合 D-11） |
| LuCI 页面渲染 | 需 sysauth，本轮未走完登录流 |

### T2.4 本机 API 面 — 通过

| 端点 | 结果 |
|---|---|
| GET status Host=127.0.0.1:8096 | 200 |
| GET status Host=evil.example | 400 invalid host |
| POST command 白名单 | 拒绝非法子命令 |
| POST command-result | 200 |

---

## 4. 缺陷清单（按优先级）

| ID | 级别 | 描述 | 位置 | 状态 |
|---|---|---|---|---|
| **D-3** | P0 | `nid(nil)` 不回退 node_id，启停静默假成功 | `node.lua:13` | 待修 |
| **D-4** | P0 | `fw4 reload` 挂死，拖死 stop/apply firewall | `node.lua:1784` + VM fw4 | 待查（VM 或 uci） |
| **D-2** | P1 | lua 执行器无 detector，apply 闭环断 | `executor_lua.go` planLuaScript | 待设计 |
| **D-1** | P1 | API 泄露 lua script 源码 | `executor_lua.go` execLua 返回值 | 待修 |
| D-5 | P2 | ubus `mynet.*` 未注册 | mynet.uc 未打包 | 已知 |
| D-6 | P2 | LuCI 登录流/UI 渲染未端到端 | 需浏览器或 sysauth | 后补 |

---

## 5. 对 192.168.0.2 升级的含义

| 结论 | 说明 |
|---|---|
| **不可带病升级启停路径** | D-3 会让远程 stop 假成功，生产路由器上等于「停不掉隧道」 |
| **D-4 必须在 ImmortalWrt 上复测** | 192.168.0.2 是 fw4/nft；若同样挂，升级窗口会被防火墙 reload 卡死 |
| 分派面（metrics/command-result/白名单） | 可用，lua 执行器主链通 |
| 部署工装（build-install.sh） | 可用，冒烟通过 |

### 建议闸门

1. 修 D-3 + 加测试（`nid(nil)` 必须解析到当前节点）
2. 在 VM 复现并解决 D-4（或 stop 路径去掉对 fw4 reload 的同步依赖）
3. 补 D-1（响应不带 script）
4. 再跑一轮 P1 全绿，才允许动 192.168.0.2

---

## 6. 实测摘录

```text
# 部署
[build-install] ✓ 冒烟通过
MYNET_EXECUTOR=lua

# D-3 对照
cfg.get_node_id() = 3764021915150630
check_config(nil) = false  → conf/0/node.conf
stop_gnb(3764021915150630) = true（真停）
stop_gnb(nil) = true（pidfile 为空，假成功）

# D-4
fw4 check  = 0.05s OK
fw4 reload = hang >45s
/var/run/fw4.lock 残留
```
