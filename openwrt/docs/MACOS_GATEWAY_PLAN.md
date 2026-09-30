# macOS 通过 OpenWrt VM 网关分流方案

> ⚠️ **已迁移**：调试环境规范已整合到 [spec/debug-env.md](../../mynet_ctl/docs/knowledge-base/spec/debug-env.md)。本文档为历史规划记录，不再维护。

> 状态：**规划中**，暂未实施。当前仍使用 vmnet-shared(WAN) + vmnet-host(LAN) 开发架构。

## 目标

让 macOS 的全部流量默认走 OpenWrt VM，利用 VM 内的 ipset + iptables 实现国内/海外分流（国内直连、海外走 GNB VPN 隧道）。

## 当前架构（开发用）

```
macOS WiFi (en0) ──→ 路由器 192.168.0.1 ──→ Internet

macOS bridge100 (vmnet-shared, 192.168.2.1)
  └── VM eth0: 192.168.2.x (DHCP) ← VM 通过 macOS NAT 上网

macOS bridge101 (vmnet-host, 192.168.101.1)
  └── VM eth1: 192.168.101.2 (静态) ← SSH/HTTP 管理口
```

macOS 自身流量不经过 VM，VM 仅用于开发调试。

## 网关方案架构

### 为什么不能直接在当前架构上改默认网关

vmnet-shared 的 NAT 走 macOS 自己的路由表。如果 macOS 默认路由指向 VM，NAT 出去的包又被路由回 VM，产生**路由环路**：

```
macOS → VM (192.168.101.2) → VM NAT via vmnet-shared → macOS 路由表 → VM → ∞
```

### 解决方案：WAN 改 vmnet-bridged

将 VM 的 WAN 网卡从 vmnet-shared 改为 vmnet-bridged，桥接到 WiFi (en0)，让 VM 直达物理路由器 192.168.0.1，绕过 macOS 路由表。

```
                    ┌──── Internet
                    │
              ┌─────┴─────┐
              │  路由器     │ 192.168.0.1 (PPPoE)
              └─────┬─────┘
                    │ 192.168.0.0/24
        ┌───────────┼───────────┐
        │           │           │
   macOS en0    VM eth0      其他设备
   .0.218      .0.200(静态)
        │
   bridge101 (vmnet-host)  192.168.101.0/24
   macOS: .101.1
        │
   VM eth1: .101.2   ← macOS 默认网关指向这里
        │
   ┌────┴─────────────────────────┐
   │ OpenWrt VM                    │
   │  ipset 分流:                  │
   │   国内 IP  → eth0 → 路由器    │
   │   海外 IP  → GNB VPN 隧道    │
   │  iptables MASQUERADE on eth0  │
   └──────────────────────────────┘
```

### 数据流（无环路）

1. macOS 应用 → 目标 IP
2. macOS 默认路由 → 192.168.101.2 via bridge101 (vmnet-host)
3. VM eth1 收包 → ipset 判断：
   - 国内：→ eth0 (vmnet-bridged) → 192.168.0.1 (真实路由器) → Internet
   - 海外：→ GNB 隧道 → 出口节点
4. 回程：路由器 → VM eth0 → VM eth1 → macOS bridge101

关键：VM 通过 vmnet-bridged 直达物理网络 (192.168.0.0/24)，不经过 macOS 路由表，无环路。

## 所需改动清单

### 1. QEMU 启动参数（start.sh）

```diff
- -netdev vmnet-shared,id=wan
+ -netdev vmnet-bridged,id=wan,ifname=en0
```

### 2. VM 网络配置（setup.sh / fix-network.sh）

WAN 从 DHCP 改为静态（避免与 macOS 抢 DHCP 地址）：

```
config interface 'wan'
    option device 'eth0'
    option proto 'static'
    option ipaddr '192.168.0.200'
    option netmask '255.255.255.0'
    option gateway '192.168.0.1'
    option dns '8.8.8.8 8.8.4.4'
```

LAN (vmnet-host 管理口) 不变：192.168.101.2

### 3. macOS 路由切换脚本（新建 gateway-mode.sh）

开启网关模式：
```bash
# 保持局域网直通
sudo route add -net 192.168.0.0/24 -interface en0
# 切默认网关到 VM
sudo route change default 192.168.101.2
```

恢复直连模式：
```bash
sudo route change default 192.168.0.1
sudo route delete -net 192.168.0.0/24
```

### 4. VM 内 iptables/ipset 分流

- 安装 `ipset`、`iptables` 相关包
- 导入国内 IP 段到 ipset (`cn` 集合)
- iptables 规则：匹配 cn 集合直连，其余走 GNB 隧道
- eth0 做 MASQUERADE（让回程包能找到路）

## 优劣分析

### 优点

- macOS 全部流量透明走 VM，应用无感知，无需配置系统代理
- ipset 分流在 VM 的 iptables 中实现，成熟方案
- vmnet-host 管理口始终可用（即使分流出问题，SSH 192.168.101.2 不受影响）
- 与二级路由器 192.168.0.2 (ImmortalWrt) 的旁路网关思路一致

### 缺点/注意

- **WiFi 桥接稳定性**：macOS vmnet-bridged + WiFi 大部分正常，但 WiFi 漫游/重连时可能短暂丢桥
- **绑定物理网络**：VM eth0 桥接 en0 (WiFi)，换网络环境（如咖啡厅）后网段变化，VM 静态 IP/路由失效，需改配置或改用 DHCP
- **macOS 路由保护**：切默认网关前必须先加 `route add -net 192.168.0.0/24 -interface en0`，否则局域网也被转发到 VM
- **MTU/性能**：虚拟网卡 + 桥接 + VM 路由，相比直连多一跳，延迟 +~1ms，吞吐降 10-20%
- **必须 sudo**：vmnet-bridged 和路由修改都需要 root 权限

## 依赖

- macOS vmnet-bridged 支持（QEMU 8.0+）
- OpenWrt ipset + iptables-nft 或 nftables
- 国内 IP 段列表（如 chnroutes / maxmind GeoIP）
- GNB VPN 隧道已建立（mynet 服务 running）
