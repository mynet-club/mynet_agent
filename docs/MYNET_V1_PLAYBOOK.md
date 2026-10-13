# MyNet v1 产品交付 Playbook

## 0. 基线裁定（2026-10-03 / 10-04）

本手册写的是**理想基线**，而仓库已经在飞。2026-10-03 对五仓做了一轮基线审计，
用户逐条裁定如下。后面各节凡与这里冲突的**以这里为准**（各节已同步改过；
本节保留裁定的理由与证据，免得下次又被当成缺口重做）。

### 0.1 「五仓」是契约口径，交付口径是七仓

手册通篇说「五仓」，是**有意**排除 `mynet_ctl` 与 `mynet_desktop`——它们与
核心业务结构和架构流程关系不大。但真实交付离不开这两仓：出包发行面在
`mynet_ctl`（`ReleaseDomainService` + `/d/` 分发），图形化安装面在
`mynet_desktop`（pkg / dmg / NSIS）。

**怎么用：** 谈领域契约、架构、数据流按五仓；谈 M0/M6 发布闸门和真实交付
按七仓。§3、§10 已按此标注。

### 0.2 P0 不是绿地，是已完成资产

P0（领域合同 + fixtures）**已经做完**，不要再当新建工作排期：

- `BundleEnvelope` 两侧都有：`mynet_api/app/Modules/VPN/Domain/Bundle/BundleEnvelope.php`
  与 `mynet_agent/shared/domain/bundle_envelope.go`。
- 驱动抽象已含双数据面：`mynet_agent/shared/domain/vpn_driver.go` 定义
  `DriverTypeGNB = "gnb"` / `DriverTypeWireGuard = "wireguard"`。
- `mynet_agent/mynet/driver/` 下 `gnb/` 与 `wireguard/` 两套实现都在，并带
  `intent_fixtures_test.go`、`a8_dual_verify_test.go`、`gnb_conf_render_test.go`
  等契约测试。

**怎么用：** P0/P2 按「审计 + 补冻结」推进，不重写。

### 0.3 NodePool ≈ IPPool 成立（已在跑，不是缺口）

`mynet_api/app/Modules/VPN/Models/IPPool.php` 的 `IPS_PER_GROUP = 6` 就是
§4.3 说的「默认六个 IP/节点额度」，模型带 `BelongsToTenant`。搜不到
`NodePool` 这个名字**不等于**节点池没实现——这正是 §2 比对方法要防的误判。

真实缺口只有连续 IP 与配额计费两项，都被 0.4 裁定为不进 v1。

### 0.4 连续 IP 与「购买更多池」配额：不进 v1

裁定理由：这块此前已经实现，只是没接配额计费与商业逻辑。但代码事实要分两层
看，别记成一句「已实现」：

| 层 | 状态 | 证据 |
|---|---|---|
| 组内 6 IP 天然连续 | **已实现且在跑** | `IPPool::calculateIPRangeFromSequence()` 算出 start = `10.x.y.(START_OFFSET + groupInCClass*6)`、end = start+5；`getIPAddressGroup()` 的连续分支返回 `allocation_mode='sequential'` |
| 跨组连续（`groupCount>1` 搜连续空隙） | **有意未迁移** | 旧仓 `mynet_main` 有 292 行实现（findConsecutiveAfter / Before / InGaps / Anywhere）；新仓显式 `Log::warning('IP 池连续分配未实现，降级为逐个分配')`，返回空 ips + `allocation_mode='random'` |

⚠️ **看到 `allocation_mode='random'` 不要当故障查**——那是设计好的降级路径，
不是 bug。真要补，等确有企业租户需要连续段再排期。

**边界别读歪：** 出局的是「购买更多池」的**计费/商业逻辑**和**跨组连续分配**。
配额的**执行**（租户/用户可有几个池、几个节点，超限拒绝）仍在 v1 内，
§7 P1 的「用户和租户配额」测试和 §12 DoD 的「限制节点/节点池配额」都照做。

### 0.5 Zone 彻底删干净（跨仓，不是清注释）

用户原话「干净，可以删掉」。⚠️ 这是**双仓联动**：`mynet_api` 侧
`TenantController` 的 `zone_*` 响应字段是对齐 `mynet_gui`
`useTenants.getTenantStatistics()` / `fetchOverview()` 的契约（第 1 步改名前的旧名是
`useZones.*`），删字段必须同步改 GUI。实测范围（2026-10-04，改名前）：`mynet_gui/src` 下
46 个文件提到 zone，`mynet_api` 的 app/database/routes 下 9 个。

**2026-10-04 取证后，这个条目的性质变了 —— 别再当「删一个业务对象」执行。**
详见决策包 `mynet_ctl/docs/knowledge-base/spec/zone-removal.md`（**已裁决**：议题 1 = A 改名保留，
议题 2 = B 改指真实平台租户 `9007199254740991`，追加议题 3 = 界面中文叫**「网络区」**且必须是可替换的
单点变量 `mynet_gui/src/config/terms.ts` 的 `TERM_ZONE`；用户原话「zone 就是 tenant，防火墙那个 zone
是另外一回事，从这个上来看，确实不能粗暴说删 zone」，以及「『租户』太技术腔……先叫网络区，用个变量名
替代吧，这样我们后续想到好词可以替换」，第二轮追加「**都统一吧，能否也设置为变量**」；
同日第三轮追加三条：① **用户面不表达 tenant 权限/边界**（「user 是 user, operator 是 operator……
不应该对前端用户产生类似的权限/边界判断，用户无感 tenant 的存在」「GUI = 普通用户前端」
「收敛该收敛的吧」）② LocalStorage `current_zone` **改名并迁移**，不许静默丢用户已选网络区
③ **en-US 用词 = Network**。**四步已全部落地**，未决只剩 7 条，见决策包末尾）。
⚠️ 用词分三层，别再混：**中文界面 = 网络区**（只从取词常量拿值）、
**代码标识符 = Tenant**、**接口 wire 字段 = `tenant_*`**（第 2 步已翻完，2026-10-04
`mynet_api 5aa8f34` + `mynet_gui 716f431`；翻之前那轮「不许单边改」的约束已经用完了）。
中文界面这一层现在有**四个**取词单点，换词四处一起改：GUI `terms.ts` 的 `TERM_ZONE`、
`mynet_api/config/mynet.php` 的 `terms.zone`（调用 `term_zone()`）、本仓节点 WebUI
`mynetd/localapi/webui/index.html` 内联的 `TERM`、本仓 CLI `mynet/cmd_node.go` 的 `termZone`
（`mynet/commands.go` 的 `usage` 帮助文案同样拼 `termZone`，别漏 —— 首轮就漏了这行，`9223eeb` 补）。
节点侧改动要 `make dist` 重出包、经 `mynet_desktop` 安装器交付才现网可见（`make pkg-darwin` 已随 `packaging/darwin` 退役）；本轮产物是 `v0.0.7-pkg`。
三条必须先知道的事实：

1. **Zone 在代码里已经不是业务对象，只剩一层名字。** 服务端早就 tenant-native
   （`Routes/api.php` 的 `tenants` CRUD + `user/tenants` 切换），GUI 的
   `getZones`/`switchZone`/`zoneService` 打的全是 `/user/tenants`、`/tenants`。
   所以任务是**改名**（机械但面宽），不是重新设计作用域模型。
2. **`grep -i zone` 全删会抹掉现网知识。** API 侧 22 行里只有 6 行是真响应字段名
   （另 5 行是配套注释），**7 行说的是另一个 zone** —— 一台主机跑多个 gnb 实例、
   每实例有自己的 tun 接口和防火墙 zone，这是现网 182 的实测结论
   （`BundleEnvelopePublisher` / `NodeConfig` 的注释），与产品 Zone 无关。还有 1 行是
   `timezone` 误报。
3. **单方面改 API 字段名会静默断掉全站 WebSocket。** GUI 的 `boot/websocket.ts`
   拿不到 `zone_id` 就直接 `return`，不报错不提示；而 `services/websocket.ts`
   正是把这个值当 `X-Tenant-ID` 头发给 broadcasting auth。**api 与 gui 必须同批改、
   同批发**，不允许存在只改一边的中间态。
   （✅ 这条已于第 2 步消解：三个注入点 `api.ts:61/115/140`、`boot/websocket.ts:16`、
   `TokenDebugPage.vue:212` 现在**全部只读 `authStore.user?.tenant_id`**，
   写者只有 `stores/auth.ts` 一个；缺值也不再静默 return，改成带字段的 `debug.warn`。
   但它仍是全站最脆的一条 —— GUI 仓只有类型棘轮（`npm run check:types`，基线 194 错/38 文件、只降不升），字段名写错不保证被编译器拦住。）

（以上取证写于裁决前，文件当时还叫 `pages/zone/ZoneListPage.vue` 等；第 1 步改名后的
现名见 `spec/zone-removal.md` 的「旧名对照」。）

**裁定结果（2026-10-04，现行口径）**：`ZoneListPage`/`ZoneSharingPage`/`/zones`/`ZoneSelector`
**改名保留**；`DEFAULT_GLOBAL_ZONE` 硬编码的假租户 `999999999` **改指真实平台租户
`9007199254740991`**；界面中文用词定为**「网络区」**，且必须是单点可替换变量。
**`GUI = 普通用户前端`**：租户 CRUD / overview / 统计已从用户面删除（`mynet_gui 716f431`），
`/tenants` 页面只做「列归属 + 切当前」，管理面走 console（轨道 G）；`pivot.is_admin: 1`
权限语义随「用户面无权限表达」取消。未处置项 7 条逐条见 `spec/zone-removal.md`
「第 2 步留下的未处置项」。

### 0.6 mynet_gnb 布局迁移已收口（2026-10-04）

审计时报的偏差是：该仓是 bare 仓、分支停在 `codex/upstream-layout`、`src/`
已含补丁（与「`src/` 保持上游只读」矛盾）。当时裁定「v1 期间不追」。

**现已收敛，不再是待办**：2026-10-04 统一到 `main` 单线，
`codex/upstream-layout` 等 7 条非 main 引用已删（本地与 GitHub），旧线内容
归档为三个**仅本地**的 `archive/*` tag（含历史敏感材料，永远不要推远端）。
B1 红线（`src/` 只读）改为按 `--absolute-git-dir` 判仓后，bare + linked
worktree 也算，这条红线在 gnb 上才第一次真的生效。

