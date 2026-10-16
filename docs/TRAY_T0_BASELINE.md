# 托盘优化 T0 —— 基线报告与合同差异表

> 当前处置同步（2026-10-16）：本文保留实测结果、根因和 D 编号，不因方案改为渐进修复而取消事实。
> 实施依据为 [Tray 渐进优化 Playbook](TRAY_OPTIMIZATION_PLAYBOOK.md)。下文旧节号、T 编号、类型和时序常量记录取证当时的设计，不再约束实现结构；当前工作包见 §6。
> D9 保留 want_node 格式/函数签名，仅修正用户可写目录，无 scope/schema/自动迁移；D12 导入不触发连接、不隐式断开；D13 必须修权限/互斥/超时/脱敏；D15 撤回 daemon Controller 死件；D20 保留 text.go 并接入文本出口。

配套历史 T0 取证交付物：基线报告、正反 fixture、合同差异表。历史测试输出不代表当前代码测试结果。
本文 **D 编号仅指「冻结合同 vs 现状」的差异**，与 AGENTS.md §7.1 的 D1–D5 无关；
B 编号沿用 playbook §2 的现状证据表。

T0 约束遵守情况：**未修 UI、未动服务**。本轮新增文件只有
`cmd/mynet_tray/contract.go`（纯类型，无行为）、`cmd/mynet_tray/t0_contract_test.go`、
`cmd/mynet_tray/testdata/`（14 份 fixture）与本文。

证据等级标注沿用 playbook §10：`源码核对` / `fake行为` / `本机原生` / `目标真机` / `发布产物`。

---

## 1. 基线实测

全部命令在 `/Users/arthur/Devel/WorkSpaceAI/mynet-club/mynet_agent` 下执行，
输出重定向到日志后再读退出码（不用管道，避免管道吞掉真实 exit）。

| 项 | 命令 | 结果 | 证据等级 |
|---|---|---|---|
| 原有测试（不含 T0） | `go test -count=1 -v -run '^Test[^T]\|^TestT[^0]' ./cmd/mynet_tray` | EXIT=0，**PASS=16 FAIL=0** | fake行为 |
| T0 新增测试 | `go test -count=1 -v -run 'TestT0' ./cmd/mynet_tray` | EXIT=1，**PASS=5 FAIL=9** | fake行为 |
| race | `go test -count=1 -race ./cmd/mynet_tray` | EXIT=1，`DATA RACE` 计数=**0**，失败全部来自 T0 红测试 | fake行为 |
| go vet | `go vet ./cmd/mynet_tray` | EXIT=0 | 源码核对 |

**race 通过是空的**：现有 16 个测试只跑纯函数（`FSM.On`、`eventFromProbe`、`Controller` stub），
从不启动真实 App 的 goroutine，因此 playbook §8.1 末行「`go test -race` 走实际
App worker→queue→reducer」在 T1 之前**无法满足**，不得当作 B4 已排除的证据。

交叉构建（`go build -o /tmp/...`，产物落临时目录不入库）：

| 目标 | 结果 | 备注 |
|---|---|---|
| darwin/arm64 tray | EXIT=0 | 宿主原生，`CGO_ENABLED` 隐式=1 |
| darwin/amd64 tray | EXIT=0 | **必须显式 `CGO_ENABLED=1`**；`file` 验证为 `Mach-O 64-bit executable x86_64` |
| windows/amd64 tray | EXIT=0 | `CGO_ENABLED=0`，`systray_windows.go` 无 cgo |
| linux/amd64 tray | EXIT=0 | `CGO_ENABLED=0`，走 godbus StatusNotifierItem |

cgo 只在 darwin 腿需要，来源是 `fyne.io/systray@v1.12.2/systray_darwin.go:6-7`
（`-x objective-c -fobjc-arc` / `-framework Cocoa`）+ `systray_darwin.m`。
`Makefile:147` 自 `952011de`（2026-09-27，托盘 dist 腿首次写入）起就带 `CGO_ENABLED=1`，
**项目侧无缺陷**；Go 的隐式规则是「仅当 GOOS/GOARCH 都等于宿主原生值时 CGO 默认 1」，
交叉编译静默降为 0。这与 playbook §8.2「若 CGO 阻止交叉构建，使用对应 native runner，
不伪装 `CGO_ENABLED=0` 通过」一致，`Makefile:144-151` 现有写法（Darwin 宿主才编 darwin tray）
是正确处置。

### 1.1 遗留门禁洞（移交 T6）

`Makefile:149` 在非 Darwin 宿主上对 darwin 腿只 `echo "  ⚠ 跳过 …"`：
`set -e` 不会被 `echo` 触发，于是 **Linux runner 上 `make dist` 会产出缺 `mynet_tray`
的 macOS 包并返回 0**。playbook §8.2 要求「macOS 两架构和 Windows amd64 必须各有实际
build 记录」，T6 必须把这条改成硬失败，或改成显式、可审计的缺件清单。

### 1.2 绿测试为何掩盖了用户可见故障（B3 的机制）

