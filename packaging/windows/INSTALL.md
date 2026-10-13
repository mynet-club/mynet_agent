# MyNet 安装指南 — Windows

> 产物：`mynet_agent_windows_amd64_v*.zip`
> 包内：`mynet.exe` `mynetd.exe` `mynet_tray.exe` + 本文件（WebUI 已 embed 进 mynetd.exe）
> 形态：**win = 安装器 + 托盘 + 装完自动开 WebUI**（zip 手工落位是当前过渡形态）
> 实测环境：Windows 10/11 x64（wintun 虚拟网卡）

---

## 1. 你需要准备

| 项 | 说明 |
|---|---|
| 平台账号 | 邮箱 + 密码 |
| 密钥对 | 有则导入，无则生成（**只备份私钥**） |
| 管理员 PowerShell | 装服务、建 wintun 网卡 |
| gnb Windows 四件套 | `gnb.exe` `gnb_ctl.exe` `gnb_es.exe` `gnb_crypto.exe`（同构建） |

---

## 2. 安装步骤

```powershell
# 1) 解压 zip，打开「管理员 PowerShell」
cd mynet_agent_windows_amd64_v*

# 2) 二进制落位
New-Item -ItemType Directory -Force -Path C:\mynet\bin, C:\mynet\etc | Out-Null
Copy-Item mynet.exe, mynetd.exe, mynet_tray.exe C:\mynet\bin\

# 3) gnb 四件套（同构建）放到 driver 树
New-Item -ItemType Directory -Force -Path C:\mynet\driver\gnb\bin | Out-Null
Copy-Item gnb.exe, gnb_ctl.exe, gnb_es.exe, gnb_crypto.exe C:\mynet\driver\gnb\bin\

# 4) 装服务（必须显式 MYNET_HOME）
$env:MYNET_HOME = "C:\mynet"
C:\mynet\bin\mynet.exe install `
  --gateways "wss://gw.mynet.club/v1/node/connect" `
  --prefix C:\mynet --home C:\mynet
```

安装器写 `C:\mynet\mynetd.env`，并：
- 注册 Windows 服务 `mynetd`（SCM，start=auto，失败自动重启），gnb 由
  mynetd 的 auto_restore 随服务拉起，不单独立服务
- 写 `HKLM\...\CurrentVersion\Run\MyNetTray` 托盘自启（带 `-home C:\mynet`，
  下次登录进菜单栏）
- mynetd 就绪后自动打开 `http://127.0.0.1:8096/ui/` 引导配置
  （⚠ MSI 的 SYSTEM custom action 会话里开页不可见，归 MSI 收尾处理）

**注意**：
- 同目录查找认 `.exe`；`gnb_ctl` 必须与 `gnb` **同目录**
- 路径不要用 `C:\Program Files\mynet`（权限/空格麻烦）

---

## 3. 登录

```powershell
$env:MYNET_HOME = "C:\mynet"
# 密码用 stdin，避免进历史
"你的密码" | C:\mynet\bin\mynet.exe auth login `
  --email "你的邮箱" --password-stdin
```

凭证：`C:\mynet\conf\credential.json`（0600 等价 ACL）。

---

## 4. 密钥

**恢复/迁移：**
```powershell
C:\mynet\bin\mynet.exe config keys set `
  --private "<128-hex 私钥>" --public "<64-hex 公钥>" `
  --node-id <node_id> --algorithm ed25519
C:\mynet\bin\mynet.exe config keys sync
```

**新装：**
```powershell
C:\mynet\bin\mynet.exe config keys generate --vpn gnb --node-id <node_id>
C:\mynet\bin\mynet.exe config keys sync
```

> `keys set/generate/sync` 会顺带装配 **后端节点面公钥**（back_pub_hex）：
> 缺了它 mynetd 节点面不装配，transport 报「NodeID 必填」而不连网关。
> 老节点补一次 `keys sync` 即可回填。

---

## 5. 配置与启动

```powershell
C:\mynet\bin\mynet.exe config sync
```

`config sync`（pull + 投影 + apply --auto）之后，mynetd 服务的 auto_restore
随节点就绪拉起 gnb；`mynet service start gnb` 只用于临时排障。

---

## 6. 防火墙

Windows Defender 防火墙需放行：
- gnb 的 **UDP listen 端口**
- ICMP（排障 ping，可选）

```powershell
New-NetFirewallRule -DisplayName "MyNet gnb UDP" -Direction Inbound `
  -Protocol UDP -LocalPort 9016 -Action Allow
```

回程路由由 `mynet apply --auto` 装；若对端 ping 不进，先查入站规则。

---

## 7. 验收

```powershell
C:\mynet\bin\mynet.exe subject --json
C:\mynet\bin\mynet.exe route doctor
C:\mynet\bin\mynet.exe status
start http://127.0.0.1:8096/ui/
```

---

## 8. 卸载

```powershell
$env:MYNET_HOME = "C:\mynet"
C:\mynet\bin\mynet.exe uninstall   # 清 mynetd 服务 + 托盘 Run 键 + mynetd.env
Remove-Item -Recurse -Force C:\mynet   # 含私钥，先确认已备份
```

---

## 9. 常见问题

| 症状 | 查 |
|---|---|
| mynetd 找不到 mynet | 同目录 `mynet.exe`（带后缀） |
| 隧道起不来 | wintun 驱动；管理员权限 |
| 全网 ping 不进 | 防火墙入站 + 回程路由 |
| SSH 会话里起停异常 | 用 `powershell -NoProfile -File xxx.ps1` |
| 构建/交叉编译 | 需 mingw-w64 或 CI `build_windows.yml` |

排错：`troubleshoot-connectivity.md` A–E
