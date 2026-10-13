# MyNet 安装指南 — OpenWrt

> 产物：`mynet-agent_*_*.ipk` + `luci-app-mynet_*_*.ipk`（SDK 交叉编译）
> 手工落位用 **`mynet_agent_openwrt_mipsel_*`**（`-tags openwrt` 构建的产物）
> 包内：`mynet` `mynetd`（瘦，无 embed WebUI）+ LuCI 壳
>
> ⚠️ **不要**用 `mynet_agent_linux_*`：通用 Linux 包不含 procd/UCI 后端，
> 在 OpenWrt 上会明确拒绝装配（目录/服务/防火墙都不同）。

---

## 1. 你需要准备

| 项 | 说明 |
|---|---|
| 平台账号 | 邮箱 + 密码 |
| 密钥对 | 有则导入，无则生成（**只备份私钥**） |
| 管理权限 | SSH + opkg/apk |
| 架构匹配 | `uname -m` → mipsel / aarch64 / x86_64 |

**UI**：OpenWrt 用 **LuCI**，不推 WebUI；WebUI 只监听 `127.0.0.1:8096`。
**形态**：openwrt = CLI/ipk 安装，无托盘、无自动开页（与 mac/win 安装器形态不同）。

---

## 2. 安装步骤

### 2a. ipk（推荐）

```sh
scp mynet-agent_*_*.ipk luci-app-mynet_*_*.ipk root@<router>:/tmp/
ssh root@<router>
opkg install /tmp/mynet-agent_*.ipk /tmp/luci-app-mynet_*.ipk
# 25.12+ 用 apk add
```

### 2b. 手工落位（无 ipk 时）

解压 `mynet_agent_openwrt_mipsel_*.tgz`（`-tags openwrt` 产物）后：

```sh
# 二进制
scp mynet mynetd root@<router>:/usr/sbin/
ssh root@<router> 'chmod 755 /usr/sbin/mynet /usr/sbin/mynetd'

# gnb 四件套（同构建，musl 版）
scp gnb gnb_ctl gnb_es gnb_crypto root@<router>:/etc/mynet/driver/gnb/bin/

# 装服务
export MYNET_HOME=/etc/mynet
/usr/sbin/mynet install --gateways 'wss://gw.mynet.club/v1/node/connect' --home /etc/mynet
```

OpenWrt 服务走 **procd**（`mynetd` + gnb 实例）；`mynet install` 会写 env、procd、防火墙 zone。

---

## 3. 登录

```sh
export MYNET_HOME=/etc/mynet
/usr/sbin/mynet auth login \
  --email '你的邮箱' --password-stdin
```

或在 LuCI → MyNet 界面登录。

---

## 4. 密钥

**恢复/迁移：**
```sh
/usr/sbin/mynet config keys set \
  --private '<128-hex 私钥>' --public '<64-hex 公钥>' \
  --node-id <node_id> --algorithm ed25519
/usr/sbin/mynet config keys sync
```

**新装：**
```sh
/usr/sbin/mynet config keys generate --vpn gnb --node-id <node_id>
/usr/sbin/mynet config keys sync
```

> `keys set/generate/sync` 会顺带装配 **后端节点面公钥**（back_pub_hex）：
> 缺了它 mynetd 节点面不装配，transport 报「NodeID 必填」而不连网关。
> 老节点补一次 `keys sync` 即可回填。

---

## 5. 配置与启动

```sh
# gnb 手工落位：四件套必须已在标准位 /etc/mynet/driver/gnb/bin/
# （OpenWrt 走 procd 独立路径，install 不代置备；ipk 则自带落位）
/usr/sbin/mynet config sync
/etc/init.d/mynet restart
# 或 LuCI 界面：状态 → 重启
```

---

## 6. 防火墙（OpenWrt = fw4 / nft）

```sh
# 安装时已委托 firewall.mynet install 建 zone
# 自定义放行（对照 node.conf listen）：
nft insert rule inet fw4 input udp dport 9016 accept

# 持久化 → /etc/nftables.d/
echo 'add rule inet fw4 input udp dport 9016 accept' > /etc/nftables.d/mynet.nft
fw4 reload
```

- **不要**装 `iptables`（legacy 与 fw4 冲突）；要装用 `iptables-nft`
- `firewall.user` 需 `fw4_compatible 1` 才会被执行
- 旁路由 NAT：VPN→LAN 方向性 SNAT；VPN↔VPN 不 NAT

---

## 7. 验收

```sh
/usr/sbin/mynet subject --json
/usr/sbin/mynet route doctor
# LuCI → MyNet → 状态
ping 10.182.236.180   # 对端 VIP
# 节点面已装配、网关已连上的硬判据：
logread | grep -E '节点面签名就绪|transport: connected' | tail
```

---

## 8. 卸载

```sh
opkg remove luci-app-mynet mynet-agent
# 或手工：
/etc/init.d/mynet stop
rm -rf /etc/mynet /usr/sbin/mynet /usr/sbin/mynetd
```

---

## 9. 常见问题

| 症状 | 查 |
|---|---|
| `fw4 reload` 挂死 | 不要同步 reload 阻塞 stop/HTTP |
| ipk 装完没界面 | `luci-app-mynet` 依赖 luci-base |
| 大包丢 | `zip` 对等；两端 `zip auto` |
| ping 通 SSH 不通 | `mynet config parity` |
| `sh` 跑 route_policy 报错 | 要 `bash`（`<<<` 是 bashism） |
| BusyBox 缺命令 | `xxd`/`timeout`/`python3` 可能没有 |
| 代理分流 | P2 `proxy.conf`（conf/\<id\>/proxy.conf），不是 route.conf 注入 |

排错：`troubleshoot-connectivity.md` · L5/L6 分工见 `route-firewall-split.md`