`status.go:116-142` 的 `eventFromProbe` **只可能**返回
`EvDaemonDown | EvNodeHealthy | EvNodeDegraded | EvNodeFailed`，永远不返回 `EvDaemonUp`；
而 `fsm.go:198-211` 的 `StateStarting` 分支**只**接 `EvDaemonUp / EvStartTimeout / EvStop / EvDaemonDown`。
两条边永不相交 ⇒ 真机上连接成功后仍停在 Starting，20s（`tray.go:34 startWait`）后
`render()` 显示「启动超时」。

但 16 个测试全绿，因为：

- `fsm_test.go:25` 手工喂 `EvDaemonUp` 完成 Starting→Running —— 这个事件真实探针产不出来；
- `fsm_test.go:136-172` 自称「穷尽核对」，实际只枚举**已存在**的转移边，
  漏掉 `{StateStarting, EvNodeHealthy}` 与 `{StateFailed, EvNodeHealthy}`，
  于是「应转移却 no-op」这类缺陷在结构上不可能被测出来。

⇒ T1 的转移表测试必须以**探针可达事件集**为定义域，而不是以现有转移表为定义域。
反证已固化为绿测试 `TestT0_ProbeNeverEmitsDaemonUp`。

---

## 2. 取证：真实合同（冻结事实）

以下为 `源码核对` 结论，是 T1–T7 不得再猜的共享语义。

### 2.1 状态端点

- `mynetd/localapi/node_status_handler.go:37`：`200 {"success":true,"data":<NodeStatus>}`；
  执行器失败 `:22` `500 {"success":false,"message":…,"stderr":…}`。
  **500 不是「节点未连接」**（B11）。
- 权威结构 `mynet/node_status.go:37-86`；wire 状态含真实常量
  `StateStopped NodeState = "STOPPED"`（`:33`）—— 必须映射为节点面 Disconnected，
  不得塌成服务面 Unavailable。
- `installed` 是 wire 字段；现状 `status.go:101` 用 `st != "" && st != "UNINSTALLED"`
  **推导** NodeInstalled，绕过了权威字段。
- `config_mismatch` 只在 `cmdNodeStatusJSON`（`node_status.go:879-886`）里设置，
  并强制 `State=DEGRADED`；`overlay_ip` 取值优先级见 `:522-539`。
- `one_line` 仅解释事实（§3.2），不参与状态判断。

### 2.2 action key（执行器权威，托盘只能收紧）

`mynet/node_status.go:101-110`，共 8 个：
`install, configure, start, restart, stop, fix-routes, uninstall, retry`。
**没有 `connect` / `disconnect` 键** ⇒ 托盘的「连接/断开」必须由固定 argv 表达，
不能指望远端给出同名动作；各状态下的 actions 集合见 `computeNodeStatus`（`:474-729`）。
已冻结映射：`ConnectActionKeys = {start, retry}`、`DisconnectActionKeys = {stop, restart}`。

### 2.3 命令端点

- 白名单 `handlers.go:106-117` `allowedSubcommand`，含 `config`；
  非 POST → 405；缺 argv → 400；子命令不在表内 → 400 `subcommand not allowed`；
  secret 走 argv → 400 拒绝（`:34-76`）。
- 并发锁 `handlers.go:119-127`：`isContextMutation(argv)` 为真时抢 `configMutation`，
  抢不到 → `409 {"success":false,"code":"operation_in_progress","message":"已有配置操作正在执行，请稍后刷新状态再试","retryable":true}` + `Retry-After: 2`。
- `isContextMutation`（`connect_api.go:123-146`）：
  `service, setup, apply, install, uninstall, update, refresh-config, refresh-routes, refresh-address, plugin` 恒真；
  `node claim` 真；`config` 除 `status/identity/runtime/cloud/plugins/tenants/parity` 与
  `keys show|status|--json` 外恒真 ⇒ **`config import` / `config export` 走 API 会拿锁**。
- 成功 `handlers.go:164`：`200 {"success":true,"data":{argv,stdout,stderr,bin}}`。
- 失败 `:139-153`：`500 {"success":false,"message",…}`，带 `stdout`/`stderr`/`argv`，
  三者均经 `sanitizeValue` / `redactArgvAny` 脱敏；成功路径经 `sanitizeWizardOut`（`:161-163`）。
  ⇒ **daemon 侧已做脱敏，托盘本地 exec 则完全没有**。
- 执行超时 `defaultExecMynet`（`:167-188`）60s ctx。
- 副作用 `handlers.go:63-73`：仅当 `argv[0]=="service"` 时按 `argv[2]` 嗅探驱动
  （默认 `gnb`，`argv[2]=="wireguard"` 才是 wireguard）→ `markTunnelWantDriver`，
  落 `utils.VarDir()/tunnel-state.json`（`config_api.go:475-486`）。
  ⇒ **`apply --active` 从不写 `want_running=true`**。

### 2.4 安全面

`server.go:219-271` `withSecurity` = `checkHost` + `checkOrigin` + `ensureToken`：

- `checkHost`（`:236-256`）白名单 `localhost / 127.0.0.1 / ::1 / [::1]` + `cfg.AllowedHosts`；
  非回环 Host → `403 {"success":false,"message":"invalid host"}`。
