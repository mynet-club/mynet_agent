# 安装链路对账 — 进展与交接（2026-10-02 02:2x）

> **⚠️ 历史快照（2026-10-02 过程记录，非在世文档，勿按它施工）**：
> 文中指向的 `packaging/darwin/scripts/postinstall` 等路径**已退役** ——
> H2「薄壳委托」后安装器归 `mynet_desktop`，postinstall 委托链迁
> `mynet_desktop/macos/scripts/postinstall`，`make pkg-darwin` 已移除。
> 现行口径见 `MYNET_V1_PLAYBOOK.md` §5 与 `mynet_ctl/docs/knowledge-base/spec/lifecycle-contract.md`。
> 本文仅作追溯留档。

背景：用户睡前指令 —— 「测试时还在到处找工具二进制，说明安装器有问题」，点名
mac 装完无托盘、不自动开网页引导；macOS 通过后横向对齐 windows/linux/openwrt；
route.conf 注入已废弃。平台裁定：linux/openwrt=CLI；macos/windows=安装器+托盘+自动开 WebUI。

## 一、已诊断的三个安装链路缺口（对账结论）

1. **gnb「标准位」无人置备**：`$MYNET_HOME/driver/gnb/bin` 是 resolveGNBBinary 的第一优先位，
   但 darwin/linux 上没有任何代码写它（此前靠手工 cp）；且 pkg 落地位
   `/opt/mynet/driver/gnb/bin` 不在候选清单里 → 运行时只能沿 PATH 捡 `/usr/local/bin`。
2. **mac 装完无托盘**：mynet_tray 有二进制、有自带 autostart（`~/Library/LaunchAgents`，需用户手动开），
   但安装链路（install/postinstall）完全不注册托盘。
3. **装完不自动开 WebUI**：postinstall 无开页逻辑。

复核 route.conf：OS 路由 SSOT=bundle.network.routes 的注释链完整（cmd_apply.go:17、
apply_plan.go:20、proxy_apply.go:8），无注入残留；route.conf 仅作 gnb L5 地址投影
（缺文件 gnb 直接退出，必须保留）。外部命令盘点无隐藏工具依赖。

## 二、已落地改动（均编译+测试通过）

| 改动 | 文件 |
|---|---|
| 候选清单补 `/opt/mynet/driver/gnb/bin/gnb` | `mynet/driver/gnb.go` resolveGNBBinary、`mynet/plugin_proxy.go` findGnbBin |
| 2d 四件套置备标准位（sha 一致跳过、随包升级刷新；源=包内位→/usr/local/bin→--gnb-bin 同目录） | `mynet/cmd_install.go` provisionDriverGnbBin + gnbSuite + sameFile/hashFile |
| 托盘 LaunchAgent：写 `/Library/LaunchAgents/club.mynet.tray.plist`（Label 与 tray 自带 autostart 同名防双托盘；KeepAlive=false；日志 /dev/null 因 gui 用户写不了 /var/log/mynet），root 以控制台用户 `launchctl asuser <uid> bootstrap gui/<uid>` 当场拉起；uninstall 同步清理 | `mynet/cmd_install_launchd.go` installTrayAgent/consoleUserUID、writeLaunchdPlist 增 keepAlive 参数 |
| postinstall 装完轮询 8096（≤24s）后 `su <控制台用户> -c 'open http://127.0.0.1:8096/ui/'`，失败不致安装失败 | `packaging/darwin/scripts/postinstall` |
| dry-run/下一步提示、uninstall 提示同步 | `mynet/cmd_install.go` |
| 测试：置备幂等+随源刷新、KeepAlive=false、plist 形状 | `mynet/cmd_install_test.go` |

181 实机验证（热装 mynet + 真跑 install）：
- 三处 gnb 四件套 sha 全一致（home=pkg=/usr/local/bin，a2f571a6…=新 dist）
- `tray bootstrapped: gui/501/club.mynet.tray`，pgrep mynet_tray 在跑，`launchctl print` state=running
- WebUI 200，proxy status 主判据 `gnb Features: proxy（P2）`