## 1. 定位与边界

**定位句：** MyNet 让用户加入一个隔离的企业级租户网络空间，在租户规则内管理自己的节点和节点池，并自动连接获准的节点、实体网络和网络服务。

首个参考场景是一个小企业：管理员创建租户，邀请员工，员工注册自己的设备；企业允许自动组网时，获准节点自动生成数据面配置和路由。管理员也可以部署 OpenWrt peer 网关，把办公室、家庭或分支网段接入；租户可发布 DNS、出口和其他服务。

支持：

- 企业租户、邀请码邀请、成员和角色
- 用户在租户内拥有多个节点池；默认节点池六个 IP/节点额度
- GNB 和 WireGuard 共存；跨数据面由双数据面转发节点连接
- 节点对节点、节点对网络、网络对网络
- GNB/WireGuard 配置、密钥备份/恢复、状态和诊断
- 普通设备 agent 和弱 OpenWrt LuCI 轻量模式
- gateway 就近接入和骨干线路转发
- 当前 Proxy 路径转发/分流能力

明确排除 v1：

- UTC 实际实现和 UTC 服务
- 用户贡献 Proxy/UTC 节点、市场、结算和收益规则
- 大规模运营商网络优化
- 自研复杂自动选路
- 把 Zone 作为业务对象（存量字段一并删净，见 §0.5）
- “个人租户”或“朋友租户”模型
- 强制一个租户或私网只使用一种数据面
- 跨组连续 IP 分配，以及「购买更多节点池」的配额计费/商业逻辑（裁定与代码事实分层见 §0.4）

## 2. 当前实现与目标需求的比对方法

产品术语和代码术语允许不同。`NodePool` 是 v1 的产品概念，现有实现中的 `Ippool`/`ip pool` 是同一能力的代码名称；在没有行为证据表明两者边界不同之前，不得因为搜索不到 `NodePool` 就判定节点池未实现，也不得直接改名造成迁移风险。

这条规则已经用过一次并成立：`IPPool.php` 的 `IPS_PER_GROUP = 6` 就是 §4.3 的默认额度，模型带 `BelongsToTenant`，组内 6 IP 的连续分配也在跑（证据见 §0.3、§0.4）。**不要重做这个核对。**

每项 v1 能力必须通过“概念—现有实现—目标行为—差距—证据”表核对：

| 产品概念 | 现有实现可能使用的名称 | 先核对的行为 | 结论证据 |
| --- | --- | --- | --- |
| 租户成员和邀请 | tenant、invitation、operator tenant、social invite | 加入、过期、撤销、权限和隔离 | API Feature tests、迁移、真实请求 |
| NodePool | Ippool、IP pool、allocation、quota | IP 分配、归属、配额、并发、释放和恢复 | Model/Service、数据库、API 测试 |
| 节点 | node、peer、device、node key | `node_id` 身份、密钥轮换、租户归属和状态 | API/agent bundle、节点测试 |
| 实体网络网关 | gateway、forward、backhaul、route source | 网段回源、节点内转发、回程和清理 | agent/OpenWrt 脚本、真实联通测试 |
| 路径加速 Proxy | proxy、route policy、出口、分流 | 出口选择、DNS、策略路由、停止清理 | `proxy.lua`、route policy、E2E |
| 自动组网 | topology、peer sync、route planner、bundle | 成员范围、路由数量、配置生成和容量 | planner、bundle、route.conf 产物 |

审计顺序必须是：

1. 读入口和调用链，确认功能是否可达。
2. 读数据模型、迁移和序列化，确认身份、归属和生命周期。
3. 读执行路径，确认配置是否真的写入并应用。
4. 读正向、拒绝、恢复和并发测试。
5. 做最小真实请求或设备实验，确认文档描述与运行行为一致。
6. 最后才使用字面量搜索补充遗漏，不能把搜索结果当作完成或缺失的证明。

每个工作包交付前必须填写实现比对表，标明“已实现、部分实现、未实现、名称不同但行为已实现、行为存在但缺测试”。名称迁移只有在行为和数据迁移方案明确后才进行；产品文案可以先使用 NodePool，内部 API/数据库保持 `Ippool` 兼容。

## 3. 当前基线与保留行为

### 仓库职责

下列五仓是**契约口径**——领域合同、架构、数据流只谈这五个。

- `mynet_api`：租户、用户、邀请、节点、节点池、路由、监控、服务、命令和权限的业务权威。
- `mynet_gui`：**用户交互前端**（社区域，服务后端，`my.mynet.club`）；隐藏驱动术语，展示节点、连接、网络、服务和密钥操作。管理面走 console（`mynet_api` 轨道 G），不进 GUI。
- `mynet_agent`：节点通信和本地执行；`mynetd` 纯通信，`mynet` 执行系统和驱动操作，`gateway` 只转发。
- `mynet_openwrt`：LuCI 原生壳、ubus/rpcd/ACL、弱设备执行适配，不形成第二套后端。
- `mynet_gnb`：已落地的 GNB 底层网络引擎、补丁和多平台构建发布基线；v1 只做版本锁定、产物回归和跨仓集成，`src/` 保持上游只读（布局迁移已于 2026-10-04 收口到 `main` 单线，见 §0.6）。

另有两仓是**交付口径**，M0/M6 发布闸门和真实交付必须算进去（见 §0.1）：

- `mynet_ctl`：出包发行面——`ReleaseDomainService`、`/d/` 分发、平台枚举与产物命名、部署脚本，以及知识库 SSOT（`docs/knowledge-base/`）。
- `mynet_desktop`：图形化安装面——macOS pkg/dmg、Windows NSIS、launchd plist 与安装器 CI。

`mynet_utc` 挂载不动（v1 排除 UTC）。`mynet_web` 是**官网 / 项目主页**（`www.mynet.club`）——D-4 的「合并 mynet_ui + mynet_gui 为一份 WebUI」**已作废**（2026-09-24 裁定），用户交互前端是 `mynet_gui`（`src/` 200+ 文件、真实产品页）。改 GUI 前先确认改的是哪一仓——两边同名概念不等价。

### 必须保留的硬约束

- `mynetd` 不依赖系统能力；系统写入全部走执行器。
- gateway 不做租户和用户鉴权；relay 只做路径白名单。
- 业务对象不泄漏 `gnb`、`wireguard`、`AllowedIPs`、`node.conf` 等驱动术语。
- 私钥不进入普通响应、邮件和日志；`back_priv` 保护级别高于节点私钥。
- GNB 和 WireGuard 驱动目录、密钥布局和协议语义按各自生态处理。
- 发布 WebUI 使用 `embed`；开发使用 `dev`；OpenWrt 瘦构建由 LuCI 托管。
- 本机 API/WebUI 端口 8096；gateway 默认 8710。

### 当前改动和基线处理

现有 `Ippool` 实现应作为 NodePool 能力的首个核对对象；先验证其 IP 池、分配、配额和租户归属行为，再决定哪些是补齐功能、哪些只是术语映射。

开始任何 v1 工作前分别记录五仓 `git status`。现有未提交改动必须先归类为：

1. v1 必须保留并纳入测试；
2. v1 无关，暂不触碰；
3. 未知行为，先建立测试再决定。

不得把当前工作区未提交文件直接当作已验证功能。跨仓变更必须记录依赖和回滚顺序。

> 这一轮已于 2026-10-04 做完：八个仓共 25 个提交，逐个按**显式路径**入暂存（不用 `git add -A`），入库前先扫敏感文件名/PEM/凭据文本。三件**故意留白**待用户裁定：`mynet_api/database/sql/migrate-tenant-to-platform.sql`（破坏性 + 属 DBA 交接路径，提交即等于排队一次生产数据操作）、`mynet_utc` Phase 7 全部产品代码（挂载不动）、`mynet_gui` 里 12 个被跟踪的 `.quasar` 生成物（`.gitignore` 从未提到 `.quasar`，是策略问题不单方改）。
>
> ✅ 门禁缺口已于 2026-10-04 收口：`mynet_gui` 装上守护链（`Makefile` + `.githooks/`，`core.hooksPath=.githooks`），七仓红线不设防的最后一处补齐。两处实测细节：
> - 该仓四个 `.env*`（`.env`/`.env.dev`/`.env.embed`/`.env.prod`）**必须入库**（Quasar 按固定文件名查找 env，改名即失效），里面只有 API 域名与 Reverb 的**客户端公开值**，但 A3 只按**文件名**拦 → 每次编辑都撞一次红线。给 A3 加了**文件级豁免判据**（① 已在 `HEAD` ② 文件内带 `GUARD-OK <理由>`，两条同时成立才降级为 ⚠；**新增**敏感名文件即使带标注仍硬拦），不放宽红线也不留 `--no-verify` 的口子。判据全文见 AGENTS.md §2.6，自测见 `templates/.githooks/guard_test.sh`（45 用例，实测输出 `guard 自测：45 通过 / 0 失败`）。
> - `mynet_gui/Makefile` 是 Vue 适配版：`lint` = `eslint` + `check:i18n`，`test` = `vitest run`，且**自己数套件**——零用例时打印 `⚠⚠ 未执行任何测试` 并按 M0 记未达标，不用 `--passWithNoTests` 装作跑过。

## 4. 冻结的领域合同

### 4.1 Tenant

- `tenant_id` 是唯一稳定标识；所有成员、节点、节点池、服务、策略和审计记录带租户范围。
- 租户是企业级治理和隔离空间；没有个人租户和朋友租户。
- 生命周期：创建、邀请开放/关闭、冻结、归档；删除须有备份和明确的级联策略。
- 租户管理员决定成员、配额、自动组网、转发、服务发布和协议能力。
- 跨租户通信默认拒绝；好友关系和邀请码不自动创建跨租户数据面。

### 4.2 User/Member/Invitation