- `checkOrigin`（`:259-278`）无 Origin 头放行（curl/本机工具）；有则接受
  `http://127.0.0.1*` / `http://localhost*` / `http://[::1]*` 前缀 + AllowedHosts 包含匹配。
- `ensureToken` 只**种** HttpOnly+SameSite=Strict cookie，**从不要求** ⇒
  `/api/v1/*` 上没有 bearer 鉴权，回环隔离是唯一屏障。
  托盘侧不得放宽（§10「loopback/Origin/鉴权未削弱」），
  `ParseEndpoint` 的回环判据必须与 `checkHost` 白名单**严格同源，含 `localhost`**。

### 2.5 CLI 导入导出

- `mynet/cmd_config.go:214` `cmdConfigExport` 用 `-o` 指定输出；
  `:335` `cmdConfigImport` 用**位置参数**指定文件 + `--dry-run`，未给文件时取 `backups/` 最新一份。
- `cmdConfigImport:394` **自己已调 `ApplyFromActive(false)`** ⇒ 调用方再启一次隧道就是双重 apply。
- 退出码：`0` 成功；`1` 读包/格式/备份/恢复/apply 失败；`2` 无备份包。
- 无信号处理、无自身超时、**无「隧道必须先停止」的前置闸门**
  （§5.3 的停止前提得由托盘侧建立，不能指望 CLI）。
- 导入前 `backupBeforeRestore(home)` 先备份现状，失败即拒绝导入（CLI 权威，托盘不得绕过）。

### 2.6 进程身份与目录权限（决定「谁能写哪」）

| 组件 | 登记位置 | 运行身份 |
|---|---|---|
| mynetd | `/Library/LaunchDaemons/com.mynet.mynetd.plist`（`cmd_install_launchd.go:55`，label `shared/utils/service_names.go:19`） | **root** |
| 托盘（安装器登记） | `/Library/LaunchAgents/club.mynet.tray.plist`（`cmd_install_launchd.go:56`、`:118`、`installTrayAgent:125-155`，bootstrap 进 `gui/<uid>`，label `service_names.go:23`） | **登录用户** |
| 托盘（自己 Enable） | `~/Library/LaunchAgents/club.mynet.tray.plist`（`autostart.go:40-43`） | **登录用户** |

- `MYNET_HOME` 默认：darwin/linux `/opt/mynet`（`main.go:45`）。
- `VarDir()`（`shared/utils/paths.go:416-428`）= `$MYNET_DATA_HOME` 否则 `$MYNET_HOME/var`，
  以 `0o700` 创建且**丢弃错误**；`SafeCreateDir` 明确「shared 不做 chown」（`paths.go:514`）。
- 全仓 grep `Chown|chown` **只命中那条注释**，无任何 chown 调用 ⇒
  `sudo mynet install` 之后 `/opt/mynet/{conf,var}` 属 root。
- 安装器给托盘的 plist（`launchdTraySpec:82-89`）传 `MYNET_HOME` env，
  但**不传 `-api` / `-home` 旗标**；托盘自启写的那份（`autostart.go` `Enable`）**只有
  `<string>self</string>`**，连 env 都没有。
- Windows：安装器写 `HKLM\…\Run` 值名 `MyNetTray` = `"<tray>" -home "<home>"`
  （`cmd_install_win.go:126`、`installTrayRunKey:128-145`），
  托盘自己读写 `HKCU\…\Run` **同值名**（`autostart.go:30`）。
  `cmd_install_win.go:132-135` 的注释已明确警告：托盘默认猜 `ProgramData\MyNet`
  而安装器 home 是 `Program Files\MyNet`，「intent 文件不同树，托盘的启停按钮就是摆设」。

⇒ **结论（T5 硬约束）**：托盘以登录用户身份本地 exec `mynet config import|export`
必然对 root 拥有的 `/opt/mynet` 写失败；这类操作必须走 daemon API（root）才能成功，
但走 API 又必须处理「导出文件由 root 写到用户选定路径」的属主问题。二者不可兼得，
T5 需在此明确取舍，不得两边各留一半。

---

## 3. 合同差异表

「测试」列中标红的用例**当前必须失败** —— 它们是 T0 的交付形态（失败 fixture），
由「归属」列的包负责转绿；**全部转绿前不得提交**（`.githooks/pre-push` 跑 `make check`）。

