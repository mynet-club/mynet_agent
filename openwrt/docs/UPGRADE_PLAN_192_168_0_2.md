# 192.168.0.2 升级计划 — 对比 · 风险 · 步骤 · Dry-run

> 日期：2026-09-29
> 目标：把生产 ImmortalWrt 网关 `192.168.0.2` 从 **旧 mynet_lua 2.1.6** 升到
> **新 mynet_agent（mynetd + mynet + procd）+ LuCI**，**proxy 分流不得中断**
> 约束：二进制取「GitHub Action 最新且带 proxy」；本轮先 **计划 + dry-run**，不动生产

---

## 1. 基线对比

| 项 | **192.168.0.2（现状）** | **目标态** | 差 |
|---|---|---|---|
| 系统 | ImmortalWrt 24.10.0 **x86_64** | 同系统 | 架构≠调试 VM(arm64) |
| 包 | `luci-app-mynet 2.1.6-1` | + `mynet-agent` | 新增 agent 包 |
| 二进制 | **无** mynet/mynetd | `/usr/sbin/{mynet,mynetd}` | 全新 |
| 服务 | `init.d/mynet` 旧壳（Lua 管 gnb） | procd 双驱动 + `mynetd` | 服务模型变 |
| 心跳 | cron `*/5 mynet-heartbeat` | mynetd 30s | **双心跳风险** |
| 驱动 | gnb `driver/gnb`，tun **gnb_tun_16** | 同（VPN_TYPE=gnb） | 保持 |
| **proxy** | **生产在跑**：`proxy_role.conf` PROXY_ENABLED=1，`table inet mynet_proxy`，fwmark 0xc8，**3007** 条 nft | 新代理链（`mynet/proxy_*.go` + Lua proxy） | **最高风险** |
| DNS | dnsmasq + smartdns 分流 | 同（脚本保留） | 勿动 |
| 节点 | 8283289760467957 | 同 | 密钥不换 |

### 调试 VM（已验证）≠ 生产

| | VM | 192.168.0.2 |
|---|---|---|
| 架构 | aarch64 QEMU | **x86_64** |
| 系统 | OpenWrt 24.10.1 | **ImmortalWrt** 24.10.0 |
| 角色 | 试验 | **生产网关 + proxy client** |
| 状态 | 空配置可折腾 | **live 流量** |

---

## 2. 升级风险（按严重度）

| ID | 风险 | 级别 | 缓解 |
|---|---|---|---|
| **R1** | **proxy 分流中断**（LAN 全走默认路由/丢国外） | **P0** | 先完整备份 nft + proxy 配置；升级窗口可回滚；升级后立即验证 mark/table |
| **R2** | 双心跳（cron + mynetd）打爆 back / 状态抖动 | P1 | 升级时摘 cron `mynet-heartbeat`，只留 mynetd |
| **R3** | 旧 Lua `start_gnb` 与 procd 抢 gnb / 端口 | P1 | 启用 procd 前精确停旧实例；不用 pkill |
| **R4** | 二进制架构错（arm64 装到 x86_64） | P1 | 产物名 `linux_amd64`；`file`/`--version` 校验 |
| **R5** | ImmortalWrt uci/fw4 与 OpenWrt 细节差异 | P2 | `firewall.mynet install` + 回读 uci；对照现有 zone |
| **R6** | GitHub Actions **无产物**（见 §3） | P1 | 先补 CI 或用等价 `make dist` 并锁定 commit |
| **R7** | LuCI 覆盖后 401/菜单 ACL 变化 | P2 | 保留旧 luci 备份；U1 已人话化 |
| **R8** | 密钥/节点身份被覆盖 | P0 | **绝不覆盖** `/etc/mynet/conf/{credential,config,proxy}` 与 keys；只升级二进制/init/LuCI |

---

## 3. 二进制来源（GitHub Actions）

**现状**：`mynet-club/mynet_agent-V2` 的 Actions **0 run / 0 artifact**，`gh release` 也是空的。
「带 proxy」= 默认 `make dist` 已编入 `mynet/proxy_*.go`（无独立 tag）。

| 选项 | 做法 | 备注 |
|---|---|---|
| **A（推荐）** | 先在 agent 仓加 release workflow → 用 **Actions 产物** `linux_amd64` | 满足「从 GitHub Action 取」 |
| B | 本地 `make dist` 出 `mynet_agent_linux_amd64_*`，与 CI 同命令 | 等价，但不是 CI 产物 |

**Dry-run 阶段用 B 出包做演练；正式升级前必须有 A 的 CI 产物**（否则不满足你的来源要求）。

产物清单（目标包 `mynet_agent_linux_amd64_*.tgz`）：

```text
mynet       # 执行器（含 proxy_apply 等）
mynetd      # 通信层（OpenWrt 用无 embed；dist 现在打 embed —— 升级时用 EMBED=0 重编）
mynet_gateway
```

⚠️ dist 默认 `mynetd -tags embed`；OpenWrt 要 **瘦构建**。升级包应单独出 `EMBED=0` 的 mynetd。

---

## 4. 升级步骤（生产，未执行）

### Phase 0 — 备份（强制，未备份不开跑）

```text
192.168.0.2:
  /etc/mynet/                     tar czf mynet-$(date +%F).tgz
  /etc/config/{network,firewall,dhcp,firewall}
  nft list ruleset                > backup-nft-$(date +%F).txt
  ip rule; ip route show table mynet_proxy
  cat /etc/crontabs/root
  /etc/init.d/mynet               cp -a
  /usr/lib/lua/luci/…             cp -a mynet 模块
  gnb bin + conf（含 keys）
  opkg list-installed             > backup-pkgs.txt
```