- 用户是身份；成员是用户在某个租户中的身份和角色。
- 注册可要求邀请码；接受邀请即加入邀请所属租户。
- 邀请码必须有：租户、创建者、状态、过期时间、使用次数/上限、审计记录。
- 撤销邀请不删除已加入成员；移除成员必须撤销其会话、节点授权和服务访问。
- 好友是用户级关系；好友关系不等于租户成员关系，也不等于节点互通。

### 4.3 NodePool

代码名是 `IPPool`，不要改名（§0.3）。

- 节点池属于租户范围内的用户资源，不是实体网络和不是租户。
- 默认六个 IP/节点额度（`IPS_PER_GROUP = 6`，已实现在跑）；购买更多节点池属于配额变化，不能绕过租户策略——**配额计费不进 v1**（§0.4）。
- 节点池拥有稳定 ID、地址分配记录、所有者成员、配额、数据面能力和生命周期。
- 节点池可包含用户自己的 MyNet 节点；朋友的实验室或办公室网络不属于节点池。
- 连续 IP 是高级资源能力，不改变节点所有权和租户边界——**跨组连续分配不进 v1**；组内 6 IP 的天然连续已实现（§0.4）。

### 4.4 Node

- `node_id` 是全局唯一且稳定的节点身份；`tenant_id` 记录节点归属和隔离范围。设备密钥是可轮换、可恢复的节点凭证，不是节点身份；设备名称只用于展示。
- 节点声明能力：数据面驱动、是否支持 GNB、是否支持 WireGuard、是否支持转发、回源、出口和服务。
- 节点生命周期：申请、注册、待验证、在线、离线、撤销、恢复。
- 仅节点所有者和租户授权角色可读取节点配置；私钥按最小暴露原则处理。

### 4.5 Connection/Route/Forwarding

- 连接描述通信意图和授权范围；不直接暴露驱动配置。
- 路由描述前缀、方向、下一跳/转发节点和来源版本。
- 实体网络通过其所有者部署的 peer 网关接入；访问者取得访问授权，不取得实体网络所有权。
- 节点到节点、节点到网络、网络到网络都必须有可解释的下一跳、回程和权限。
- 跨 GNB/WireGuard 必须使用同时持有两个数据面接口的转发节点；普通单协议节点不得伪装成跨协议网关。

### 4.6 Service/Acceleration

- 服务可由租户节点或实体网络网关发布；记录类型、提供节点、目标地址、访问范围、方向、健康状态和版本。
- DNS、企业内部服务和出口服务属于网络服务。
- Proxy 是当前 v1 的路径转发/分流实现，产品界面可统一称为“加速”或“网络出口”，但必须向高级用户说明路径和出口变化。
- 协议加速 UTC 仍是未来能力；用户贡献节点提供 Proxy/UTC 是未来构思，不进入 v1 完成条件。

### 4.7 自动组网

- 自动组网是租户管理员为认证企业网络启用的能力，不是所有开放租户的默认全互通。
- “自己的节点默认互通”与“企业自动组网”分开建模。
- 企业自动组网可以自动登记节点、生成配置、注册路由和下发状态，但必须有规模上限。
- 上限以实测的节点数、连接数、`route.conf` 条目、bundle 大小、设备承载和分发耗时确定；未实测前不得承诺数字。
- 超限时 fail-closed：阻止扩大自动生成范围并返回可操作错误，不生成半成品配置。

## 5. 架构和数据流

节点上的 GUI、LuCI 和命令行都通过本机 `mynetd` 进入 MyNet 控制链路；它们不直接连接 `mynet_api`。

用户侧请求路径：

```text
桌面 GUI / OpenWrt LuCI / mynet CLI
              │ 本机 HTTP/Unix socket/本地命令
              ▼
          mynetd（本地通信入口）
              │ 节点身份、WS、relay
              ▼
          gateway（边缘接入和转发）
              │ 验证后的节点面/用户面请求
              ▼
          mynet_api（租户、成员、策略、配置和命令权威）
```

控制命令和配置下发走反向路径：

```text
mynet_api
   ▼
gateway
   ▼
mynetd
   ▼
本地执行器（mynet / OpenWrt Lua）
   ├── 系统路由、防火墙、转发、诊断
   └── GNB / WireGuard 数据面
```

状态、心跳和执行结果同样由节点经 `mynetd → gateway → mynet_api` 上报。gateway 维护连接和转发，不拥有租户业务权威；API 负责用户会话、租户隔离、节点身份、授权、命令和审计。

API 生成协议无关的网络意图和版本化 bundle。agent 校验 bundle、选择对应驱动、写入驱动目录、应用系统路由与转发，并上报结果。GUI/LuCI 展示产品对象和操作结果，不直接生成或编辑 `node.conf`、`route.conf` 或 WireGuard 原生配置。

## 6. 迁移与行为决策

### 租户/Zone

- `Zone` 从产品和业务模型移除；不做兼容的第二套 Zone 语义。**彻底删干净**（用户原话「干净，可以删掉」）。
- 旧数据若含 Zone，先做一次性映射到租户、节点池或路由范围；迁移前备份，验证后再删除旧字段。
- 不允许新 API、GUI 或 agent 继续创建 Zone。

⚠️ **这是双仓联动，不是清注释。** `mynet_api` 侧 `TenantController` 的 `zone_*` 响应字段是 `mynet_gui` `useTenants.getTenantStatistics()` / `fetchOverview()` 的契约（第 1 步改名前的旧名是 `useZones.*`）；只删 API 字段会直接打穿 GUI。实测范围（改名前）：`mynet_gui/src` 46 个文件、`mynet_api` 的 app/database/routes 下 9 个文件。动手前先按 §0.5 重新量一次（`mynet_web` 骨架里另有 1 处，别漏）。
✅ **2026-10-04 状态**：本条目标已由用户改判为「改名保留 + 界面叫网络区」，不再是「彻底删除 Zone 语义」；**四步已全部落地**（第 2 步契约翻转 = `mynet_api 5aa8f34` + `mynet_gui 716f431`），进度与 7 条未决见 §0.5 与 `spec/zone-removal.md`。
⚠️ 上面那句「`zone_*` 响应字段是 `useTenants.getTenantStatistics()` / `fetchOverview()` 的契约」**已随第 2 步作废** —— 这两个方法连同租户 overview/统计/CRUD 一起按「用户无感 tenant」裁定从 GUI 删掉了，wire 也已翻成 `tenant_*`。留着这段是为了说明「为什么当初不能只删 API 字段」，别把它当现状读。

### 单协议到双数据面

- 采用 additive driver 维度，不替换租户或节点主键。
- 现有 GNB 配置保持可读和可回滚；新增 WireGuard 字段和驱动 intent 必须版本化。
- bundle 明确 `schema_version`、`driver`、generation、node identity 和 checksum。
- 未知 schema/driver/能力 fail-closed，不覆盖当前可用配置。

### 自动组网

- 先 shadow 计算连接和 route 数量，不下发；记录预计配置大小和设备能力。
- 在单个认证企业租户 scoped enforce；通过一轮真实节点、回滚和容量证据后才扩大范围。
- 回滚使用上一份已验证 bundle 和上一代路由；新配置应用失败不得清除旧配置。

### Proxy

- Proxy 当前实现可进入 v1，但“用户贡献 Proxy 节点”只保留接口和未来合同，不实现市场、结算和自动调度。
- Proxy 策略必须明确目标范围、出口节点、DNS 模式、转发状态和停止时的清理动作。
- 失败默认 fail-closed，避免意外把流量泄漏到错误出口；诊断必须区分服务失败、路由失败和出口不可达。

### 密钥和备份

- 密钥迁移必须先备份并校验，再写入；失败停止，不删除旧凭证。
- 私钥只在明确的配置下发/恢复流程可见；日志、监控、普通查询和错误信息全部脱敏。
- 恢复包必须包含协议、节点身份、版本和 checksum；恢复前校验租户和节点归属。

## 7. 工作包

### P0：领域合同和 fixtures（先做）

**状态：已完成资产，不是绿地（§0.2）。** 按「审计 + 补冻结」推进，不要重写。

**目标：** 固定租户、成员、邀请、节点池、节点、连接、路由、转发、服务、bundle 和稳定错误码。

**已落地（先核对再动手）：** `BundleEnvelope` 两侧齐（api `Domain/Bundle/BundleEnvelope.php` ↔ agent `shared/domain/bundle_envelope.go`）；`shared/domain/vpn_driver.go` 已含 `DriverTypeGNB` / `DriverTypeWireGuard`；`mynet/driver/{gnb,wireguard}` 两套实现带 `intent_fixtures_test.go` / `a8_dual_verify_test.go` / `gnb_conf_render_test.go`。

**负责模块：** `mynet_api` DTO/Request/Domain、`mynet_agent/shared/domain`、共享 fixtures、GUI 类型。

**必须交付：** JSON schema/示例、版本字段、错误码、权限矩阵、GNB/WireGuard 双驱动 fixture、密钥脱敏测试。真实缺口是权限矩阵与稳定错误码清单，不是 envelope 本身。

**缺口落地（2026-10-04，两份 P0 冻结件已成文）：**
- **权限矩阵** → `mynet_ctl/docs/knowledge-base/spec/v1-permission-matrix.md`：R1–R6 边界规则 +
  六个表面（公开/登录/节点/用户/console/本机 + LuCI）× 操作域的判据矩阵，每条带 `file:line`。
  审计出 **10 条洞**（跨用户写 `PUT /users/{id}`、`GET /users` 返回全站列表、悬空 `DELETE /users/{id}`→500、
  客户端 `X-Tenant-ID` 在用户面**不经归属校验**即被 20+ 调用点直采 等），逐条分级后
  进决策包 **F-22 / F-23 / F-24 / F-27 / F-28**；只有 #8（GUI 残留 `admin_name`，沿用 10-04「用户面不表达 tenant 权限」
  既有裁定）是能立刻执行的。**#4 是本组唯一带真实跨租户读风险的洞**，且它**全仓测试抓不到**
  —— `tests/TestCase.php:73` 把 `IdentifyTenant` 只推进测试态的 api 组（同 dev-plan **B-6**），
  套件里那批「先补 `tenant_users` active 记录才敢发头」的用例测的是生产根本不跑的护栏。
  **2026-10-04 这批洞对应的七条（F-22 ~ F-28）已逐项裁定完毕**，裁定表在
  `mynet_ctl/docs/knowledge-base/dev-plan.md`「2026-10-04 逐项裁定」。取向四句话：
  **属主判定层建在后端**（Laravel Gate/Policy，写成可被框架同名方法替换的接线点，同时把「框架出属主/归属判定方法、
  下层调用、业务继承+扩容」列为对框架的第 13 条诉求）；**当前区只认服务端指针 `users.tenant_id`**
  （实测「当前区」在后端是三条并存互不相识的路，切区即数据错位）；**用户面 `/users/{id}` 整组下架**并同批改 OpenAPI；
  **权益只做租户级 `subscription_plan`**，用户级 `level`/`score`/`right` 不进 v1 合同（库里根本没有那三列）。