## 三、顺带挖出并修复的重大断链：back_pub_hex 永远为空

现象：重装后 gnb 不被 mynetd 拉起。日志（自 09-30 起已出现 31 次，非本次引入）：

```
WARN 节点面未就绪 … keys 缺少: back_pub_hex
ERROR transport 启动失败 err="transport: NodeID 必填（寻址用）"
```

根因链：
- `mynet/cmd_config_face.go` 的 keys set / keys generate 调 `WriteNodeKeys(..., "")` 硬写空串；
- `mynet/setup_v2.go` 收了 `--back-pub` 却 `_ = backPub` 丢弃（node claim 取了 back 公钥传进来也白传）；
- `cmd/mynetd/nodeface.go` loadNodeIdentity 把 back_pub_hex 当硬依赖 → 节点面永不装配 → WS transport 不连 → darwin 上「mynetd 生命周期拉起 gnb」整条自动化死掉（此前隧道 RUNNING 是 CLI/手工 start 维持的假象）。

修复（契约式：判定成立才带内容）：
- `mynet/keys/nodekeys.go` 新增 `UpdateNodeMetaBackPub`（幂等、容错、不碰私钥面；附单测）；
- `cmd_config_face.go` 新增 `backFacePubFor`：在线模式 keys set/generate 时经 `fetchBackFacePub` 尽力拉取，拉不到只警告不阻断（sync 可回填）；
- `config_keys_sync.go`：`keys sync` 对存量空 meta 回填（老节点升级路径）；
- `setup_v2.go`：`--back-pub` 给了就回填 meta（claim 链路闭环）。

181 实测：`keys sync` → `back_pub_backfilled: true` → kickstart mynetd →
`节点面签名就绪` → `transport: connected … as node 3764021915150630` → gnb running。

## 四、遗留（按优先级）

1. **#20 剩余**：Windows 实机回归（reg Run 自启 + 自动开页 + sc 服务），等有
   Windows 测试机时做。macOS pkg 回归已于 10-02 08:1x 全量通过（见 §六）。
2. 更大背景（非本轮）：gnb-dist.sh HEAD 基线漂移待 1.6.9 重基修；Windows MSI
   本体、签名公证待做；B1 脏工作树门禁历史红。

## 五、任务面板状态

- #19 back_pub_hex 断链修复 — **已完成并经 181 实测闭环**（transport 连上、gnb 自动拉起）
- #17 Windows 安装链路对齐 — **代码级完成**：SCM 服务+失败重启、HKLM Run 托盘自启
  （`-home` 显式对齐）、装完自动开页、`mynet uninstall` 清服务+Run 键+env；
  `GOOS=windows` build/vet 全绿；实机回归并入 #20
- #18 CLI 平台口径与系统脚本复核 — **已完成**：
  - 四平台 `packaging/*/INSTALL.md` 按新矩阵改写：删 webui.html 落位步骤（embed 构建，
    `mynet/upgrade/install.go:32` 早已注明不落盘）；gnb 手工 cp 简化为「只放 /usr/local/bin，
    标准位由 install 2d 自动置备」（openwrt 走 procd 独立分支不置备，注明手工落标准位）；
    补 keys sync 回填 back_pub_hex 说明与「节点面签名就绪 / transport: connected」验收判据；
    补 linux/openwrt=CLI 无托盘、mac/win=安装器+托盘+自动开页形态矩阵；
  - `mynet/units/openwrt_mynet.init` 删除 ROUTE_CONFIG 死变量（赋值后无消费方），
    注明 OS 路由 SSOT=bundle.network.routes、唯一写入者 `mynet apply --auto`；
  - 复核结论：`scripts/check-route-writers.sh` 门禁 rc=0；e2e 脚本里的 route.conf
    引用均为 L5 投影存在性断言（合法）；`config sync` 子命令确认存在（pull+apply 一条龙）；
  - 全量 `go build ./... && go test ./mynet/... ./cmd/...` 绿。