| 编号 | 取证时合同（旧节号） | 已取证现状 | 证据 | 测试/历史设计 | 原归属（当前见§6） |
|---|---|---|---|---|---|
| D1 | §3.3 Connecting 收到 RUNNING 即完成 | Starting 收不到任何探针可达事件，连接成功后 20s 报「启动超时」 | `status.go:116-142` × `fsm.go:198-211`、`tray.go:34` | `TestT0_ConnectCompletesOnRunningProbe`（红） | T1+T2 |
| D2 | §8.1 Failed→healthy 由真实事实恢复，无需重启托盘 | `EvDaemonUp` 不可达 ⇒ StateFailed 是**终态** | `fsm.go:246-264` | `TestT0_FailedRecoversOnHealthyProbe`（红）；`TestT0_ProbeNeverEmitsDaemonUp`（绿·反证） | T1 |
| D3 | §3.2 禁止用一个 Down 同时表示服务离线与节点停止 | `!Up`/非200/非法JSON/UNINSTALLED/INSTALLED/CONFIGURED/STOPPED/空 全塌成 `EvDaemonDown` | `status.go:116-142`（B2） | `TestT0_ServiceDownDistinctFromNodeStopped`（红） | T1+T2 |
| D4 | §3.2 不把 HTTP 200 当健康，不把未知 schema 当未连接 | 200+信封非法 → `st=""` → `EvDaemonDown`（显示「已停止」） | `status.go:74-100` | `TestT0_InvalidEnvelopeIsNotDisconnected`（红） | T2 |
| D5 | §3.1/§8.1 200+业务失败必须映射固定 reason | `postCmd` 只判 `StatusCode>=300`，`success:false` 被当成功 | `tray.go:140-159`（B5） | `TestT0_CommandEnvelopeValidated`（红） | T2 |
| D6 | §8.1 409 → 固定 reason `operation_in_progress`，不自动写重试 | 409 当普通失败、body 原文进 UI；连接与断开**都**可能 409 | `handlers.go:119-127`、`connect_api.go:123-146` | `TestT0_Command409MapsToInProgress`（红） | T2 |
| D7 | §3.1 只显示安全文案；§8.1 不泄漏 body | 错误分支把 body 前 200B 拼进 error；`runMynet` 把 `CombinedOutput` 全量拼进 tooltip | `tray.go:152-158`、`tray.go:416` | `TestT0_CommandErrorHidesRawBody`（红） | T2 |
| D8 | §3.2 `one_line` 去换行与控制字符，菜单 ≤48 Unicode 字符 | OneLine 原样进模型；`render()` 把 `lastErr` 拼进 title；导出路径进 tooltip | `status.go`、`tray.go:288-308`、`tray.go:374` | `TestT0_PrivacyInjectionStaysOutOfTooltip`（红） | T2（清洗）+T3（渲染） |
| D9 | 当前：用户可写目录；保留 want_node 格式/签名，无自动迁移 | 落 `$MYNET_HOME/var`（root 0700），且 `_ = saveIntent(...)` 丢弃错误 ⇒ 普通用户「记住连接意图」从未生效 | `intent.go`、`paths.go:416-428`、`main.go:45`、`tray.go:197/235`（B9） | `TestT0_IntentLivesInUserConfigDir`（红） | P3 |
| D10 | §6.2/§10 自启反映**实际**来源与状态；无第二份入口 | 安装器写系统 `/Library/LaunchAgents`，托盘只 `os.Stat` 用户 `~/Library/LaunchAgents` ⇒ 看不见安装器那份；托盘 `Enable` 的 plist 只有 `self`、无 `MYNET_HOME` ⇒ home 落默认值 | `autostart.go:40-43`、`cmd_install_launchd.go:56/82-89/125-155`、`service_names.go:23`（B7） | 本轮**不出 fixture**，见 §5 | T4 |
| D11 | 同 D10 + §9.6「重复托盘」是回滚触发 | 安装器写 HKLM Run、托盘读写 HKCU Run **同值名** ⇒ 启用产生跨 hive 双份（双托盘）、停用只删 HKCU（用户永远关不掉）、托盘 `Enable` 不带 `-home`（正是 `cmd_install_win.go:132-135` 警告的故障）；`IsEnabled` 把权限错误当「未启用」，`Disable` 忽略删除错误 | `autostart.go:30/94-101/103-110/112-115`、`cmd_install_win.go:126/128-145` | 同 D10 | T4 |
| D12 | 当前：导入不触发连接；不隐式断开已有隧道；P1 与真连接修复同时处理 | 导入成功后无条件 `fsm.On(EvStart)` + `startTunnel()`；而 CLI 自己已 `ApplyFromActive(false)` ⇒ 双重 apply | `tray.go:395-396`、`cmd_config.go:394`（B8） | 归属包内补 | P1（连接规则）/P3（执行） |
| D13 | 当前：配置经既有 daemon command 面，核实用户/daemon文件权限，复用锁与60s执行上限，并做托盘侧脱敏 | 本地 exec 绕过 `configMutation` 锁、绕过 60s ctx（`CombinedOutput` 无 ctx ⇒ 可无限阻塞 clickLoop）、绕过全部脱敏；且以**登录用户**身份写 root 目录 ⇒ 必然 permission denied，而该 stderr 又被原样进 tooltip | `tray.go:371/391/413-416`、`handlers.go:106-127/139-163/167-188`、`connect_api.go:132-143`、本文 §2.6 | 归属包内补 | P3 |
| D14 | §3.3 核对 active driver，不扩展未验证的双驱动能力 | 断开 `service stop gnb` **硬编码 gnb**：wireguard 节点上停的是没在跑的 gnb，wg 隧道留着不动。驱动名**承重**：它被写进 `var/tunnel-state.json`，开机恢复照它 `service start <drv>` | `tray.go:136`、`config_api.go:475-486`、`tunnel_restore.go:141-146`、`cmd/mynetd/main.go:401-404` | 已改为 `DisconnectArgv()` **省略驱动名**，由 CLI 从 bundle 解析（`cmd_apply.go:173-189`）；`handlers.go:63-67` 对 `argv[2]` 有长度守卫，两元素 argv 不越界 | T2 |
| D15 | 当前：撤回 daemon Controller 实现及入口/存储死接线，不复用到隧道链 | `NewController(*mynetd)`、`a.ctrl`、`control.go` 三平台实现（281 行）全仓**零引用**；`main.go:6` docstring 仍写「启停 daemon」 | `main.go:6/59`、`tray.go:62`、`control.go`（B12） | grep 已验证零引用 | P0清单与收敛 |
| D16 | §3.1 Endpoint 拒绝非 loopback；§10 鉴权未削弱 | 非回环 `-api` → daemon 403 `invalid host` → 现状塌成 `EvDaemonDown` 显示「已停止」；`/api/v1/*` 无 bearer 鉴权（`ensureToken` 只种不验） | `server.go:219-278`、`status.go:116-142` | `TestT0_ContractEndpointRejectsNonLoopback`（绿） | T2 |
| D17 | §5.1 兼容分支必须「已有且测试覆盖」才可保留 | 状态响应的顶层（非 `data`）兼容分支**零测试覆盖**（`status_test.go` 只测 `eventFromProbe` 映射，无 HTTP 级测试）⇒ 不具保留资格 | `status.go:74-100`、`status_test.go` | T2 删除该分支，改以 httptest 覆盖信封校验 | T2 |
| D18 | §3.1 NodeFacts 缺字段不猜，执行器权威 | `installed` 是 wire 字段却被 `st != "" && st != "UNINSTALLED"` 推导 | `status.go:101` vs `node_status.go:37-86` | 已冻结为 `NodeFacts`，T2 直接读字段 | T2 |
| D19 | §3.3「连接」必须真的连接；T0 职责是核对 CLI 合同 | **连接按钮从未连接过**。冻结值 `apply --active` 的文档头自己写着「投影：…（**不装 OS 路由**）」，`cmdApply` 全程不起隧道进程；且它不在 `service` 分支里，`markTunnelWantDriver` 从不触发 ⇒ `want_running` 永远不被置真，开机也不恢复。真连接编排在守护侧已存在：`POST /api/v1/config/connect` | `cmd_apply.go:25,33,35`、`cmd_apply.go:208-245`（`service start` 才是 RequireLocalIdentity→ApplyFromActive→`d.Start()`→verifyTun→`apply --active --auto`）、`connect_api.go:88-121`、`handlers.go:63-73` | 合同改为 `ConnectPath = /api/v1/config/connect`（专用端点、无 argv）；顺带继承 409 锁、`redactSecrets`、`need_login`→401、start/routes 失败自动 cleanup。新增 4 份 fixture：`connect_ok` / `connect_400_not_ready` / `connect_need_login` / `connect_409` / `connect_stage_failed` | T2 |
| D20 | 当前：保留 text.go；中文秘密关键字/长hex隐私注入，覆盖所有UI文本出口 | 守护侧 `redactSecrets` **只认英文关键字**（`secretKeywords = token/refresh/priv/private/password/secret`），中文「私钥/密钥」行里的 64 位 hex **原样透出**。实测：`lineHasSecretKeyword("私钥指纹 aaaa…1111")=false`，输出未变；同串换成 `private key …` 则被遮成 `********[hex]` ⇒ 托盘侧遮蔽不是冗余，是承重的 | `wizard.go:376`、`wizard.go:471-491`；实测探针见本文 §2.4 | `text.go` 的 `secretKeyword` 含 `密钥\|私钥\|口令\|凭据`；fixture `connect_stage_failed.json` 刻意保留未遮的指纹与绝对路径，用来证明托盘不信任上游脱敏 | P0保留/P3接入 |