- **端别判定（同日追加，回答「用户面可操作没鉴权是哪一端的问题」）** → 矩阵 §4-10 + 决策包 **F-28**：
  要分清两种鉴权 —— **租户归属**（我是不是这个区的成员）vs **资源属主**（这条路由/节点/池是不是我的）。
  框架**只给前者、且刻意不给后者**：`IdentifyTenant::resolveWithOwnershipCheck()` 查的是 `tenant_users`/`operator_tenants`
  成员关系（输入里没有资源），`RbacService::check()` 对非 Operator 主体直接 `return false`，
  框架铁律 `identity-model.md` §4 还明写「User 不得持角色，要区分 User 侧能力走套餐/功能开关」。
  ⇒ **「框架层的问题」只对一半**：框架不是漏了，是把它划给下游 —— **缺陷端在后端**（全仓 `Gate::`/`Policies`/`->authorize(` **0 命中**，
  归属靠 15 处零散 `where('user_id')`，且 7 处归属列 nullable）。**GUI 不是缺陷端**（恒 `true` 的 `hasNodeEditPermission` 桩按 R1 本就不算防线），
  但它现在也没有可依据的数据源：点名的五个字段里只有 `credits` 真有列（在 `tenant_users` pivot，不在 `users`），
  `level`/`score`/`right` **库里根本不存在**，而框架指定的替代（`feature_flags`）在后端 **链是断的**
  （依赖的 `FeatureFlagService` 属未安装的 module-platform ⇒ 恒 fail-closed 404，且 `feature.flag` 别名都没注册）。
- **稳定错误码** → `mynet_ctl/docs/knowledge-base/spec/v1-error-codes.md`：登记 **7 套并存信封**（E1–E7）
  与现有码注册表，证实 `code` 这一个键在后端 / OpenWrt / 文档里是**三种不同语义**（HTTP 整数 / shell 退出码 / 0–6 业务码），
  所以 §11「代理不得静默改变错误码语义」当前**无从判定**。冻结口径 C1–C6；待裁定进 **F-25 / F-26**
  ⇒ **同日已裁**：F-26 = 认 C1（E1 `BaseApiController` 为唯一 HTTP 信封，`code` 只放 HTTP 整数、业务码一律
  `error_code`，**只对新代码硬约束**，存量 7 套按清单登记不动）；F-25 = **改文档承认现状**
  （`spec/local-api.md` 的信封与四码改成代码真实形状，并写明分类靠文本嗅探、防线是 Host/Origin 而非 token）。
  顺带修掉 13 处实参槽错位（`NodeConfigController` 11 处把数组当 `$statusCode`、两个 Monitor service 的
  `DomainException` 把 `0` 喂给 `array $context`）—— 原判据是「命中即 `TypeError`」，属于文档说的合同在、代码不兑现。
- **无需裁定、本轮已清的三处**：`ApiTenantMiddleware` + `FrontendTenantMiddleware`（**两个零注册的租户中间件**；
  后者实现的优先级是「URL 参数 > 表单 > 会话 > 用户默认」，与 R4/R6 正好相反，还带改写请求体的能力 ——
  挂上即给全站装「参数定租户」）；`ModuleServiceProvider.php:43-54` 那句「api 组已 prepend `IdentifyTenant`」的假记载。
  验证：`make check` 全绿（guard / pint 325 files / 369 tests 3559 assertions）。
- 仍缺：**GNB 已落地版本写入兼容基线**、**密钥脱敏测试**（`BundleEnvelope` 侧尚无 redact 断言）。

**实施记录（2026-10-05，B 批 / 先B后A）：**
- **密钥脱敏测试 ✅ 已落** —— `mynet_api` 侧加 `BundleEnvelope::redactSecrets()`（`app/Modules/VPN/Domain/Bundle/BundleEnvelope.php:127`，按**键名**递归把 `private_key`/`preshared_key` 换成常量 `REDACTED`（`:20`，值 `[redacted]`），**只换非空字符串**、其余原样透传），配套回归 `tests/BundleEnvelopeV2Test.php`（`:131` `test_redact_secrets_replaces_only_secret_keys` 逐 fixture 走结构断言、`:145` `test_redacted_gnb_json_contains_no_private_key_material` 对 gnb fixture 另加**字符串不存在**断言、`:167` `test_redact_secrets_nested_empty_and_pure` 钉嵌套/空值/纯净体）。诊断面 `deploy/diag-bundle.php:20,78-80` 同批改走 `redactSecrets()`（`--full` 输出、工单贴回面 = 私钥不得进诊断输出）。
  - **为什么不改 fixture、为什么只对 gnb 做字符串缺席断言**：`wireguard-minimal.json` / `wireguard-multi-peer.json` 的 `private_key` 与 `public_key` **本就是同一串**（`dMOaQs…qD4=`，落 fixture 时的既有事实）⇒ 改它会连带 checksum 与 Go 侧用例红，故 WG 只做结构断言、不做「全文无该串」断言。
  - **故意不动的下载路径**：agent 的 fetch/install/restore 是 §6 所说「明确的配置下发/恢复流程」，此处不脱敏。
  - 验证：`make check` 全绿（pint 337 files、phpunit **437 tests / 4563 assertions**）。
- **GNB 已落地版本写入兼容基线 ⏸ 待裁（出决策包，不擅自选）** —— 全仓**无「兼容基线」承载物**：`mynet_agent/mynet/parity.go` 的 `fallback` map 只登记**部署 peer 的能力位**（zip / zip-level / crypto / safe-index / multi-socket / unified-forwarding），**不含 gnb 版本**；`Makefile` 里 `GNB_BASELINE ?= 1.6.8-c411f14ee647` + `GNB_PATCHES` 是**构建期 `-ldflags` 注入的字符串**（喂 `mynet version` 三句话），是「我们编的是哪版」而不是「对面可判定的兼容基线」。⇒「写进哪个文件 / 以什么形状 / 谁消费 / 旧节点怎么对齐」至少两条合法路径，按「不自行拍板」出决策包：**正文已成文** `mynet_ctl/docs/knowledge-base/spec/gnb-compat-baseline.md`（四个碎片逐条 `file:line` + 待裁 D1–D4 + 建议默认「先落文字表、零行为风险」），索引挂 `dev-plan.md` #75 行。
  - **✅ 已裁并落（2026-10-05）**：**D1-A**（知识库文档）+ **D2-E2**（表）+ **D3-iv**（不动运行时）+ **D4**（现网只读取证工单）。冻结基线表（三档：`1.6.8-c411f14ee647` / `v1.6.0.a` / 无 Features 行的旧 p2 构建）+ 只读取证工单见 `spec/gnb-compat-baseline.md` §6；**运行时一行未动**。现网实际版本待用户/运维跑 `mynet version` 回填。

**禁止：** 不改 UI 流程、不添加新驱动能力、不重新引入 Zone。

### P1：租户成员、邀请码和节点池配额

**目标：** 注册邀请码、加入租户、成员角色、节点池 6 IP 默认额度。（「购买更多池的配额模型」按 §0.4 不进 v1。）

**已落地：** `POST /api/v1/social/invitation/use` 入租链路 + `InvitationJoinTenantTest`（2026-10-04 提交，含事务内 `lockForUpdate` 重核修 `max_uses` 并发洞）。

**负责模块：** `mynet_api/app/Modules/*`、迁移和 Feature tests；GUI auth/member/node-pool stores/pages。

**依赖：** P0。

**必须测试：** 邀请过期/撤销/次数、重复加入、跨租户拒绝、用户和租户配额、并发分配 IP、审计记录。

> ⚠️ 已知的测试盲区：`max_uses` 的**真并发**窗口测不出来——测试库是 SQLite（整库写锁只会串行化），要 MySQL + 两条连接才行。现有 `test_exhausted_invitation_cannot_join_tenant` 是顺序用例，只钉住对外契约，证明不了服务里那段事务内重核还在。别把它当并发回归网。

**实施记录（2026-10-05，P1 测试补齐）：§13 第 2 条点名的「过期 / 撤销」两半已补。** 此前无用例，现加 4 条（`mynet_api/app/Modules/VPN/tests/Feature/Api/ZoneInviteJoinTest.php`）——
- **注册路径**（过期码 / 停用码 → 422 且**不建用户**）：钉住框架 `UserAuthController::register()` 的「建用户 + `consume()` 同事务」——`assertRegistrationAllowed` 在 open 模式不校验过期/停用，唯一拦截点是事务内的 `consume()`，失败即整体回滚、不留「已建用户但未入区」的孤儿。测试断言 `users` 无行 + `used_count` 不动。
- **入区路径**（过期 / 停用 → 422、不改归属、不核销）：`ZoneInviteDomainService::join()` 的 `inviteCodes->validate()` 在**事务外**前置拒绝，`users.tenant_id` 与 `tenant_users` 都不动。

**跨租户**此前已覆盖（`test_join_rejects_code_of_another_zone_when_already_bound`）；**真并发**（`max_uses` 竞态）仍是不测的盲区（同上条）。验证：默认 sqlite 套件 **441 tests 全绿**（较上批 +4 例 / +14 断言）；隔离 MySQL 子集 `deploy/test-mysql.sh` **150 tests 全绿**；`pint` 通过。**仍未做**：单独「核对租户迁移改动」。

**禁止：** 不把用户创建为租户、不把朋友关系直接变成节点互通。

### P2：双数据面和 bundle 合同

**目标：** GNB/WireGuard intent、驱动选择、双数据面能力声明、配置生成和回滚。

