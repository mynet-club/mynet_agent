# OpenWrt 部署测试计划 — 交互逻辑 + 用户界面

> 载体：老 `mynet_lua/debug` 的 QEMU VM（OpenWrt 24.10.1 armsr-armv8，`openwrt-qemu` / 192.168.101.2）
> 目标：验证 **mynet_openwrt（LuCI 壳）+ mynet_agent（mynetd/mynet）** 在 OpenWrt 上的
> ① 交互逻辑（流程是否走得通、反馈是否一致）② 用户界面（页面/控件/状态是否正确）
> 原则：**不改生产**；结论带 `file:line` 或命令输出；失败记 CPI 风格条目。

---

## 0. 环境基线（开测前）

| 项 | 期望 | 实测 |
|---|---|---|
| SSH `openwrt-qemu` | 可达 | ☐ |
| OpenWrt 版本 | 24.10.x armsr | ☐ |
| LuCI / uhttpd | 在跑，菜单有 MyNet | ☐ |
| `/usr/sbin/mynetd` | 在跑，`:8096` 有响应 | ☐ |
| `/usr/sbin/mynet` | 存在（L6 apply） | ☐ |
| LuCI `model/mynet/*` | 10+ 模块 | ☐ |
| `mynetd` env | `MYNET_EXECUTOR=lua` | ☐ |

---

## P1 交互逻辑（流程正确性）

### T1.1 服务启停链
| 步骤 | 操作 | 期望 |
|---|---|---|
| 1 | LuCI Dashboard「停止 VPN」 | Toast 成功；gnb 退出；路由清理 |
| 2 | 「启动 VPN」 | gnb 起来；tun 就绪；`mynet apply` 装路由 |
| 3 | `/etc/init.d/mynet restart` | procd respawn；不残留孤儿 gnb_es |

**通过判据**：pidfile+cmdline 精确匹配；无 `pkill`；TUN 状态与 UI 一致。

### T1.2 配置同步链
| 步骤 | 操作 | 期望 |
|---|---|---|
| 1 | LuCI「同步配置」 | bundle 优先，404 回退 legacy；写 node/route/keys |
| 2 | 同步后 | 自动生成 `mynet.conf`；route.conf 更新 |
| 3 | 失败注入（断网/token 过期） | 错误可读，不半写文件 |

### T1.3 mynetd 命令分派（E-4 / lua 执行器）
| 步骤 | 操作 | 期望 |
|---|---|---|
| 1 | `POST :8096/api/v1/command {"argv":["metrics","--json"]}` | 200 + JSON 指标 |
| 2 | `argv:["apply","--auto"]` | 路由装上（或明确报错） |
| 3 | `argv:["apply","--clear"]` | 只清隧道口路由 |
| 4 | 设 `MYNET_EXECUTOR=lua` 后重复 1–3 | 走 lua -e，结果同形 |
| 5 | Lua 回报 `POST :8096/api/v1/command-result` | 不 500；有 OnResult 时上行 |

### T1.4 认证 / 会话
| 步骤 | 操作 | 期望 |
|---|---|---|
| 1 | LuCI 未登录访问 MyNet | 跳登录或 local_mode |
| 2 | 登录成功 | credential.json 0600 |
| 3 | token 过期 | 自动 refresh，失败跳登录 |
| 4 | guest 模式 | 伪凭证，本地操作可用 |

### T1.5 危险操作确认
| 操作 | 期望 |
|---|---|
| 停止 VPN / 删除节点 / 生成新密钥 / fw_teardown | 有 confirm |
| 同步配置覆盖本地 | 有提示 |

---

## P2 用户界面（页面与反馈）

### T2.1 页面可达性
| 页面 | 路由 | 期望 |
|---|---|---|
| Dashboard | `/admin/services/mynet/index` | 200，状态点正确 |
| Node | `…/node` | 配置/列表 tab 可切 |
| Service | `…/service` | 五 tab：status/network/peers/log/diagnose |
| Proxy | `…/proxy` | 模式/DNS/诊断 |
| Settings | `…/settings` | GNB 二进制选择 |
| Wizard | `…/wizard` | 未配置时可走完 |

### T2.2 状态与反馈一致性
| 项 | 期望 |
|---|---|
| 操作反馈 | 统一 Toast；成功后**局部刷新**（非强制整页 reload） |
| 失败反馈 | 原因可见（含 stderr），非只报 exit code |
| 轮询 | Dashboard 15s / peers 60s 等仍在；状态不过期 |
| 危险按钮 | disabled/loading 态正确 |

### T2.3 WebUI（mynetd `/ui/`，对照）
| 项 | 期望 |
|---|---|
| `/ui/` 可开 | 200，三行壳层 |
| 概览状态 | 与 `/api/v1/status` 一致 |
| 长命令 | 不 15s 假失败；有进度 |
| 代理策略 | 双控意图可见 |

### T2.4 本机 API 面（给 UI 的）
| 端点 | 期望 |
|---|---|
| `GET /api/v1/status` | 200 |
| `GET /api/v1/metrics` | 200 JSON |
| `POST /api/v1/command` | 白名单外 400 |
| Origin/Host 校验 | 非法 Host 拒绝 |

---

## P3 部署与升级路径（为 192.168.0.2 铺路）

| # | 项 | 期望 |
|---|---|---|
| 3.1 | `build-install.sh` 一键构建安装 | mynetd+mynet 落盘，init 带 `MYNET_EXECUTOR=lua` |
| 3.2 | ipk 不变量 | 缺 files/mynetd|mynet 拒绝出包 |
| 3.3 | 重启持久化 | 重启后 mynetd/gnb 自动恢复 |
| 3.4 | 回滚 | 旧二进制+配置可恢复 |

---

## 执行顺序

```text
P0 基线 → P3.1 部署 → P1.3 分派 → P1.1 启停 → P1.2 配置
→ P1.4 认证 → P2 UI → P1.5/P2.2 反馈 → P3.3 重启
```

## 已知风险（先记账）

| ID | 风险 | 来源 |
|---|---|---|
| R1 | 旧 init 无 `MYNET_EXECUTOR=lua` | 本机现状 |
| R2 | LuCI menu.d/luci-app-mynet.json 缺失 | 本机现状 |
| R3 | apply 长命令 15s UI 超时 | WebUI index.html:1089 |
| R4 | Lua `service/op` 含 `sleep 1` | mynet.lua:1156 |
