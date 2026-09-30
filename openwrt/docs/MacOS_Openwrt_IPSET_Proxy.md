# macOS + QEMU + OpenWrt 透明分流网关方案

> ⚠️ **已迁移**：调试环境规范已整合到 [spec/debug-env.md](../../mynet_ctl/docs/knowledge-base/spec/debug-env.md)。本文档为历史技术设计记录，不再维护。

> 技术设计文档 · 2025  
> 涵盖：vmnet.framework 工作原理 · 网络拓扑 · 路由配置 · 避坑指南

---

## 0. 实施计划

> **目标**：将 macOS 上运行的 GNB 节点（`3764021915150630` / VIP `10.182.236.181`）整体迁移到 QEMU VM，  
> VM 接管 GNB + 透明分流，macOS 自身流量默认走 VM 网关。
>
> 当前 debug 环境：WAN=vmnet-shared（NAT）+ LAN=vmnet-host（管理口 192.168.101.2）  
> 目标架构：WAN=vmnet-bridged（直连 192.168.0.0/24）+ LAN 不变

**节点信息（已知）**

| 角色 | 节点 ID | VIP | 说明 |
|------|---------|-----|------|
| 本机（迁移目标） | `3764021915150630` | `10.182.236.181` | macOS → VM |
| proxy server（EC2）| `4997342800601732` | `10.182.236.180` | 海外出口，route.conf 中有 proxy 段 |
| LAN peer（ImmortalWrt）| `8283289760467957` | `10.182.236.182` | 192.168.0.2 |

---

### 阶段一：准备（macOS 侧，无需重启 VM）

**1.1 安装 socket_vmnet（解决 vmnet-bridged 权限问题）**

```bash
brew install socket_vmnet
sudo /opt/homebrew/opt/socket_vmnet/bin/socket_vmnet_client setup
```

> 目的：让 QEMU 不必以 root 身份运行，socket_vmnet 作为特权 helper 代为操作 vmnet

**1.2 在路由器（192.168.0.1）固定 VM 的 MAC 绑定**

VM WAN 网卡 MAC 将固定为 `52:54:00:12:34:56`（start.sh 中写死），在路由器后台将此 MAC 绑定静态 IP `192.168.0.200`，防止 DHCP 竞争。

**1.3 停止 macOS 上的 mynet 服务**

迁移后 macOS 不再运行 GNB，先停止：

```bash
/usr/local/opt/mynet/bin/mynet stop
# 或
launchctl unload ~/Library/LaunchAgents/com.mynet.service.plist 2>/dev/null
```

**1.4 准备国内 IP 段列表**

```bash
curl -o /tmp/chnroutes.txt \
  https://raw.githubusercontent.com/misakaio/chnroutes2/master/chnroutes.txt
```

---

### 阶段二：修改 debug/start.sh（切换网络模式）

```diff
- # WAN: vmnet-shared（NAT 上网）
- -device virtio-net-pci,netdev=wan
- -netdev vmnet-shared,id=wan

+ # WAN: vmnet-bridged（桥接 en0 WiFi，VM 直接接入 192.168.0.0/24）
+ -device virtio-net-pci,netdev=wan,mac=52:54:00:12:34:56
+ -netdev vmnet-bridged,id=wan,ifname=en0
```

LAN（vmnet-host 管理口 192.168.101.2）保持不变，始终可用。

---

### 阶段三：修改 OpenWrt 网络配置（fix-network.sh）

WAN 保持 DHCP（vmnet-bridged 直接从家庭路由器获取 IP 和 DNS，无需手动指定）。  
去掉原来为 vmnet-shared 特有的 `peerdns=0` 和硬编码 DNS 覆盖：

```diff
- # vmnet-shared 网关不转发 DNS，必须手动指定公共 DNS
- uci set network.wan.peerdns="0"
- uci add_list network.wan.dns="8.8.8.8"
- uci add_list network.wan.dns="8.8.4.4"

+ # vmnet-bridged 直连路由器，peerdns 正常工作，无需覆盖
+ uci delete network.wan.peerdns
+ uci delete network.wan.dns
```