**负责模块：** `mynet_agent/shared/domain`、`mynet/driver`、`mynet/apply`、API VPN bundle、GNB/WG fixtures。

**依赖：** P0；可与 P1 并行实现，合并需先过 P0 contract。

**必须测试：** 双驱动正常/拒绝、未知版本、checksum、密钥格式、跨协议转发节点能力不足、旧 GNB bundle 回放。

**实施记录（2026-10-05，B 批 / 先B后A）：apply 层路由/防火墙回滚已落。** 落实 §6「新配置应用失败不得清除旧配置、回滚使用上一代」：`executeApply`（`mynet_agent/mynet/apply_plan.go`，仍是 F-5 唯一写入入口）在写入循环前建 `applyJournal`（`:94`），每条 route/firewall/iface **真正落下去才 `recordX`**（`:156,179,191`，`dry` 不记）；失败计数 > 0 时在收尾前调 `rollbackCmds(snap, &applied)`（`:646`）**逆序**回滚本轮已应用项（`:312` 起）。
- **回滚形状 = 对称逆操作，不是扫描/正则删除**：route 的 add/replace 一律只 `del`、原本就是 `del` 的**跳过**（不反向 add 回去，否则会把别人的路由装回来）；firewall 走 `firewallRollbackArgv`（`:607`，对 ensure-masq 与 open-port 各出 `nft delete rule` / `iptables -D`）；iface 走 `ifaceInverseAction`（`:590`，up↔down，只在有 ip 工具时）。
- **回滚命令一律 `optional=true`**，失败只记 `Error`、**绝不掩盖原始失败**（原报错始终是本轮失败的原因，回滚失败只是附注「需人工核对 FIB/防火墙」）。
- **三处刻意排除**（都写进代码注释，不扩大到「看起来更完整」）：`pinDefaultGateway`（回滚它 = 把默认网关重新劫持回去）、Proxy 段（已有自己的 `proxyCleanupCmds`）、`ExtraExec`（不可逆逃生口）。
- 用例 `mynet/mynet/apply_plan_test.go`：`TestRollbackInvertsAppliedRoutes`（`:276`，del 不再被反向、replace 优先、全 optional）、`TestRollbackFirewallArgvSymmetry`（`:300`，nft/iptables 两种 masq 与 open-port 带/不带 comment、无工具/未知类型返 nil）、`TestRollbackIfaceAndJournalEmpty`（`:331`）。验证：`make check` 全绿（`go test ./...` 全 ok、`check-route-writers.sh` 仍确认唯一写入点）。

**禁止：** 不改 `mynet_gnb/src`，不把驱动字段放进业务对象。

### P3：节点注册、自动组网和容量闸门

**目标：** 认证企业租户 scoped 自动组网；自己的节点默认互通；生成、计数、检查和回滚 routes/bundles。

**负责模块：** API route planner/command queue、agent command apply、gateway presence、GUI topology/status。

**依赖：** P1、P2。

**必须测试：** shadow/enforce、重复注册、并发节点加入、route.conf 大小上限、超限拒绝、应用失败回滚、旧连接不误摘新连接、race。

**禁止：** 不实现自研自动选路；不对开放租户默认开启无限全互通。

**实施记录（2026-10-05，A 阶段 / 先B后A）：P3 开发计划已成文（设计 + 决策包，未写运行时码）。** 正文 `mynet_ctl/docs/knowledge-base/spec/auto-mesh-planner.md`。**八条实测**：① 组网入口是人工 `RouteRequest` 审批（`RouteRequestDomainService.php:49,297`，accept 双向写 `Route` `:571-599`）；② 「自家节点默认互通」**已存在但隐式** —— `NetworkSemanticsProjector::fromDatabase()` 现算合成 own-node peer（`:208-256`），不落表、不经审批、**无计数**；③ 该合成直接决定 `route.conf` 规模（`BundleEnvelopePublisher.php:281-315` / WG `:436-459`）；④ **无任何大小/条数上限**（`GnbConfigGeneratorDomainService.php:218-239` 无阈值）；⑤ **无节点数配额**（grep 无命中）；⑥ `NodeCapabilityResolver`（`:41-79`）与 `TransportPlannerDomainService::plan/planAll`（`:25-62`）**全仓零调用方**；⑦ **两套连接模型并存、只有一套进数据面** —— `connections` 有唯一键 `uk_connection_pair`（迁移 `2026_09_23_000001:113`）却**不被 bundle 读**（`BundleEnvelopePublisher.php:198` 恒 `[]`），数据面只认 `routes`；⑧ `routes`/`node_route_requests` **无唯一索引**，仅业务级重入检查（`:179-288`）→ 并发可绕。**设计骨架**：租户级三态 `off/shadow/enforce`（默认 `shadow`，只对显式开启租户）、`MeshPlannerDomainService` 落 domain 层、`TransportPlanner` 作 enforce 第二跳接线、容量三量（每节点对端数/每租户边数/`route.conf` 字节）、回滚复用 P2 apply 回滚 + 上一代 bundle。**待裁 D1–D5 交主线**（D1 planner 与 own-node 合成的关系、D2 shadow 载体、D3 容量初值、D4 并发护栏范围、D5 enforce 作者对象 `routes` vs `Connection`）。分步 P3-1…P3-5 与测试计划见正文。

**裁定（2026-10-05）：自己的节点自动组网、不限制，且此前已实现。** 用户原话要点「不用问了，此前已经实现了，自己的所有节点，自动组网，**不限制**」⇒ ① **不建容量闸门、不做超限拒绝**（P3 的「容量闸门」半边对自家节点作废，M3 的「超限拒绝」同上）；② **planner 的「谁连谁」不立项**——自家节点已默认全互联（`NetworkSemanticsProjector.php:208-256` 的 own-node 合成即既有实现）；③ 与本节「不对开放租户默认开启无限全互通」**不冲突**——那条讲跨租户/开放租户，本裁定讲同一用户自己的节点，作用域不同。**P3 剩余面只剩一条真缺陷**：`routes`/`node_route_requests` 无唯一索引、仅业务级 check-then-insert 的**并发/重复注册护栏**（`spec/auto-mesh-planner.md` §1-8/§8），与可选的**尺寸只读统计**。裁定全文见 `mynet_ctl/docs/knowledge-base/spec/auto-mesh-planner.md` §8。

**追加裁定（2026-10-05 同日）：并发/重复护栏的修法已定 —— 保持代码层逻辑判断、不加唯一索引、不动表结构。** 理由：`routes`/`node_route_requests` 按「源+目标」建唯一键会与**软删除语义**纠缠（软删行仍占唯一位、复连时 `insertOrIgnore` 静默失败），反而更难查。⇒ 重复防护保持现状 `checkRouteRequestReentry()`（`:179-288`），**无新增运行时码**。**P3 就此收口**（planner 不立项、容量闸门作废、护栏维持逻辑判断）。详见 `spec/auto-mesh-planner.md` §8.1。

### P4：实体网络网关、节点内转发和 Proxy

**目标：** peer 网关接入实体网段；节点到网络、网络到网络；Proxy 路径转发、DNS/策略路由、停止清理。

**负责模块：** agent forwarding/proxy、OpenWrt `proxy.lua`/route/firewall、API service/policy、GUI service/acceleration。

**依赖：** P2；P3 的路由合同。

**必须测试：** 正向/回程路由、转发开关、网关离线、错误出口 fail-closed、DNS 模式、重启恢复、规则清理、敏感日志脱敏。

**禁止：** 不实现用户贡献节点市场；不把 Proxy 命名成第三种 VPN 协议。

**实施记录（2026-10-05）：P4 开发计划已成文（设计 + 决策包，未写运行时码）。** 正文 `mynet_ctl/docs/knowledge-base/spec/p4-forwarding-proxy-plan.md`。实测：① 网络到网络转发合同 = `GnbForwarding`（api `Domain/Bundle/GnbForwarding.php:7-53` ↔ agent `shared/domain/bundle_envelope.go:171-177`，消费 `driver/gnb.go:688-730`）；② 转发开关在 `NodeConfigController.php:384-386`；③ 实体网关**隐式**（无 `peer_gateway` 模型，表达为 `source:"lan"` 路由，装配在 OpenWrt）；④ Proxy 合同 `shared/domain/proxy_policy.go:12-44` + 落地 `proxy_apply.go`（策略路由/DNS/清理 `:394-420`/fail-closed ACL `:546-590`）。**三处真缺口**：**(a) §13 第 5 条未达标** —— OpenWrt `proxy.lua` **自读本地 `proxy_role.conf`/`proxy_route.conf`、不消费 bundle `network.proxy`**（`proxy.lua:16,60,411,439`）；**(b) 回程路由无实现**（只有诊断文案 `cmd_route_doctor.go:99-102`）；**(c) fail-closed / 网关离线无用例**、OpenWrt 无可执行测试。另 `Acceleration`/`service_policy` **全仓零命中**（`Service` 只是已发布服务目录）。**待裁 D1–D5**：统一合同形状 / OpenWrt lua 处置 / `Acceleration` 是否立项 / 回程处置 / fail-closed 用例落点。分步 P4-1…P4-6 与测试计划见正文。

**实施记录（2026-10-05，第二～六轮裁定 + 执行）：上条「未写运行时码」口径作废 —— P4 的 wire/许可面已落码。** 裁定与落地全文见 `spec/p4-forwarding-proxy-plan.md` §7.4–§7.8。四件事：