### Phase 1 — 产物就绪

```text
1) CI 出包（或本地等价）：
   GOOS=linux GOARCH=amd64
   mynet（默认，含 proxy）+ mynetd（-tags 无 embed）
2) 校验：file 为 x86-64，./mynet --help，proxy 子命令存在
3) scp 到 /tmp/upgrade/，先不覆盖
```

### Phase 2 — Dry-run（本轮要做）

```text
mynet install --dry-run --gateways wss://gw.mynet.club/v1/node/connect
  → 只打印动作，不写盘
对比输出与 §5 检查表
```

### Phase 3 — 维护窗（短断可接受时）

```text
1) 再确认 Phase 0 备份完整
2) 停旧服务：/etc/init.d/mynet stop（Lua 清 gnb）
3) 摘 cron：去掉 */5 mynet-heartbeat
4) 装文件（安装包/脚本）：
     /usr/sbin/mynet, mynetd          （新二进制）
     /etc/init.d/{mynet,mynetd}       （procd 双驱动）
     LuCI 壳（controller/model/view）
5) 服务安装：
     mynet install --gateways wss://… --home /etc/mynet
     → 校验二进制 + network/zone + env + enable
6) 起 mynetd；再 /etc/init.d/mynet start
7) 验证 §5，不达标立即回滚 §6
```

### Phase 4 — 验证后收尾

```text
- proxy 分流抽查（国内外域名 + 策略表）
- 心跳只走 mynetd
- LuCI Dashboard 状态与 OS 一致
- 保留备份 ≥ 7 天
```

---

## 5. 验证清单（升级后）

| # | 项 | 通过标准 |
|---|---|---|
| V1 | `mynetd` 进程 | running，env 含 `MYNET_EXECUTOR=lua` |
| V2 | gnb | procd 实例或旧路径停止后正确拉起；tun **gnb_tun_16** UP |
| V3 | 路由 | `ip route show table mynet_proxy` default via 对端仍在 |
| V4 | **proxy** | `nft list table inet mynet_proxy` 有 set；`ip rule` 31800 fwmark 0xc8 |
| V5 | 分流抽查 | 国外域名走隧道、国内直连（对照 proxy_role） |
| V6 | 心跳 | 仅 mynetd；back 上节点 online |
| V7 | LuCI | Dashboard 无裸 API 错误；启停反映 OS |
| V8 | DNS | dnsmasq→smartdns 链未破坏 |

---

## 6. 回滚

```text
1) systemctl/init 停 mynetd、mynet
2) 恢复 /usr/sbin/*、/etc/init.d/*、LuCI 模块（备份）
3) 恢复 /etc/config/* 与 nft ruleset（backup-nft.txt）
4) 恢复 crontab mynet-heartbeat
5) /etc/init.d/mynet start（旧 Lua 路径）
6) 重验 V2–V5
```

---

## 7. Dry-run 结果（2026-09-29）

| 项 | 结果 |
|---|---|
| 192.168.0.2 SSH / 基线 | ✅ ImmortalWrt 24.10.0 **x86_64**；lua 2.1.6；**proxy live**（nft 3007 条）；无 mynet/mynetd |
| GH Actions 产物 | ❌ **不存在**（0 run / 0 release）→ 正式升级前须补 CI 或书面确认改用 make dist |
| linux_amd64 产物（本地等价） | ✅ `/tmp/mynet-upgrade-amd64/{mynet,mynetd}` 静态 x86-64；**proxy 符号在**（ApplyProxyDNSFiles / ApplyProxyToGnb / EffectiveProxyPolicy…） |
| `mynet install --dry-run`（VM 上旧二进制） | ✅ 退出 0；打印 init+env+enable（**旧文案仍含「装二进制」**——VM 未更新到「服务安装」版） |
| 新版 dry-run（服务安装文案） | ☐ 待把新 mynet 装进 VM 后复跑 |
| 回滚演练 | ☐ 未做 |

### Dry-run 输出摘录（VM，旧二进制）

```text
将执行以下动作（OpenWrt/procd，--dry-run 只看不做）：
  写 /etc/init.d/{mynetd,mynet}
  写 /etc/mynet/mynetd.env  (MYNET_EXECUTOR=lua)
  建 /etc/mynet/{conf,var}
  装二进制到 /opt/mynet/sbin/{mynet,mynetd}    ← 新版已删（文件归 ipk）
  /etc/init.d/mynetd enable && start
  /etc/init.d/mynet  enable
--dry-run：以上都没有执行
EXIT:0
```

### 升级包清单（本轮已产出，待 CI 化）

```text
/tmp/mynet-upgrade-amd64/mynet    ELF x86-64 static  含 proxy
/tmp/mynet-upgrade-amd64/mynetd   ELF x86-64 static  无 embed（瘦）
```

---

## 8. 建议的 Go / No-Go

| 条件 | 要求 |
|---|---|
| Go | 备份齐全 + **CI 产物**（或书面改 make dist）+ amd64 瘦 mynetd + mynet(含 proxy) + 新版 dry-run 通过 + 回滚演练 |
| No-Go | 任一：无备份 / 产物架构不对 / proxy 验证不过 / 仍双心跳 / CI 产物缺失且未批准替代来源 |

**建议顺序**：补 CI 出包 → 新版 dry-run → 备份生产 → 维护窗切换 → V1–V8。