VM 启动后会自动从路由器（192.168.0.1）拿到 IP，每次 IP 可能不同。  
**建议**：在路由器后台将 VM 的 MAC `52:54:00:12:34:56` 绑定固定 IP（如 192.168.0.200），便于 SSH 和防火墙规则引用。

---

### 阶段四：将节点配置从 macOS 迁移到 VM

SSH 进 VM，将 macOS 上的 GNB 配置复制进去：

```bash
# macOS 上执行：将 GNB conf 目录打包传入 VM
NODE_ID=3764021915150630
SRC="/usr/local/opt/mynet/driver/gnb/conf/${NODE_ID}"
DEST_DIR="/etc/mynet/driver/gnb/conf/${NODE_ID}"

ssh openwrt-qemu "mkdir -p ${DEST_DIR}/ed25519 ${DEST_DIR}/security"
for f in node.conf route.conf address.conf; do
    scp "${SRC}/${f}" "openwrt-qemu:${DEST_DIR}/${f}"
done
# 传 ed25519 公钥
scp "${SRC}/ed25519/"*.public "openwrt-qemu:${DEST_DIR}/ed25519/"
# 传私钥（注意保护）
scp "${SRC}/security/ed25519_private" "openwrt-qemu:${DEST_DIR}/security/"
```

然后在 VM 内更新 `/etc/mynet/conf/mynet.conf`，将 `NODE_ID=3764021915150630`、`VPN_IP=10.182.236.181` 填好，或直接通过 LuCI mynet 界面注册/激活节点。

---

### 阶段五：OpenWrt 内安装分流规则

SSH 进 VM，执行（mynet proxy 功能已有集成，也可手动配置）：

```bash
# 安装依赖
opkg update && opkg install ipset kmod-ipt-ipset iptables-mod-ipset

# 导入国内 IP 段
ipset create cn hash:net
while read cidr; do
    [[ "$cidr" =~ ^# ]] && continue
    ipset add cn "$cidr" 2>/dev/null || true
done < /tmp/chnroutes.txt

# mangle 规则：国内 IP mark=1（直连），海外 mark=2（走 GNB proxy 隧道）
iptables -t mangle -F PREROUTING
iptables -t mangle -A PREROUTING -m set --match-set cn dst -j MARK --set-mark 1
iptables -t mangle -A PREROUTING -j MARK --set-mark 2

# 策略路由：mark=2 走 GNB proxy peer（EC2 10.182.236.180）
ip rule add fwmark 2 table 200 priority 100
ip route add default via 10.182.236.180 dev gnb_tun table 200

# eth0（WAN）MASQUERADE，保证回程包能找到路
iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
```

---

### 阶段六：切换 macOS 默认网关

VM 启动稳定、GNB 已连接后，执行（脚本化为 `debug/gateway-mode.sh`）：

```bash
# 开启网关模式
sudo route add -net 192.168.0.0/24 -interface en0  # 局域网直连
sudo route change default 192.168.101.2             # 其余走 VM

# 验证：第一跳应为 192.168.101.2，第二跳应为 192.168.0.1
traceroute -m4 8.8.8.8

# 验证海外走代理
curl -4 https://api.ipify.org   # 应返回 EC2 IP（54.251.x.x）

# 恢复直连模式（回退用）
# sudo route change default 192.168.0.1
# sudo route delete -net 192.168.0.0/24
```

---

### 阶段七：持久化

**macOS 路由**（launchd，在 QEMU 启动后执行）：

```xml
<!-- ~/Library/LaunchAgents/com.user.openwrt-gateway.plist -->
<key>ProgramArguments</key>
<array>
  <string>/bin/sh</string>
  <string>/usr/local/bin/openwrt-gateway.sh</string>
</array>
<key>RunAtLoad</key><true/>
```

**OpenWrt ipset 规则持久化**：将阶段五命令写入 `/etc/rc.local` 或 nftables 永久配置文件。

---

### 风险与回退