1. **bundle 的 `network.proxy` 收窄为「出口许可」** `ProxyPermission{enabled, acl}`；客户端策略（role/exits/dns/split/domain_rules/…）**移出 wire**，归节点 Local 轨（agent `ProxyIntentState`，`proxy_unify.go`），L6 按职责合成而非按 revision 仲裁（agent `76ef49f`、api `d38aa36`、ctl `24834a0` schema + fixture 重算 checksum）。同批删掉 `gnb.forwarding.proxy` 位（api `480f183`、ctl `914a3db`、agent `214284e`）与 node.conf `proxy` 字段（api `31027cc`）；gnb 代理开关的落点是独立 `driver/gnb/conf/<id>/proxy.conf`，云端不渲染。
2. **许可权威 = 节点级专表 `proxy_exit_grants`**（裁定不复用 `connection_grants`：那张表 `permission` 仅 varchar(32)、`connection_id` 非空且入唯一键，装不下节点级「允许谁」；`ConnectionGrant::RESOURCE_PROXY` 已撤）。迁移 `2026_10_05_000001` + `ProxyExitGrant` 模型 + `ProxyAuthDomainService::grant()/revoke()`（替换语义）。**§7.6 裁定：空白名单 = 组织内放开**（`ProxyAuthDomainService.php:52` → `MODE_OPEN`；出口侧无 `allowed_cidrs` 即不装规则，`proxy_apply.go:554-556`）。节点回写的 `node_configs.config.proxy` 仍留档，但**不再当作许可来源**（避免节点把自己误当出口）。
3. **「允许谁」在出口侧 L6 真生效**：云端把消费端解算成 overlay `/32` 写进 `acl.allowed_cidrs`，Go 侧 `ProxyACLCmds` **一行未动**（本就吃 `allowed_cidrs`）。
4. **用户面写面已开（§7.8）**：`GET|PUT|DELETE /api/v1/nodes/{id}/proxy-exit`（`ProxyExitController.php`、`Routes/api.php:141-143`，openapi `back.yaml` 新增 `node-proxy` tag）。语义 = **属主授权好友**，自己的节点不需要授权、自动并入白名单（`ProxyAuthDomainService.php:64,84`）；`friend_ids: []` / DELETE = 回默认「本网络区开放」（清空是放开，不是关闭）。两条 fail-closed：非好友拒绝（403 `DOMAIN_FORBIDDEN`）、好友无可连节点拒绝而**不是**误开成全网络区（否则空名单会退化成 NULL 行 = 全组织开放）。api `b38c36a`，`phpunit` 454 用例全绿（新增 `ProxyExitApiTest` 7 例 + `ProxyExitGrantTest` 第 6 例）。

**P4 仍未收口的四处（M4 因此未达标）**：**(a)** OpenWrt `proxy.lua` 仍自读本地 `proxy_role.conf`/`proxy_route.conf`、不消费 bundle（`proxy.lua:16,60,411,439`）—— §13 第 5 条字面仍未达标；**(b)** 回程路由仍无实现（只有诊断文案 `cmd_route_doctor.go:99-102`）；**(c)** 错误出口 fail-closed / 网关离线仍无用例，OpenWrt 无可执行测试；**(d)** **console 面「网络区提供 proxy 公共服务、授权出口」是独立线，未开**（受 console 写路径闸门约束：`Controllers/Console/V1/NodeController.php:23-24` 要求带审计与操作者身份，落点与审计接法待裁）。`Acceleration` 契约仍全仓零命中（待裁 D3）。

**实施记录（2026-10-08，出口授权小闭环 L0–L2 闭合）：上面 (c) 里「规则清理」那一半已落码，但 (a)(b)(d) 不变。** 详见 `mynet_ctl/docs/knowledge-base/spec/proxy-permission-loop-playbook.md`（上位基线挂在本节）与 `spec/proxy-exit-contract-lock.md`。三件事：

1. **代码结构闭环成立**（用户 10-08 确认主链）：用户授权 → API 原子写入（事务 + 行锁）→ 发布新 bundle（checksum/generation）→ agent 校验并拒旧代 → 应用出口 NAT/ACL → 撤销时停用许可并清理规则；重复请求/重复 apply 不叠加，写入失败保留旧授权，规则应用失败回退上一代。落点：api `5eb08d0`（写面原子化 + 跨租户/并发/合同门禁）、agent `3e3d464`（出口 NAT/ACL 作用域清理与回滚）、`9f20e45`（`GenerationRevision` 解析 + `ApplyCloud` 单调门禁，此前 `ParseInt` 恒 0 导致重放被当最新）。
2. **出口 NAT/ACL 专项**（原判「无清理、兜底 DROP 被当 optional」两个阻断）：改为接口独立 nft 专表 / iptables 链，带替换、幂等、撤销、故障回退与精确历史规则迁移；独立门禁 7 例 + OrbStack Alpine 里 nft 1.0.9 / iptables 1.8.10(nf_tables) 内核实测通过。
3. **仍未验（故 M4/M6 不勾）**：E/O/C/U 真实流量矩阵、真 gateway 通知与拉取、**出口离线 fail-closed**、撤销后 conntrack 旧连接是否续走出口、并发 apply 互斥、iptables-legacy / OpenWrt 版本兼容、实机乱序重放 —— 七项逐条见该 playbook §10。**「出口规则撤销已验」≠「L2 全部通过」≠「可发布」**。

### P5：OpenWrt LuCI 产品壳和弱设备适配

**目标：** LuCI 原生完成登录、节点、连接、网关、服务、Proxy、诊断和备份恢复；提供轻量执行路径。

**负责模块：** `mynet_openwrt/luasrc`、`files`、`scripts`、agent OpenWrt installer/commands。

**依赖：** P1、P2、P4 的接口；可与 P3 并行开发界面。

**必须测试：** ubus/ACL、弱设备启动、配置恢复、代理启停、脚本参数转义、LuCI/API 行为一致。

**禁止：** 不创建第二套租户模型、不复制 API 业务权威、不要求独立 8096 WebUI。

**规划记录（2026-10-10）：** 用户要求先核当前情况再制定 OpenWrt 规划；现状证据与 O0–O8 工作包已形成于 [OpenWrt Agent 接入实施规划](../../mynet_openwrt/docs/AGENT_INTEGRATION_PLAYBOOK.md)。先收敛实际执行合同/init，再接 LuCI/ubus、生命周期、统一 Proxy、出包与资源验证，最后现网迁移。规划已成文，运行时尚未实施，不勾 P5/M4。

**跨平台前置（2026-10-10 用户追加）：** .178 是公司当前网络主路由，.182 是家庭旁路由；安装/接口/运行/更新须分层，mynet 系统实现须平台条件编译。新 [跨平台规划](../../mynet_ctl/docs/knowledge-base/spec/platform-lifecycle-build-playbook.md) H0–H2 为 OpenWrt O1 前置，H4 为 O6 前置；[workspace review](../../mynet_ctl/docs/knowledge-base/workspace-review-20261010.md) 登记安装器与CLI的服务/路径/权限、更新与分发断层，运行时均未修复。

**实施记录（2026-10-10 核对）：本节仍无立项实施记录。** 已存在的相关资产是 `mynet_openwrt` 里 **10 条未推送**的 LuCI 批（Token Binding `X-Device-Fingerprint`、`route.conf` 注入废弃 —— 对端路由唯一写入者归 `mynet apply`、postinst 改 procd 启停、断开隧道二次确认、QEMU 联调脚本收编），分叉点 `de765cf`（09-30）、自 10-04 起未再动。**Proxy 侧仍是旧 Lua 路径**（与 P4 的 (a) 同源）。这些提交属"做完没推"，不算本节达标；弱设备实机验证仍无证据。

### P6：Gateway 和部署闭环

**目标：** 就近接入、WS/relay 透传、后端鉴权链路、部署和多实例边界。

**负责模块：** `mynet_agent/gateway`、`cmd/mynet_gateway`、API nginx/deploy、安装文档。

**依赖：** P0、P3；可提前验证协议，但最终门禁依赖 P3。

**必须测试：** 真 WebSocket 握手、Hijacker/Flusher、重连、verified 状态、路径白名单、签名逐字转发、上游 4xx 原样透传、单实例 presence 和未启用 Redis 的限制。

**禁止：** 不在 gateway 增加租户授权或业务查询。

**实施记录（2026-10-10 核对）：部分能力已有代码，真链路仍未验。** gateway **二进制已存在**（`cmd/mynet_gateway/main.go`），跨实例 presence 已接线 —— `NewRedisPresence` 被 `main.go:90` 调用（口令走 `MYNET_GATEWAY_REDIS_PASSWORD`），并有 `gateway/cross_instance_test.go`；`agent 2212e47` 落了「一键接入设备与统一本机界面」，`48b84f6` 落了「升级按本机 OS/架构选包」。**仍未验**：真实 WS/relay 端到端、跨机 WS 补拉与 ETag、通知丢失窗口、多实例 presence 真共享（见 `spec/proxy-permission-loop-playbook.md` §10「真实 gateway 通知与拉取」）。**本节达标判据未满足。**

### P7：GUI 产品化和发布验收

**目标：** 让普通用户看到“我的节点/节点池/可访问网络/服务/加速”，让管理员看到成员、配额、自动组网和诊断。

**负责模块：** `mynet_gui/src`、i18n、API client、docs。

**依赖：** P1–P6 的稳定契约。

**必须测试：** API 错误映射、权限菜单、密钥遮罩、邀请流程、节点池配额、协议/转发能力、WebSocket 状态、SPA 构建和 embed 产物。

**禁止：** 不把 zone、AllowedIPs、route.conf 作为普通用户主流程。

**实施记录（2026-10-10 核对）：设备与连接这一层已起步，四条用户路径尚未统一。** 10-09 落了：`mynet_gui 3a1b53d` 简化设备与连接流程并统一交互样式、`26d8562` 区分「首次创建设备」与「首次加载引导」、`5896fe4` 修正生产 API 代理回环与 WebSocket 域名；M0 侧另有 `922f18e` 首批 vitest + 类型棘轮（见 §10 M0）。**本节目标（统一「我的节点／节点池／可访问网络／加速」四条用户路径）仍无实施记录**，P1–P6 契约也未全部稳定（P1 真并发盲区、P2 版本/容量、P4 四处未收口）。

## 8. 并行与集成顺序

```text
P0
├── P1 ───────┐
├── P2 ──┐    ├── P3 ─── P4 ─── P5
└── P6 ──┘    └─────────────── P7
```

- 第一批只做 P0；领域合同和 fixtures 未冻结，不开始跨仓实现。**P0 已落地（§0.2），第一批的实际内容是审计 + 补缺（权限矩阵、稳定错误码），不是新建。**
- P1、P2、P6 可在 P0 后并行。
- P3 必须等 P1/P2 的身份、节点和 bundle 合同。
- P4 必须等路由和转发合同；P5 可以先做壳，但接入需等 P4。
- P7 最后接入，避免根据临时字段建立 UI。