playbook §2 的 B1（双主操作项、固定图标、错误拼进首行、OneLine 未进渲染）、
B4（多 goroutine 改 FSM/deadline/lastErr）、B6（启动未 `syncAutoMenu`、错误静默、
点击阻塞 clickLoop）属 UI/编排面，T0 不改 UI 故不在本轮出 fixture，
分别由 T3、T1、T3+T5 承接；其证据已在 §1.2 与 §2.6 补齐到 `file:line` 级。

### 3.1 已冻结且当前即绿的合同锁

这五项是 T0 的「正向」交付，防止后续包在实施中放宽：

| 测试 | 锁住的语义 |
|---|---|
| `TestT0_ProbeNeverEmitsDaemonUp` | 穷举证明 `EvDaemonUp` 不可达（D1/D2 的反证，见 §1.2） |
| `TestT0_ContractOverlayIPValidated` | `overlay_ip` 必须是合法 IP；缺失/非法不得进剪贴板（§8.1「不复制旧 IP」）。**2026-10-16 更新（P4 已落）**：T0 只锁了合同侧，消费侧现在也有了 —— `validOverlayIP`（`tray.go:830`）+ 缺失即 `Hide()`（`tray.go:742-745`）+ 仅点击才写（`tray.go:845`）；且已取证 `overlay_ip` 是**配置面**值（`mynet/node_status.go:521-539`），STOPPED 时也在，不能当连通信号 |
| `TestT0_ContractMismatchForbidsConnect` | `config_mismatch` ⇒ `CanConnect()==false`；托盘不得放宽执行器的 `actions` 事实 |
| `TestT0_ContractActionsAreFactsNotDefaults` | `actions` 缺键即禁止，不以「默认可用」兜底 |
| `TestT0_ContractEndpointRejectsNonLoopback` | Endpoint 回环判据与 `checkHost` 同源（含 `localhost`）；非回环/非法定端口一律拒绝 |