| 风险 | 触发条件 | 回退方法 |
|------|---------|---------|
| macOS 断网 | VM 未启动但默认路由已改 | `sudo route change default 192.168.0.1` |
| VM 拿不到 192.168.0.200 | MAC 未绑定 / WiFi 漫游重连 | 路由器确认 MAC 绑定；或改 DHCP |
| GNB 打洞失败 | 两端节点都在 NAT 后 | 检查 address.conf 中 index server 可达性 |
| 分流失效 | ipset 未加载 | SSH 192.168.101.2（管理口始终直连）重新应用规则 |
| socket_vmnet 权限报错 | entitlement 未生效 | 临时 `sudo bash debug/start.sh` |

---

### 改动文件汇总

| 文件 | 改动 |
|------|------|
| `debug/start.sh` | WAN 改 vmnet-bridged + 固定 MAC `52:54:00:12:34:56` |
| `debug/fix-network.sh` | WAN 改静态 IP `192.168.0.200`，网关 `192.168.0.1` |
| `debug/gateway-mode.sh` | 新建：macOS 路由切换脚本（开启/恢复） |
| `~/Library/LaunchAgents/com.user.openwrt-gateway.plist` | 新建：开机自动切路由 |

---

## 1. 背景与目标

在 macOS（Wi-Fi 接入）环境下，通过 QEMU 运行 OpenWrt 虚拟机，实现国内流量直连、海外流量走 GNB VPN 隧道的透明分流网关方案。

核心需求：

- macOS 本机流量也走分流，不仅仅是下游设备
- OpenWrt 作为唯一的策略路由节点，便于集中管理
- 利用 Apple 原生 vmnet.framework，无需额外驱动
- Wi-Fi 环境可用（不依赖有线网卡桥接）

---

## 2. 关键认知：Apple vmnet.framework vs Linux bridge

> 这是本方案能够成立的核心原因，也是与 Linux 网络虚拟化最大的认知差异点。

### 2.1 Linux bridge 的工作方式

在 Linux 下，虚拟机通过 TAP 设备 + bridge 接入物理网络：

```
VM eth0 (MAC: AA:BB:CC...)  ──→  tap0  ──→  br0  ──→  eth0 (物理网卡)

  • VM 的 MAC 地址原样出现在物理网络上
  • 路由器 ARP 表里可以直接看到 VM 的 MAC
  • 这是真正的二层透传
```

但 Wi-Fi（802.11）不支持这种模式：无线网卡工作在托管模式（managed mode），协议层要求每个 MAC 地址必须与 AP 单独关联认证。因此，将非本机 MAC 的帧直接透传给 Wi-Fi AP 会被拒绝或丢弃。

### 2.2 Apple vmnet.framework 的工作方式

Apple 的 vmnet.framework（macOS 原生虚拟网络框架，被 QEMU、Parallels、UTM 等使用）采用了完全不同的架构：

```
VM eth0 (MAC: 52:54:00...)  ──→  vmnet.framework
                                        │
                            ┌───────────┴───────────────┐
                            │   框架内部处理：            │
                            │   • ARP 代理 (Proxy ARP)   │
                            │   • MAC 地址重写            │
                            │   • DHCP 代理转发           │
                            └───────────┬───────────────┘
                                        │
                           en0 Wi-Fi (MAC: macOS 自己的 MAC)
                                        │
                                      路由器
```

关键行为：

- **路由器只看到 macOS 自己的 MAC 地址**，Wi-Fi 协议层完全合规
- vmnet 框架在内部代理 VM 的 ARP 请求/响应
- DHCP 请求被框架拦截并以 macOS 身份转发给路由器
- 路由器分配的 IP（如 192.168.0.200）被透传给 VM
- VM 从自己视角看，就像直接接在路由器上一样

### 2.3 两者的核心差异对比

| 对比项 | Linux bridge + TAP | Apple vmnet.framework |
|--------|--------------------|-----------------------|
| VM MAC 可见性 | 路由器直接看到 VM MAC | 路由器只看到 macOS MAC |
| Wi-Fi 兼容性 | ❌ 无法用于 Wi-Fi | ✅ 原生支持 Wi-Fi |
| IP 地址分配 | 路由器 DHCP 直接分配给 VM | 框架代理后透传给 VM |
| 二层帧处理 | 原样透传 | MAC 重写 + ARP 代理 |
| 实现层次 | 内核态 bridge | 用户态框架（含内核辅助）|
| VM 视角 | 直连到物理网络 | 也像直连，实为代理 |