- #20 单独出包回归 — **macOS 已全量通过（10-02 08:15，含 #21 修复版）**：
  `make pkg-darwin v0.0.6-pkg` → `sudo installer` → 三查全过（版本/基线/补丁、
  gnb sha `a2f571a6…` 三处一致、tray pid 6507 running、WebUI 200、
  08:15:22 `节点面签名就绪`+`transport: connected`、gnb 拉起、peer 表收敛）；
  **新增判据**：装后 `route -n get default` 仍为 192.168.0.1/en0、
  `mynet apply --active --auto --dry-run` 输出 0 条 default 命令。
- #21 主表默认路由劫持修复 — **已完成并经 181 实测闭环**，见 §六。

## 六、#21：macOS 主表默认路由劫持（「装完/同步后掉默认路由」根因）

现象（用户报「又把系统默认路由干掉了」，复发）：07:46 那次 pkg 装完的
bundle_changed 落地链 apply 之后，IPv4 默认路由整体消失，只剩隧道黑洞。

根因链（dry-run 实锤：`proxy apply cmds=18` 中含
`route -n delete -net default` + `route -n add -net default 10.20.0.250`）：
- `proxy_apply.go` proxyClientCmds 的 BSD 分支把 Linux 的「table N 里
  replace default」直译成 BSD 方言 —— 而 `route delete -net default` 不带网关
  会删掉**全部** IPv4 默认路由（BSD 无语义区分），随后 add 经隧道 VIP 失败即断网；
- macOS 没有策略路由表，fwmark+table 语义**无对应物**，正确契约本就是
  「只靠 DNS/域名清单把命中项 /32 钉隧道 + dns-split，绝不碰主表 default」；
- 伴生断链两处：读取侧 `planFromBundle` 只认 bundle 不消费 intent（本地关闭
  被每次 sync 复活，正是触发重放危险命令的源头）；`proxyCleanupCmds` 零调用方
  （禁用后 mac 的国外 DNS /32 钉扎永久残留 → 隧道消失后整机 DNS 黑洞）。

修复（四项，均落测试）：
- **A** `proxyClientCmds` BSD 分支改发占位 no-op，不再触碰 default；
  `FullTunnelCmds(snap,…)` 加 BSD 门禁（Linux 独有能力）。
- **B** `proxy_unify.go` 新增 `StampCloudIntent`：bundle 下发落 intent Cloud 轨，
  `planFromBundle` 改为 intent 有记录时**只认 Effective()**。防复活两道闸
  （实测 generation 是 `rev-…` 非数字串，解析恒为 0）：①Cloud 轨内容与本次
  下发一致 → 跳过重 stamp（否则自增 rev 每次 sync 越过本地覆盖）；
  ②revision<=0 且本地 Dirty → 云端无法证明更新，保持现状不覆盖。
- **C** `executeApply` 接通清理：plan.Proxy 为空但 intent 有记录 → 跑
  `proxyCleanupCmds(snap,…)`（方言对称撤 /32 钉扎，Linux 另撤 fwmark rule+table，
  全部 optional 幂等）。
- **D** 测试：旧签名更新；新增 `TestProxyBSDNeverTouchesMainDefault`
  （含 FullTunnel/DoH-tunnel 也不得出 default）、BSD cleanup 形状断言、
  `TestPlanFromBundleLocalDisabledSuppressesCloud`（含云端内容变更无版本号仍压制）。

181 实测：热装修复版 → apply 撤净 7 条 DNS 钉扎、default 保持 192.168.0.1 →
重打 pkg 安装后 dry-run 0 条 default 命令 → 全量 `go test ./...` 绿 +
`GOOS=windows` build/vet 绿。