每次集成顺序：共享合同 → API → agent → OpenWrt → GUI → gateway 部署/E2E。跨仓接口变更必须先改 fixtures 和契约测试。

## 9. 安全、隐私和失败处理

### 身份与授权

- 用户面使用会话/API auth；节点面使用节点密钥和 DH/HMAC；数据面由 GNB/WireGuard 自己保护。
- gateway 只转发；后端做用户授权、租户隔离和节点身份验证。
- 授权优先级：租户隔离 → 成员角色 → 节点/服务策略 → 数据面能力。

### 数据可见性

- metadata-only：在线状态、版本、能力、路由数量。
- inspect-body：仅 bundle 校验和诊断，限制大小、脱敏并不长期保留私钥。
- transform-body：配置生成和驱动适配，使用内存缓冲，错误只返回 reason code。
- external raw-body：gateway relay 原样转发，路径白名单，鉴权由后端，记录最少必要审计字段。

### 失败策略

- 鉴权、租户隔离、密钥校验、bundle checksum、容量上限和路由归属：fail-closed。
- presence 或非关键监控短暂失败：保留最后已知状态并标记 stale，不自动扩大权限。
- 配置应用失败：保留上一代可用配置，记录 generation 和 rollback reason。
- Proxy 出口异常：停止策略并清理注入的 route/nft/DNS 规则，不能静默直连错误出口。

## 10. 验证闸门

### M0：仓库基线

**七仓**（五仓契约口径 + `mynet_ctl` + `mynet_desktop`，见 §0.1）guard、lint、单元测试可运行；未提交改动已分类；无凭据和私钥泄露。

已知不达标项：~~`mynet_gui` 缺 `.githooks`~~ —— **2026-10-04 已补**（`Makefile` + `.githooks/{guard.sh,pre-commit,pre-push}`，`core.hooksPath=.githooks`，`make check` 实测 guard 通过、`eslint` 0 errors／194 warnings、`check:i18n` 通过）。**仍不达标：该仓 `vitest` 零用例**，「单元测试可运行」在这仓是空壳 —— `make test` 现在自己数套件数，为 0 就打印两行 `⚠⚠ 未执行任何测试`（不用 `--passWithNoTests` 装绿），补进第一个 `*.test.*` 即撤销告警。gui 侧类型棘轮已建（`check:types`/vue-tsc，2026-10-05）；其余盲区：`check:i18n` 漏检带参 key、`quasar build` 抓不到断链的 `import type`。`mynet_openwrt` 的 `make check` 需要 buildroot，本地用 `make -f Makefile.guard check`。`mynet_desktop/nsis/mynet-agent.nsi` 无法本地编译验证（homebrew `makensis 3.12` 对纯 ASCII 四行脚本也 `std::bad_alloc`）。

**2026-10-05 实施记录：`mynet_gui` vitest 零用例这一条已撤销（B 批 / 先B后A）。** 首个套件落地：`vitest.config.ts`（`environment: node`，`include: ['src/**/*.{test,spec}.ts']`，排除 `dist/`/`.quasar/`）+ `src/utils/__tests__/statusMapping.test.ts`（14 例：锁 `NODE_STATUS` 数值契约、逐状态三色语义、字符串状态码归一、谓词互斥、主操作文案与按钮 `disabled`）+ `src/utils/__tests__/vpnType.test.ts`（6 例：老库裸串 / 新库 JSON 数组双形态、空值剔除、标量回退）。此二件是**本仓 `*.test.ts` 的首批**，`make test` 的「零用例就响着放行」分支自此不再触发、真跑 `vitest run`（**20 passed / 2 files**）。同一批不改 `Makefile` 的判据（仍自己数套件、仍不用 `--passWithNoTests`），只把头部那段「当前零用例」的注释改成「首个套件已落地」的史实口径。验证：`make check` 全绿 —— guard PASS、eslint **0 errors / 185 warnings**、`check:i18n` 通过（zh 2171 / en 2006，新增 2 个被测文件未引入任何 i18n 键）、类型棘轮**未升**（vue-tsc 202 条 / 39 文件 = 基线，新文件 0 错）。gui 侧其余三条盲区（`check:i18n` 漏检带参 key、`quasar build` 抓不到断链 `import type`）**不在本批**，仍挂 M0 行上方。

### M1：双数据面

GNB 和 WireGuard 都有真实 fixture、配置生成、应用/回滚和最小互通证据；业务对象不含驱动术语。

### M2：租户与邀请

邀请码加入、过期、撤销、跨租户拒绝、成员权限、节点池配额和并发 IP 分配测试通过。

### M3：企业自动组网

认证租户 scoped enforce；容量 shadow 数据；超限拒绝；真实节点配置可回滚；route.conf 和 bundle 大小有证据。

> **2026-10-05 裁定修正**：**「超限拒绝」对自家节点不做** —— 自己的节点自动组网、不限制（既有行为）。M3 该条收窄为：**scoped enforce ✅（自家节点按 tenant+user 作用域，既有）**、**超限拒绝 ✗（裁定不做）**、**可回滚 ✅（P2 apply 回滚已落）**、**大小证据 = 只读统计（可选）**。见 §7 P3 裁定与 `spec/auto-mesh-planner.md` §8。

### M4：实体网络和 Proxy

真实 peer 网关完成节点到网络、网络到网络、回程和 Proxy 启停；错误出口、规则清理和敏感日志测试通过。

> **2026-10-05 进度：未达标。** 已具备的是 **Proxy 出口授权链**（节点级专表 `proxy_exit_grants` + 用户面写面 `PUT /api/v1/nodes/{id}/proxy-exit` + L6 按 `acl.allowed_cidrs` 真生效 + 空白名单=组织内放开）与 **bundle 许可收窄**（`network.proxy` = `ProxyPermission{enabled,acl}`，客户端策略归 Local 轨）。仍缺四条：① OpenWrt `proxy.lua` 不消费 bundle；② 回程路由无实现；③ 错误出口 fail-closed / 网关离线无用例；④ console 面「网络区公共代理授权出口」未开。见 §7 P4 与 `spec/p4-forwarding-proxy-plan.md` §7.4–§7.8。

> **2026-10-10 进度：仍未达标（M6 同样不勾）。** 相对上一条的增量只有「规则清理」这半边：出口 NAT/ACL 改为接口独立 nft 专表 / iptables 链，带替换/幂等/撤销/故障回退，独立门禁 7 例 + OrbStack Alpine 内核测试通过；代码结构小闭环（授权→发布→应用→撤销→回退）已成立，有测试守护。**①②④ 三条不变**；③ 只解决了一半 —— **规则能撤了，但「撤销后 conntrack 旧连接是否续走出口」「出口离线时消费端是否回落本地公网」这两个 fail-closed 判据一件没验**。七项上线前实机验收逐条见 `spec/proxy-permission-loop-playbook.md` §10。

### M5：OpenWrt

LuCI 原生流程在弱设备模式通过安装、注册、配置同步、转发、Proxy、备份恢复和重启恢复。

### M6：发布

API migration/restore、agent `make check`、GUI lint/test/build、OpenWrt package/build、已落地 GNB 版本的构建与 checksums 回归全部通过；embed 产物不依赖开发目录。

交付口径另加两仓（§0.1）：`mynet_ctl` 的 `ReleaseDomainService` 平台枚举/产物命名与 `/d/` 分发面一致、`mynet_desktop` 的 `make check` 与三平台打包目标（`pack-pkg` / `pack-dmg` / `pack-nsis`）可跑。

## 11. Agent 交付协议

每个工作包必须提交小而可回滚的 commit，命名为 `area: short-description`，并报告：

```text
Work package:
Files changed:
Interfaces added:
Behavior changed:
Migration impact:
Security/privacy impact:
Tests and commands:
Known limitations:
Follow-up dependencies:
Rollback procedure:
```

代理不得静默改变租户、用户、节点池、私网、协议、权限或错误码语义。发现需要修改冻结合同时，先更新合同、fixtures 和本 playbook，再实现。

## 12. Definition of Done

v1 只有在以下条件同时满足时才算完成：

- 企业租户可通过邀请码邀请成员并限制节点/节点池配额。
- 用户可在租户内拥有多个节点池和自己的节点。
- 认证企业租户可启用有容量限制的自动组网；自己的节点按规则互通。
- GNB/WireGuard 可分别运行并在同一租户共存；跨协议可通过双数据面节点转发。
- Peer 网关可接入实体网络并完成回程路由。
- Proxy 路径加速实现可配置、可诊断、可停止和可清理。
- 密钥、配置备份和恢复有明确流程且无私钥泄露。
- LuCI 原生界面覆盖弱 OpenWrt 设备；桌面/服务器 agent 完成闭环。
- gateway 真 WS/relay 链路、API 鉴权链和部署文档通过验证。
- 所有验证闸门通过，已知限制和未实现的 UTC/贡献加速机制已记录。

## 13. 第一批执行任务

> 进度（2026-10-04）：仓库基线分类已完成（§3「当前改动和基线处理」）。下面第 1、2 条已按 §0.2 / §0.4 改写口径。