> **结论：** vmnet-bridged 模式下，VM 可以在 macOS Wi-Fi 环境中获得真实的 192.168.0.x IP 地址，路由器能正常路由到它。这不是 Linux 意义上的"桥接"，但效果等价，且绕过了 Wi-Fi 的二层限制。

### 2.4 补充：路由器为什么能分配两个 IP？

一个常见疑问：Wi-Fi 网卡只有一个，路由器会不会只分配一个 IP？

答案是：**路由器分配 IP 的依据是 MAC 地址，而不是物理接口数量。**

vmnet 给 VM 分配了一个独立的虚拟 MAC（如 `52:54:00:AA:BB:01`），与 macOS en0 的真实 MAC 不同。路由器在 DHCP/ARP 层面看到的是**两个不同的 MAC 地址**，因此分配两个独立的 IP：

```
路由器 ARP/DHCP 表：

MAC: AA:BB:CC (macOS en0 真实 MAC)   →  192.168.0.218
MAC: 52:54:00 (VM 虚拟 MAC，经vmnet代理) →  192.168.0.200
```

这正是 vmnet 框架"神奇"的地方——它在 Wi-Fi 单一物理信道上，模拟出了多个独立的网络身份。实践中建议在路由器后台将 VM 的虚拟 MAC 绑定静态 IP，确保每次启动地址不变。

---

## 3. 网络拓扑

### 3.1 总体架构

```
                    ┌──── Internet
                    │
              ┌─────┴─────┐
              │  路由器    │  192.168.0.1 (PPPoE)
              └─────┬─────┘
                    │ 192.168.0.0/24
        ┌───────────┼───────────┐
        │           │           │
   macOS en0    VM eth0      其他设备
   .0.218      .0.200(静态)
        │
   vmnet-host  192.168.101.0/24   ← macOS 端: .101.1
        │
   VM eth1: .101.2   ← macOS 默认网关指向这里
        │
   ┌────┴──────────────────────────┐
   │ OpenWrt VM                     │
   │  eth0: WAN (.0.200) → 直连出口 │
   │  eth1: LAN (.101.2) → macOS上游│
   │                               │
   │  ipset 分流:                   │
   │   国内 IP → eth0 → .0.1 直连   │
   │   海外 IP → GNB VPN 隧道       │
   │  iptables MASQUERADE on eth0   │
   └───────────────────────────────┘
```

### 3.2 流量路径说明

| 场景 | 流量路径 |
|------|---------|
| macOS 访问国内 IP | macOS → .101.2(OpenWrt LAN) → ipset匹配国内 → eth0(.0.200) → 路由器 → 直连 |
| macOS 访问海外 IP | macOS → .101.2(OpenWrt LAN) → ipset匹配海外 → GNB VPN 隧道 → 出去 |
| OpenWrt 自身出网 | eth0(.0.200) → 路由器(.0.1) → Internet |
| 其他局域网设备 | 设备 → 路由器 → OpenWrt（需在路由器配置静态路由） |

---

## 4. 实现步骤

### 4.1 QEMU 网卡配置

OpenWrt VM 需要两块网卡：

- **eth0（WAN）**：vmnet-bridged 模式，接入 192.168.0.0/24，获取静态 IP .0.200
- **eth1（LAN）**：vmnet-host 模式，与 macOS 组成 192.168.101.0/24 内部网段

```bash
qemu-system-x86_64 \
  # WAN 口：桥接到 Wi-Fi，获取 .0.200
  -netdev vmnet-bridged,id=wan,ifname=en0 \
  -device virtio-net-pci,netdev=wan,mac=52:54:00:AA:BB:01 \
  # LAN 口：host-only，与 macOS 内部通信
  -netdev vmnet-host,id=lan,start-address=192.168.101.1,\
          end-address=192.168.101.254,subnet-mask=255.255.255.0 \
  -device virtio-net-pci,netdev=lan,mac=52:54:00:AA:BB:02 \
  ...
```