`contract.go` 是**纯类型 + 纯判据**，刻意不含任何行为实现：
T0 若顺手写出 reducer/adapter 的空壳，就构成 AGENTS.md §7.1 的 D1（返回 error 的桩当已完成）。
冻结的时序常量（`PollSteady=3s`、`PollActive=1s`、`ProbeTimeout=2s`、`StaleThreshold=10s`、
`ConnectDeadline=40s`、`DisconnectDeadline=30s`、`CommandTimeout=20s`、`ConfigOpTimeout=120s`、
`MaxStatusBody=1MiB`、`MaxCommandBody=256KiB`、`MaxMenuRunes=48`）与 §3.2/§3.3 逐条对应。

---

## 4. fixture 清单（`cmd/mynet_tray/testdata/`）

全部使用**虚构**节点与地址（node_id `4147069894021999` / `4147069894022001`，
overlay `10.200.0.17`，peers `10.200.0.21` / `10.200.0.33`），
假凭据只有两种形状：`FAKE-SECRET-PAT-…` 与一段 64 位十六进制串。
不含真实归档、私钥或凭据（§8.1）。

`t0_contract_test.go` 的 `fixtureStatus()` 为每份 fixture 绑定**其真实 HTTP 状态码**
（200 / 500 / 409）并注明来源行号；`serveFixture(t, name)` 起 httptest server，
使被测代码走完整 HTTP 路径，而不是直接喂结构体。

正向（合法事实）：

| fixture | 用途 |
|---|---|
| `status_running.json` | RUNNING + process_up + overlay_ip + actions —— D1 的关键输入 |
| `status_stopped.json` | STOPPED（真实 wire 常量）⇒ 必须映射节点面 Disconnected，非服务面 Unavailable |
| `status_degraded_routes.json` | DEGRADED + process_up ⇒ 连接受限而非失败 |
| `status_failed_pid.json` | FAILED + process_up ⇒ 先安全停止再允许重连 |
| `status_uninstalled.json` | UNINSTALLED ⇒ 不可 start，设置入口可见 |
| `status_installed_unconfigured.json` | INSTALLED/CONFIGURED ⇒ 同上，且不得当「已断开」 |
| `status_mismatch.json` | `config_mismatch=true` ⇒ 禁止 start |
| `command_ok.json` | `200 {"success":true,"data":{argv,stdout,stderr,bin}}` |

反向（必须被识别为失败/无效，不得静默通过）：

| fixture | 用途 |
|---|---|
| `status_executor_500.json` | 执行器失败 500 —— **不是**「节点未连接」（B11） |
| `status_invalid_200.json` | 200 但形状错误 ⇒ Invalid，不是 Disconnected（D4） |
| `status_secret_injection.json` | 200，`one_line` 内嵌假 PAT + 真实换行 + BEL/ESC 控制字符，`detail` 内嵌 64 位十六进制假私钥 ⇒ 清洗与渲染都不得泄漏（D8） |
| `command_200_business_failure.json` | 200 + `success:false` ⇒ 必须判失败（D5） |
| `command_500.json` | 500 + stdout/stderr/argv ⇒ 固定 reason，原文不进 UI（D7） |
| `command_409.json` | 409 + `code:operation_in_progress` + `retryable:true` ⇒ 映射为「操作进行中」，不自动重试（D6） |

隐私检测用 `hexRun = regexp.MustCompile("[0-9a-fA-F]{16,}")` 扫 tooltip/文案/日志，
凡命中 16 位以上十六进制串或 `FAKE-SECRET-PAT` 前缀即判泄漏。

---

## 5. 未验限制

1. **自启差异（D10/D11）本轮只取证、不出 fixture。**
   现有 `AutoStarter` 接口把路径与注册表键写死在实现里（`autostart.go:30/40-43`），
   没有可注入 seam；要写「双来源」测试必须先引入 T4 的新接口。
   在 T0 里为此改产品代码就越过了「不修 UI」的边界，故按 §7 归属推给 T4，
   取证结论已在 §2.6 固化到 `file:line`。
   **2026-10-16 更新（P3 已落）**：不需要新接口 —— 把 `systemDir`（darwin）与
   `systemKey`（windows）做成实现内的可注入字段就够了，`AutoStarter` 三个方法
   签名一字未动。双来源已有测试：`autostart_test.go` 5 例（darwin，临时目录）
   + `autostart_windows_test.go` 5 例（192.168.0.5 真机，临时 HKCU 键顶替 HKLM）。
