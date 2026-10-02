# MyNet 安装指南 — Linux

> 产物：`mynet_agent_linux_<arch>_*.tgz`
> 包内：`mynet` `mynetd` + 本文件
> 适用：Debian/Ubuntu/CentOS 等（含云主机）
> 形态：**linux = 纯 CLI 安装，无托盘**；WebUI 已 embed 进 mynetd，
> 装完后浏览器访问 `http://127.0.0.1:8096/ui/`（mac/win 才是安装器+托盘形态）

---

## 1. 你需要准备

| 项 | 说明 |
|---|---|
| 平台账号 | 邮箱 + 密码 |
| 密钥对 | 有则导入，无则生成（**只备份私钥**） |
| root / sudo | 建 TUN、写路由、systemd |
| 云安全组 | 放行 gnb 的 **UDP listen 端口**（两层防火墙！） |

---

## 2. 安装步骤

```bash
tar xzf mynet_agent_linux_amd64_v*.tgz
cd mynet_agent_linux_amd64_v*

# 1) 二进制落位（mynet/mynetd 进 /opt/mynet/bin；WebUI 已 embed，无 webui.html）
sudo mkdir -p /opt/mynet/bin /opt/mynet/etc
sudo cp mynet mynetd /opt/mynet/bin/

# 2) gnb 四件套（同构建）只放 /usr/local/bin ——
#    标准位 $MYNET_HOME/driver/gnb/bin 由下一步 mynet install 自动置备，
#    不必手工拷两处。gnb 单独打包在 mynet_gnb 发行件里。
sudo cp gnb gnb_ctl gnb_es gnb_crypto /usr/local/bin/

# 3) 装服务（Linux → systemd）
export MYNET_HOME=/var/lib/mynet
sudo -E /opt/mynet/bin/mynet install \
  --gateways 'wss://gw.mynet.club/v1/node/connect' \
  --prefix /opt/mynet --home /var/lib/mynet
```

安装器写 systemd unit + `/opt/mynet/etc/mynetd.env` 并 `enable`，同时把 gnb
四件套置备到标准位 `/var/lib/mynet/driver/gnb/bin/`（同构建同目录，gnb_ctl
计数依赖这一点）。`--gnb-bin` 默认 `/usr/local/bin/gnb`，放在别处才需要指定。

---

## 3. 登录

```bash
export MYNET_HOME=/var/lib/mynet
/opt/mynet/bin/mynet auth login \
  --api https://api.mynet.club \
  --email '你的邮箱' \
  --password-stdin
```

---

## 4. 密钥

**恢复/迁移（手上有私钥）：**
```bash
/opt/mynet/bin/mynet config keys set \
  --private '<128-hex 私钥>' --public '<64-hex 公钥>' \
  --node-id <node_id> --algorithm ed25519
/opt/mynet/bin/mynet config keys sync
```

**新装：**
```bash
/opt/mynet/bin/mynet config keys generate --vpn gnb --node-id <node_id>
/opt/mynet/bin/mynet config keys sync
```

> `keys set/generate/sync` 会顺带装配 **back 节点面公钥**（back_pub_hex）：
> 它是 mynetd 节点面签名的前提，缺了 transport 会报「NodeID 必填」而不连网关。
> 老节点/迁移机补一次 `keys sync` 即可回填。

---

## 5. 配置与启动

```bash
/opt/mynet/bin/mynet config sync          # pull + 投影 + apply --auto（OS 路由）
```

gnb 起停归 systemd（`mynet_gnb.service`，install 时已 `enable --now`）；
`mynet service start gnb` 只在临时排障时用。

---

## 6. 防火墙（云主机必须两层）

```bash
# 机内（对照 node.conf 的 listen 端口）
sudo nft insert rule inet filter input udp dport 9016 accept
# 或 sudo iptables -A INPUT -p udp --dport 9016 -j ACCEPT

# 云侧（示例：Lightsail）
aws lightsail open-instance-public-ports \
  --instance-name <name> \
  --port-info fromPort=9016,toPort=9016,protocol=udp,cidrs=0.0.0.0/0
```

> 只开云侧不够：机内 `policy drop` 会丢包。只开机内也不够。

---

## 7. 验收

```bash
/opt/mynet/bin/mynet subject --json
/opt/mynet/bin/mynet route doctor
curl -s http://127.0.0.1:8096/api/v1/node-status | python3 -m json.tool
# 节点面已装配的硬判据（journalctl 里应能看到）：
sudo journalctl -u mynetd -n 50 | grep -E '节点面签名就绪|transport: connected'
```

---

## 8. 卸载

```bash
sudo -E /opt/mynet/bin/mynet uninstall
sudo rm -rf /opt/mynet /var/lib/mynet   # 含私钥，先确认已备份
```

---

## 9. 常见问题

| 症状 | 查 |
|---|---|
| 隧道不通 tcpdump 0 包 | 云安全组 UDP |
| Lightsail 开了仍不通 | 机内 nft INPUT |
| 大包丢 / SSH 超时 | `zip` 对等：`mynet config parity` |
| systemd 起了没心跳 | `mynetd.env` 里 `MYNET_GATEWAYS` |
| transport 报「NodeID 必填」 | back_pub_hex 未装配：`mynet config keys sync` 回填 |
| 路由走物理口 | `mynet route doctor`；`apply --auto` |

排错手册：`troubleshoot-connectivity.md`（A–E 五步法）
