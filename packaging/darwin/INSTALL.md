# MyNet 安装指南 — macOS

> **安装形态**：mac = 安装器（pkg/dmg）+ 托盘 + 装完自动开 WebUI。
>
> **图形化安装（推荐）**：`make pkg-darwin` 产出 `dist/MyNet_Agent_<ver>.dmg` →
> 双击挂载 → 双击 `MyNet_Agent_<ver>.pkg` → 系统安装向导点「继续 → 安装」。
> pkg 负责文件落地（mynet/mynetd/mynet_tray → /opt/mynet/bin，gnb 四件套 →
> /usr/local/bin 与 /opt/mynet/driver/gnb/bin），postinstall 自动调
> `mynet install` 装配服务（升级时沿用已配置的 MYNET_GATEWAYS）：
> 装完即有菜单栏托盘（LaunchAgent），并在 mynetd 就绪后自动打开
> `http://127.0.0.1:8096/ui/` 引导配置。
> 未签名构建仅适合本机/内网验证；对外分发需 codesign + notarize。
>
> 产物：`mynet_agent_darwin_<arch>_*.tgz`（手工 CLI 安装用）
> 包内：`mynet` `mynetd` `mynet_tray` + 本文件（WebUI 已 embed 进 mynetd）

---

## 1. 你需要准备

| 项 | 说明 |
|---|---|
| 平台账号 | 注册/登录 mynet（邮箱 + 密码） |
| 密钥对 | **有** → 后面「导入私钥」；**没有** → 工具生成 |
| 管理员权限 | `sudo`（装 LaunchDaemon、建 TUN） |

**备份原则**：只需备份**私钥**（gnb 128hex / wg base64）。公钥可由私钥推导。

---

## 2. 安装步骤

```bash
# 1) 解压并进入目录
tar xzf mynet_agent_darwin_arm64_v*.tgz
cd mynet_agent_darwin_arm64_v*

# 2) 落位二进制（安装包负责放文件；WebUI 已 embed，无 webui.html）
sudo mkdir -p /opt/mynet/bin /opt/mynet/etc
sudo cp mynet mynetd mynet_tray /opt/mynet/bin/

# 3) 从 GitHub Release 或 CI 取 gnb 四件套（同构建！）
#    gnb / gnb_ctl / gnb_es / gnb_crypto 必须来自同一构建包
#    只放 /usr/local/bin —— 标准位 $MYNET_HOME/driver/gnb/bin
#    由下一步 mynet install 自动置备
sudo cp gnb gnb_ctl gnb_es gnb_crypto /usr/local/bin/

# 4) 装服务（必须显式 MYNET_HOME）
export MYNET_HOME=/var/lib/mynet
sudo -E /opt/mynet/bin/mynet install \
  --gateways 'wss://gw.mynet.club/v1/node/connect' \
  --prefix /opt/mynet --home /var/lib/mynet
```

安装器会：
- 写 `/Library/LaunchDaemons/com.mynet.mynetd.plist`（KeepAlive）
- 写 `/Library/LaunchAgents/club.mynet.tray.plist` 并 bootstrap 到控制台用户
  gui 域（菜单栏托盘；无图形登录会话时下次登录自动拉起）
- 启动 `mynetd`（gnb 由 mynetd 拉起，macOS 无 `--systemd`）
- 置备 gnb 四件套到标准位 `/var/lib/mynet/driver/gnb/bin/`

**不拷贝二进制**——那是安装包的职责，`mynet install` 只装服务+托盘。

---

## 3. 登录

```bash
export MYNET_HOME=/var/lib/mynet
/opt/mynet/bin/mynet auth login \
  --api https://api.mynet.club \
  --email '你的邮箱' \
  --password-stdin   # 密码从 stdin 读，不进 shell 历史
```

凭证落在 `$MYNET_HOME/conf/credential.json`（0600）。

---

## 4. 节点身份与密钥

### 4a. 手上有私钥（恢复/迁移）

```bash
# 导入（公钥可省略，工具会从私钥推导；也可一并给出）
/opt/mynet/bin/mynet config keys set \
  --private '<128-hex 私钥>' \
  --public  '<64-hex 公钥>' \
  --node-id <你的 node_id> \
  --algorithm ed25519

# 上传公钥到 back（只传 public）
/opt/mynet/bin/mynet config keys sync
```

### 4b. 没有密钥（新装）

```bash
/opt/mynet/bin/mynet config keys generate --vpn gnb --node-id <node_id>
/opt/mynet/bin/mynet config keys sync
```

> 流程约定：**本地生成/导入私钥 → 只上传 public**。back 不持有私钥。
>
> `keys set/generate/sync` 会顺带装配 **back 节点面公钥**（back_pub_hex）：
> 缺了它 mynetd 节点面不装配，transport 报「NodeID 必填」，
> 「服务自动拉起 gnb」整条链路不会动。老节点补一次 `keys sync` 即回填。

---

## 5. 配置与启动

```bash
# 拉配置 + 投影 + OS 路由（推荐一条龙）
/opt/mynet/bin/mynet config sync

# 或分步
/opt/mynet/bin/mynet config pull      # 下载 bundle + 投影 driver/**
/opt/mynet/bin/mynet apply --active --auto
```

pkg 形态装完即自动开 WebUI（127.0.0.1:8096/ui/），登录→选节点→密钥→
拉配置→启动全程在页面内完成。CLI 口径：`config sync` 之后 mynetd 的
auto_restore 会随节点就绪拉起 gnb，无需手工 `service start gnb`
（那只用于临时排障）。

---

## 6. 验收

```bash
/opt/mynet/bin/mynet subject --json     # 主体定位
/opt/mynet/bin/mynet route doctor       # 路由是否进隧道
/opt/mynet/bin/mynet status
open http://127.0.0.1:8096/ui/          # WebUI（pkg 装完会自动弹出）
pgrep -l mynet_tray                     # 托盘在位（菜单栏 MyNet 图标）

# 节点面已装配、网关已连上的硬判据：
log show --last 10m --predicate 'process == "mynetd"' 2>/dev/null | \
  grep -E '节点面签名就绪|transport: connected'
# 或看 LaunchDaemon 标准错误落盘（EnvironmentVariables 指定）
grep -E '节点面签名就绪|transport: connected' /var/log/mynet/mynetd.err | tail
```

---

## 7. 卸载

```bash
sudo -E /opt/mynet/bin/mynet uninstall
# uninstall 保留 conf/var（含私钥）；要彻底删请自行 rm -rf $MYNET_HOME
sudo rm -rf /opt/mynet
```

---

## 8. 常见问题

| 症状 | 查 |
|---|---|
| 心跳/WS 不启动 | `MYNET_GATEWAYS` 是否在 `mynetd.env` |
| transport 报「NodeID 必填」 | back_pub_hex 未装配：`mynet config keys sync` 回填 |
| 菜单栏没有托盘 | `pgrep mynet_tray`；`launchctl print gui/$(id -u)/club.mynet.tray` |
| 起了但 ping 不通 | `mynet route doctor`；见知识库排错手册 A–E |
| `gnb_ctl` 读数不对 | 是否与 gnb **同目录同构建** |
| 私钥找不到 | `$MYNET_HOME/driver/gnb/conf/<id>/security/` |
| 配置改了不生效 | 改 `conf/**` 再 `mynet apply --active`，别改 `driver/**` |

更多：`mynet_ctl/docs/knowledge-base/troubleshoot-connectivity.md`