2. **B4 并发缺陷未被证明。** race 跑通是空的（§1），真实 App 编排的 race 矩阵属 T1（§8.1）。
   **2026-10-16 更新（P2 已落）**：`concurrency_test.go` 8 组用例真实并发驱动
   clickLoop / 3s 轮询 / 启停 worker，`-race -count=5` 零报警。
3. **全部结论止于 `源码核对` + `fake行为`。**
   无任何 `本机原生` / `目标真机` / `发布产物` 级证据；未启动过真实 mynetd 或托盘进程，
   未在任何设备上验证菜单、图标、DPI、文件框或登录启动。
   **2026-10-16 更新（P3/P4）**：Windows 自启腿已有真机级证据（192.168.0.5，见下条）；
   托盘 UI 本体（菜单排布、tooltip、图标观感、`openURL` 实际拉起）仍未在任何平台实机运行 ——
   P4 只出了图标三态的**渲染对照图**（模板腿/彩色腿 × 深浅菜单栏 × 16px 与 4× 放大），
   那是 stdlib 派生结果的直接呈现，不等于菜单栏里的真实观感。归 P5。
   **2026-10-16 更新（P5）**：托盘进程已在本机**真跑**（新构建 darwin/arm64，
   `-api 127.0.0.1:8096`，日志零报错，节点事实 RUNNING），自启的系统级路径与 label
   也与真机 `/Library/LaunchAgents/club.mynet.tray.plist` 逐字对上；但**菜单栏观感仍无证据** ——
   本机 `screencapture` 报 `could not create image from display`（宿主没有「屏幕录制」权限），
   图标与菜单排布只能由用户肉眼核对。这是 P0–P5 唯一交不出的证据，不宣称完成。
4. **Windows/Linux 侧只有交叉构建通过记录**，不等于可运行（§8.2、§8.3）。
   **2026-10-16 更新（P3）**：Windows 侧已有**可运行**级证据 —— 交叉编出的测试
   exe 在 192.168.0.5 实跑 5/5 通过（真实调用 reg.exe，非 mock）。但托盘 UI 本体
   仍未在任何平台实机运行；Linux 侧仍只有交叉构建记录。
   **2026-10-16 更新（P5）**：Windows 真机证据扩到 **20 个用例全 PASS（RC=0）**，
   覆盖注册表自启 5 条 + 图标 4 条 + 旅程 + 合并项/复制 IP 各组；
   `TestDiagURLMatchesWebUIPage` 刻意排除（它读开发树里的 `webui/index.html`）。
   **DPI 与菜单实际观感仍未验** —— 那需要交互式桌面会话，SSH 里跑不出菜单栏。
   Linux 侧仍只有交叉构建记录（托盘也不出 Linux 腿，Makefile:147-153）。
5. **T0 的 9 个红测试是刻意的**，`make check` / `pre-push` 当前会红；
   在 T1（D1/D2/D3）与 T2（D4–D8/D14/D16/D17/D18）转绿前不得提交。

---

## 6. 当前处置与下一依赖（2026-10-16）

当前顺序为 P0 → P1 → P2 → P3 → P4 → P5，沿用现有 App/FSM。原 T1–T7 分派被本节替代，不要求 reducer、ViewModel、新偏好 schema 或迁移框架。