> **注意：** vmnet 相关操作需要 `com.apple.vm.networking` entitlement 或 root 权限。推荐使用 socket_vmnet（`brew install socket_vmnet`）作为 helper，避免整个 QEMU 进程跑在 root 下。

### 4.2 OpenWrt 网络配置

```bash
# /etc/config/network

config interface 'wan'
    option ifname   'eth0'
    option proto    'static'
    option ipaddr   '192.168.0.200'
    option netmask  '255.255.255.0'
    option gateway  '192.168.0.1'
    option dns      '192.168.0.1'

config interface 'lan'
    option ifname   'eth1'
    option proto    'static'
    option ipaddr   '192.168.101.2'
    option netmask  '255.255.255.0'
    # 无需网关，macOS 在 .101.1
```

### 4.3 macOS 路由配置

```bash
# 1. 删除 en0 自动添加的默认路由（Wi-Fi 连接时自动生成）
sudo route delete default

# 2. 新默认路由走 OpenWrt LAN 口
sudo route add default 192.168.101.2

# 3. 保留对本地网段的直连（访问路由器管理页等）
sudo route add 192.168.0.0/24 -interface en0

# 4. vmnet-host 网段直连（OpenWrt LAN 通信）
sudo route add 192.168.101.0/24 -interface en0
```

### 4.4 DNS 配置

```bash
# 将 macOS DNS 指向 OpenWrt，防止泄漏
sudo networksetup -setdnsservers Wi-Fi 192.168.101.2

# 验证
scutil --dns | grep nameserver
```

### 4.5 OpenWrt ipset 分流配置

```bash
# 安装依赖
opkg install ipset kmod-ipt-ipset iptables-mod-ipset

# /etc/dnsmasq.d/china.conf → 国内域名打 tag
ipset=/baidu.com/qq.com/taobao.com/china_domains

# iptables 分流规则
# 国内 IP 直接走 WAN（eth0）
iptables -t mangle -A PREROUTING -m set --match-set china_domains dst \
         -j MARK --set-mark 1

# 海外流量走 VPN（GNB 隧道接口，如 gnb0）
ip rule add fwmark 0 table 200   # 无 mark → 走 VPN
ip rule add fwmark 1 table main  # mark=1  → 直连

# eth0 方向 MASQUERADE
iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
```

---

## 5. 开机持久化

macOS 重启后路由表会重置，通过 launchd 在登录后自动执行：

```xml
<!-- ~/Library/LaunchAgents/com.user.openwrt-routes.plist -->

<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.user.openwrt-routes</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>/usr/local/bin/setup-openwrt-routes.sh</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.user.openwrt-routes.plist
```

---

## 6. 常见问题

| 问题 | 原因 | 解决方案 |
|------|------|---------|
| VM eth0 拿不到 .0.200 | DHCP 竞争或 MAC 未绑定 | 在路由器后台按 MAC 绑定静态 IP |
| macOS 断网 | 默认路由改了但 OpenWrt 未启动 | 先启动 VM 再改路由，或 launchd 加延迟 |
| DNS 仍然走运营商 | macOS DNS 未修改 | 用 networksetup 命令指向 OpenWrt |
| QEMU 启动报权限错误 | vmnet 需要 entitlement | 安装并使用 socket_vmnet helper |
| 国内流量走了 VPN | ipset 域名表未包含 | 更新 china_domains 列表（china-list 项目）|

---

## 7. 总结

本方案的可行性建立在对 Apple vmnet.framework 正确理解的基础上。它不是 Linux 的裸 bridge，而是一套完整的代理层，使 Wi-Fi 环境下的 VM 也能透明地接入物理网段。

**方案优点：**

- macOS 本机流量走 OpenWrt 分流，与下游设备行为一致
- 利用 Apple 原生框架，无需第三方内核模块
- OpenWrt 集中管理所有分流策略，灵活可维护
- Wi-Fi 环境完全支持，不依赖有线网卡

**需要注意：**

- QEMU 的 vmnet 权限问题需要额外处理（socket_vmnet）
- macOS 路由表需要在每次重启后重新配置（launchd 自动化）
- ipset 域名列表需要定期更新以保持分流准确性