1. P0：**审计并补冻结**已有领域合同——`BundleEnvelope` 两侧与双驱动 intent 已在，真正要补的是权限矩阵与稳定错误码清单，再把已落地 GNB 版本写入兼容基线。
   **状态（2026-10-04）：◐ 两份冻结件已成文**（`mynet_ctl/docs/knowledge-base/spec/v1-permission-matrix.md`、`v1-error-codes.md`，逐条带 `file:line` + 复现命令）；§7 P0 点名的 **7 个待裁定项（F-22/F-23/F-24/F-27/F-28 权限面，F-25/F-26 信封面）已于同日全部裁定**（见 `mynet_ctl/docs/knowledge-base/dev-plan.md`「2026-10-04 逐项裁定」表，取向摘要见上文 §7 P0 的「裁定四句话」），所以冻结件的「冻结列」方向已成立，**剩下的是按裁定落码**。仍缺两件：GNB 已落地版本的兼容基线登记、`BundleEnvelope` 密钥脱敏测试。
   **状态（2026-10-05，B 批 / 先B后A）：两件均已收口到「可裁决」** —— ① **密钥脱敏测试 ✅ 已落**（`BundleEnvelope::redactSecrets()` + `BundleEnvelopeV2Test` 三例 + `diag-bundle.php` 诊断面接线，`make check` 437 tests 全绿）；② **GNB 兼容基线 ⏸ 决策包已成文** `mynet_ctl/docs/knowledge-base/spec/gnb-compat-baseline.md`（四个碎片逐条 `file:line` + 待裁 D1–D4 + 建议默认「先落文字表、零行为风险」，索引挂 dev-plan #75 行）。详见上文 §7 P0 的两条实施记录。
   **状态（2026-10-05，B1 裁定后）：⇒ P0 两件全部收口 ✅。** ① 密钥脱敏测试已落；② **GNB 兼容基线已裁并落**（**D1-A** 知识库文档 + **D2-E2** 表 + **D3-iv** 不动运行时 + **D4** 只读取证工单）：冻结基线表（三档）+ 取证工单见 `spec/gnb-compat-baseline.md` §6，**运行时一行未动**；现网实际版本待运维跑 `mynet version` 回填。
2. P1：邀请码入租链路已提交（`/api/v1/social/invitation/use`）；剩下核对租户迁移改动，补齐过期、撤销、跨租户和并发配额测试。**配额计费按 §0.4 不做。**
   **状态（2026-10-05）：◐ 过期 / 撤销两半已补。** 入租链路现走框架码空间 + `/api/v1/user/tenants/join`（旧 `/social/invitation/use` 已随 invitations 表下架）；新增 4 条用例（注册 × 过期/停用 → 422 不建用户；入区 × 过期/停用 → 422 不改归属不核销），见 §7 P1 实施记录。跨租户此前已覆盖；**真并发（`max_uses` 竞态）** 仍是不测盲区（sqlite 串行化，需 MySQL 双连接），**「核对租户迁移改动」尚未单独做**。
3. P2：对照现有 `BundleEnvelope`、GNB/WireGuard intent 和 agent apply，补齐版本、容量和回滚合同。
   **状态（2026-10-05，B 批）：回滚合同 ✅ 已落** —— `executeApply` 加 `applyJournal` + `rollbackCmds()`（失败时逆序回滚本轮已应用 route/firewall/iface、全 `optional` 不掩盖原始失败、刻意排除 `pinDefaultGateway`/Proxy/`ExtraExec`），用例三支，`make check` 全绿。版本/容量两半仍在 §7 P2（版本半边与 GNB 兼容基线决策包相邻）。详见 §7 P2 实施记录。
4. P3：设计自动组网 planner 的 shadow 输出和容量指标，先不扩大默认租户范围。
   **状态（2026-10-05，A 阶段）：◐ 开发计划已成文**（`mynet_ctl/docs/knowledge-base/spec/auto-mesh-planner.md`，八条实测 + 设计骨架 + 待裁 D1–D5 + 分步 P3-1…P3-5 + 测试计划）；**未写运行时码**，等 D1–D5 裁定与步骤 P3-1 的开工指示。详见 §7 P3 实施记录。
   **裁定（2026-10-05）：自己的节点自动组网、不限制（既有行为）。** P3 的**容量闸门/超限拒绝对自家节点作废**，**planner 不立项**；P3 剩余面只剩「并发/重复注册护栏」一条真缺陷 +（可选）尺寸只读统计。详见 §7 P3 裁定与 `spec/auto-mesh-planner.md` §8。
   **追加裁定（2026-10-05 同日）：该护栏保持代码层逻辑判断、不加唯一索引、不动表结构（软删语义会使唯一键更难缠）⇒ P3 收口，无新增运行时码。** 详见 `spec/auto-mesh-planner.md` §8.1。
5. P4：把现有 OpenWrt Proxy 实现接入统一的 Service/Forwarding/Acceleration 合同。
   **状态（2026-10-05）：◐ 开发计划已成文**（`mynet_ctl/docs/knowledge-base/spec/p4-forwarding-proxy-plan.md`，现状实测 + 三处缺口 + 待裁 D1–D5 + 分步 P4-1…P4-6）；**未写运行时码**。**关键实测：本条字面未达标** —— OpenWrt `proxy.lua` 自读本地 `proxy_role.conf`/`proxy_route.conf`（`:16,60,411,439`），**不消费 bundle `network.proxy`**；且 **`Acceleration` 契约全仓零命中**（`Service` 只是已发布服务目录）。详见 §7 P4 实施记录。
   **状态（2026-10-05，第二～六轮裁定后）：◐ wire/许可面已落码，本条字面仍未达标。** `Forwarding` 半边已按裁定收敛（`gnb.forwarding.proxy` 位删除、node.conf `proxy` 字段删除），`network.proxy` 收窄为出口许可 `ProxyPermission{enabled,acl}`，权威改节点级专表 `proxy_exit_grants`，并开出用户面写面 `PUT /api/v1/nodes/{id}/proxy-exit`（属主授权好友，自己的节点自动并入）。**但 OpenWrt `proxy.lua` 仍不消费 bundle**（`proxy.lua:16,60,411,439` 自读本地 conf），`Acceleration` 仍零命中 ⇒ 「接入统一合同」这条**未完成**；`Service` 侧也仍未与 Proxy 打通。详见 §7 P4 两条实施记录与 `spec/p4-forwarding-proxy-plan.md` §7.4–§7.8。
   **状态（2026-10-10 对齐）：◐ 不变，另起了一条小闭环。** 本条（接入统一合同）没有新进展；独立立项的是 `spec/proxy-permission-loop-playbook.md` 的**出口授权小闭环**，其 **L0–L2 代码结构已闭合**（api `5eb08d0`、agent `3e3d464`/`9f20e45`、ctl `1208561`），但**上线前七项实机验收未做**，故 M4/M6 不勾。当前 HEAD：agent `db398d2`、api `76e1214`、ctl `8fc5e6e`、gui `26d8562`，四仓已推送；`mynet_openwrt` 10 条 / `mynet_gnb` 2 条仍压在本地。
6. P5/P6：以真实 LuCI 和真 WebSocket 作为集成入口，避免只依赖 mock 或独立 8096 页面。
7. P7：合同稳定后再统一 GUI 的“我的节点/节点池/可访问网络/加速”用户路径。

### 插队项（不属于任何 P，但挡路）

- **API 状态机地雷**：缺 `node_configs` 行即被判「未配置」，任意一次基本信息 PUT 会把在线节点打成 `-1`。这是 P1/P3 的前置——节点状态不可信，自动组网的 shadow/enforce 都无从验证。
  **状态（2026-10-04）：三层全部收口，无待裁决项。** 已修：模型侧判据（`Node::checkConfigurationStatus`）、服务侧判据（`NodeService::checkNodeConfigurationStatus`，把 `has_config` 降为可选项，只算必选项）、基本信息 PUT 不再无条件评估配置判据（`updateNodeBasicInfoWithStatusCheck` 只在请求显式带 `node_status` 时才走闸门）。**关键教训：同一个误判有两张脸——只修模型侧不修服务侧，两个判据口径不一致，缺陷照样活着；判据改动务必两侧同时改。** 第三层两条已裁定：GET `/nodes/{id}/status` **改为只读**（`mynet_api` `4df87e3`，附 2 个真请求测试锁住「不写 `node_status`、不碰 `updated_at`」）；心跳无条件写 `STATUS_ENABLED` **保持不动**（它是唯一自愈通道，`.184` 已证明「卡在错误状态且无人纠正」比「状态偶尔偏乐观」危险得多），`node_status`/`presence` 拆字段记为 v1 之后方向，须走 §11 合同冻结流程。详见 `mynet_ctl/docs/knowledge-base/spec/node-status-gate.md`。
- **Zone 彻底删除**：跨 api + gui 双仓（§0.5、§6）。拖得越久，`zone_*` 契约上粘的 GUI 代码越多。
  **状态（2026-10-04）：已裁决，四步全部落地。** 取证发现前提错了——Zone 已不是业务对象只剩名字，盲删会出两类事故（抹掉现网 182 的 gnb/防火墙 zone 知识、单方面改名静默断掉全站 WebSocket）。用户裁定：议题 1 = A（改名保留，不删页面）、议题 2 = B（`DEFAULT_GLOBAL_ZONE` 改指真实平台租户 `9007199254740991`）。追加裁定：界面中文用词叫「网络区」，且要做成可替换的**取词单点**；第二轮追加三条 —— **用户面不表达 tenant 权限/边界**（「user 是 user, operator 是 operator……用户无感 tenant 的存在」「GUI = 普通用户前端」）、**LocalStorage `current_zone` 改名并迁移**、**en-US 用词 = Network**。中文界面这一层现在有四个单点，换词四处一起改：`mynet_gui/src/config/terms.ts` 的 `TERM_ZONE`、`mynet_api/config/mynet.php` 的 `terms.zone`（`term_zone()`）、本仓 `mynetd/localapi/webui/index.html` 内联的 `TERM`、本仓 `mynet/cmd_node.go` 的 `termZone`。已落地：第 0 步与议题 2（`mynet_api df6c4da`、`mynet_gui 44f389d`+`68b273f`）、第 1 步 GUI 内部改名（`mynet_gui cf00de3`）、界面用词（`mynet_gui 61d6b86`+`434701b`、`mynet_api 8a98e3d`、本仓 `b80c029`+`9223eeb` 含 CLI 与节点 WebUI）、API 契约注释同步（`mynet_api 1e42a4d`）、**第 2 步 `zone_id`→`tenant_id` 契约翻转**（`mynet_api 5aa8f34` 带 `TenantWireContractTest` 钉死 wire，333 tests 绿；`mynet_gui 716f431` 同批改读新名 + LocalStorage 迁移 + en-US 落 Network + 按「用户无感 tenant」把租户 CRUD/overview/统计/`pivot.is_admin` 从用户面删除）。**剩下的只有 7 条未决**（非租户域的 `zone_id` 死读、WS 频道名服务端无实现、cluster B 命名、5 个无路由孤儿页面、死文案 key 等），逐条见 `spec/zone-removal.md` 的「第 2 步留下的未处置项」。