| D 编号/事实 | 当前责任与处置 |
|---|---|
| D1/D2、B3 | P1：原 FSM 接收真实探针可达事件，补连接完成/失败恢复回归 |
| D3、B4 | P2 现有 App 并发修复；P3 显示服务/节点差异。按实际用例核对，不用纯 FSM race 替代 App race |
| D4–D8/D14/D16/D17/D18 | P1/P3：在现有 HTTPProber/postCmd 内修响应判定、事实读取与错误处理；保留取证，不强制历史 typed adapters/时序常量/endpoint新限制 |
| D9 | P3：intentPath → os.UserConfigDir()/MyNet/tray-intent.json；保留 trayIntent/want_node 与函数签名，用户私有目录、文件0600。无自动迁移；旧文件不删，缺新文件按既有默认false；普通用户保存/重读和权限失败均需测试 |
| D10/D11 | P3：复用 AutoStarter，核实自启来源与初值/操作后状态，失败不可伪装关闭。**2026-10-16 已落**：双来源判定（系统级 OR 用户级）、系统级在位时 `Enable` 拒绝并返回 `errSystemManaged`、`Disable` 先清用户级遗留再报「系统级仍在位」、读不到显示「登录时启动（状态未知）」不冒充未勾选（P4 把「开机启动」全部改为「登录时启动」：登记的是 LaunchAgent / Run 键，不是开机）、点击失败经 `notice` 上屏且原始 OS 错误不出现在文案里；Windows 腿在 192.168.0.5 真机验证通过（该机现状：HKLM 有 `MyNetTray`、HKCU 没有 —— 正是活体症状）。**残留**：reg 退出码分不清「访问被拒 / 值不存在」，一律当未设置 |
| D12 | P1：修真连接时同时删除 importConfig 成功路径的 EvStart/startTunnel。导入成功只刷新，不发 connect，也不隐式断开；运行中导入的 CLI 行为由 P3 核对，不凭 UI 自造规则 |
| D13 | **2026-10-16 改判（旧判「经 daemon command 面委托 CLI + 文件框在用户会话」作废）**：导出改为**内存 tar → 响应流**（`Content-Disposition: attachment`），新增专用端点，不接受 `-o`、不落盘；导入改为**浏览器上传 → 专用端点 → daemon 落盘**（现有「导入前自动备份」照用，root 写自己的 backupsDir，无暴露）；`allowedSubcommand` 收掉 `config export`/`config import`（CLI 的 `mynet config export -o` 保留给管理员在终端以 root 自己跑）；**托盘配置菜单整个删掉**。理由：① 包体只有几百字节（`cmd_config.go:44-47` 已排除可再生的 `driver/**` 投影），根本不是"文件落点/权限/交付"问题，把它当文件处理才凭空长出那三件事；② 本文 §3 D13 行早已取证「以登录用户身份写 root 目录 ⇒ 必然 permission denied」，托盘本地 exec 两个方向都不通（导出读不到 root 的 0600 私钥、导入写不进 root 的 0700 conf/；全仓零 `chown`，`PlatformDefaultHome`=`/opt/mynet` 且注释自陈「系统级唯一，不设用户级模式」）；③ 走通用命令面 + `-o` 反而是**恶化** —— `os.Create(out)`=`O_TRUNC` 且路径由未鉴权调用方给 ⇒ 本机任意进程可让 root 截断覆写任意文件（详见记忆 `project-localapi-command-plane-export-hole`）。**残留（不在本包）**：新端点与该 API 其余部分一样无调用方鉴权，属整条 localapi 的信任模型问题 |
| D15 | P0：control*.go daemon Controller 及 NewController/App.ctrl 死接线列为撤回；核对测试依赖后收敛，不重接到隧道控制 |
| D19 | P1：真实连接调用现有 config/connect 编排，断开按现有执行器合同；修复落在原 startTunnel/stopTunnel，不复制编排进托盘 |
| D20 | P0 明确保留 text.go；P3 接入状态/命令/配置结果、tooltip/通知等文本出口。中文私钥/密钥/口令/凭据加长hex注入不得泄漏，不信任上游已完全脱敏 |
| 发行缺件事实 §1.1 | P5 核对适用产物门禁；独立缺陷保留，不借此扩大托盘架构改造 |

**实施进度（2026-10-16）**：P0 完成 —— D15 撤回，`text.go` 保留（D20 的承重件）。
P1 完成 —— D1/D2/B3、D5/D6/D7、D12、D14、D17/D18、D19 已修并各有回归；
D4/D8/D20 的**渲染出口**部分顺带落了（`statusText` 是纯函数，不拆开反而要写两遍），
自启那条出口（`toggleAutoStart` 静默吞错）仍归 P3。D3 的显示差异已落，
但 B4 的 App 并发矩阵仍未证明 —— `-race` 现在绿是因为没有用例真并发，P2 才算数。
§5 的「9 个红测试」已随 T0 替代架构一并撤回，`make check` 当前全绿。
未验项照旧：写面没在真机执行过，托盘 UI 本体没实机跑过。

**续（2026-10-16 晚）**：P2 完成（`concurrency_test.go` 真实并发矩阵 + 3 条变异守卫）；
P3 完成（D9/D10/D11/D20 收尾 + D13 改判落地，14 条变异守卫，Windows 腿真机 5/5）；
P4 完成（状态图标三态、合并连接/断开、复制虚拟 IP、查看诊断深链、文案明确，
10 + 18 条变异守卫全 CAUGHT）。P5 完成 —— `journey_test.go` 一条 11 步用户旅程把
状态/按钮/图标/可复制 IP 四面串在同一条时间线上断言（4 条变异守卫），门禁全绿、
四条出包腿编译通过、Windows 真机 20 用例全 PASS、新构建托盘在本机真跑零报错。
**P0–P5 全部收口。** 提交：P0–P3 = `545622a`、P4 = `5f26af4`、P5 = 本次提交，**均未 push**。
唯一交不出的证据是**菜单栏观感** —— 本机 `screencapture` 被「屏幕录制」权限挡住，
图标与菜单排布只能由用户肉眼核对，已如实记为未验。
逐项 `file:line` 证据与三条附带发现（装机托盘落后 11 个提交、系统级 LaunchAgent
登记但未加载、`/ui/` 404 的根因是手工 build 覆盖掉 embed 构建）在
`TRAY_OPTIMIZATION_PLAYBOOK.md` §7 的对应行。

P2 的测试注入限定在现有 App：菜单事件 channel、渲染出口、探针/命令执行替身，以及必要的时间来源。无需启动原生 systray.Run；必须驱动真实点击/轮询/worker完成/FSM.On，不能用另一个测试用 reducer 或纯 FSM 测试证明 App 正确。

历史 fixture 中反映真实 API/CLI 故障的用例保留；只锁定旧新模型的测试改为现有 App/FSM 的等价行为用例。故意红测试仅用于修复过程，工作包交付应绿。D 表的真实缺陷不因调整架构而自动关闭。
