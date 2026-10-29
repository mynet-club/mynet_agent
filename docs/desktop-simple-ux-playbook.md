# MyNet 桌面极简体验 Playbook

## 1. 产品边界

**定位：** MyNet 桌面版只负责让用户可靠地连接本机节点，并用 DNS hook 把选定域名送入隧道；用户不需要理解代理出口、路由、DoH、ACL 或网关策略。

**目标用户：** Windows、macOS，以及未来 Linux 本机模式的普通用户。

**第一参考场景：** 用户安装 MyNet，打开面板，完成初始化，选择干净 DNS 源，打开加速并添加一个自定义域名；重启应用后状态和配置仍然正确。

**部署形态：** 桌面 Agent + 本机 WebUI + 云端基础域名清单。Linux/OpenWrt client 继续支持高级网关配置。

**明确排除：** 桌面 UI 不暴露全隧道、区域策略、黑名单、出口节点、ACL、DoH/DoT、下游设备分流和网关 DNS 模式。

## 2. 当前基线与刻意变化

现有嵌入式 WebUI 已有 `proxy dns-hook` 命令、本机自定义域名、云端域名基线和 DNS 状态，但页面仍显示四个 Proxy 子页及网关字段；`mynet_gui` 的 WebSocket 体验修复已落地，未提交改动必须保留。

保留：

- 云端基础域名清单只读下发。
- 本机自定义域名只写本机。
- DNS hook 未接入隧道时不能宣称加速已生效。
- Linux/OpenWrt 的完整 ProxyPolicy、出口和 ACL 能力。
- 现有 `mynet install` 的升级保留配置、dry-run 和端口冲突拒绝语义。

改变：

- 桌面加速页面收敛为“开关、DNS 源、域名清单、状态、诊断”。
- 桌面不再直接编辑完整 `ProxyPolicy`。
- 安装与初始化统一为可重复执行的状态机和 repair 流程。

## 3. 冻结领域契约

### 3.1 桌面加速配置

```json
{
  "enabled": true,
  "dns_sources": ["tenant", "peer", "clean_public"],
  "custom_domains": ["example.com", "*.internal.example"]
}
```

- **key：** 本机节点身份。
- **owner：** Agent 本机配置；云端只拥有 tenant 基础清单和租户 DNS 候选。
- **scope：** 当前设备，不随用户账号跨设备复制 `custom_domains`。
- **lifecycle：** `disabled → waiting_tunnel → applying → active → degraded`。
- **version：** `desktop_acceleration.v1`。
- **隐私：** 自定义域名属于本机配置，不自动回传云端；日志只记录数量和 hash，不记录完整域名。
- **审计：** 记录启停、源变更、应用结果、失败 reason code；不记录 DNS 查询内容。

### 3.2 干净 DNS 源

桌面界面显示可选源，但不允许用户编辑复杂 DNS 协议参数。候选源按优先级从高到低排列：

1. `custom`：用户明确配置的本机干净 DNS（可选，默认关闭）。
2. `tenant`：网络区/云端下发的干净 DNS。
3. `peer`：已连接节点提供的干净 DNS。
4. `clean_public`：内置公共兜底源，只在前面都不可用时使用。

选择策略是**按顺序尝试，首个健康源生效**，不是把所有源并行混用。界面显示当前生效源、候选源和失败原因；用户可调整优先级，但不能把所有源都删空。公共兜底源必须有固定版本和可审计地址，不能运行时从任意外部服务发现。

### 3.3 安装状态

对用户暴露：

```text
NOT_INSTALLED → INSTALLED → INITIALIZING → READY_OFFLINE / READY_ONLINE
                                      ↘ DEGRADED → REPAIRING
```

底层已有的 `UNINSTALLED / INSTALLED / CONFIGURED / RUNNING / DEGRADED` 保留，用统一适配层映射，不让 UI 自己猜状态。

## 4. 行为、迁移与失败决策

- 旧 `ProxyPolicy` 数据保留；桌面 API 读取时只投影 `enabled`、DNS 源和自定义域名。
- 第一次保存桌面配置时执行一次性迁移：读取旧 `domain_rules.custom_domains` 和本机 DNS 配置，生成 `desktop_acceleration.v1`；不删除旧字段。
- 网关高级字段只允许 Linux/OpenWrt client 继续写入，桌面写接口拒绝这些字段并返回稳定的 `DESKTOP_FIELD_UNSUPPORTED`。
- DNS 源采用 fail-closed：没有健康源时不启动“加速已生效”，保持普通网络并显示“等待可用 DNS 源”。
- DNS hook 启停必须幂等：重复 start/stop 返回当前事实，不重复修改系统 DNS 或防火墙。
- 配置写入使用临时文件、fsync、rename；应用失败保留上一份有效配置。
- 安装/初始化任何步骤失败都停止后续副作用，提供 repair；不自动无限重试。
- 所有 API 返回 `reason_code`、用户可读 `message`、`retryable` 和当前状态。

## 5. 目标架构

```text
桌面 WebUI / 托盘
        │  desktop acceleration API
        ▼
mynetd localapi
        │  统一状态、并发锁、旧配置投影
        ▼
mynet executor
        │
        ├── proxy dns-hook（Windows/macOS/Linux desktop）
        └── full ProxyPolicy（Linux/OpenWrt client only）
```

桌面 API 建议：

```text
GET  /api/v1/acceleration
PUT  /api/v1/acceleration
POST /api/v1/acceleration/apply
GET  /api/v1/acceleration/status
```

桌面 UI 不直接调用 `/api/v1/plugin/proxy` 编辑完整策略；旧接口保留给网关高级页和兼容迁移。

## 6. 里程碑执行计划

每个里程碑都是一个完整的可验收增量。里程碑内可以并行处理代码、测试和文档，但不把同一功能继续拆成大量独立小任务。

### M1：桌面加速契约和 Agent API

**目标：** 桌面拥有稳定、窄化、可幂等的加速控制面。

包含：

- 冻结 `desktop_acceleration.v1`、DNS 源优先级和 reason code。
- 完成 `GET/PUT /api/v1/acceleration`、`POST .../apply`、`GET .../status`。
- 接入旧 `ProxyPolicy` 的一次性投影迁移。
- 接入配置锁、原子写入、DNS hook start/stop/apply 和 fail-closed。
- 增加 API、迁移、幂等、并发、失败恢复和隐私测试。

验收条件：桌面客户端可以只依赖新 API 完成读取、保存、启停和状态展示；Linux/OpenWrt 旧 Proxy API 行为不变。

### M2：桌面极简 WebUI 和托盘一致性

**目标：** 普通桌面用户只看到“开关、DNS 源、自定义域名、状态”。

包含：

- 重做嵌入式 WebUI 的桌面加速页。
- 隐藏桌面模式下的区域、全隧道、出口、ACL、DoH/DoT 和网关 DNS 字段。
- 增加干净 DNS 源优先级选择、当前生效源、源失败原因和恢复入口。
- 托盘改为调用同一套 acceleration status/apply API。
- 保留 Linux/OpenWrt client 的高级配置入口和完整字段。

验收条件：从空配置、隧道未连接、DNS 源失败、系统改写 DNS、配置成功和恢复成功等状态走完整 UI；托盘和 WebUI 状态一致。

### M3：安装与初始化收敛

**目标：** 安装、首次启动、升级、修复都进入同一套状态机。

包含：

- 统一 `NOT_INSTALLED → INSTALLED → INITIALIZING → READY_* / DEGRADED → REPAIRING` 映射。
- 为 `mynet install` 增加 preflight、目标状态 reconcile、健康确认和 repair 语义。
- 三平台统一服务 owner、配置保留、旧版本迁移和端口冲突处理。
- WebUI wizard 只保留“检查本机 → 选择云端/本机 → 登录或生成身份 → 完成”。
- 安装成功以本机 API、node-status 和 WebUI 资源健康为准。

验收条件：全新安装、重复安装、中断重试、升级、修复、旧配置恢复和外部端口占用均有明确结果，且不会留下半安装状态。

### M4：跨平台集成和回归

**目标：** 将 M1–M3 接入真实构建、安装包和平台 smoke。

包含：

- 嵌入 WebUI、Agent 二进制和桌面安装器联调。
- Windows、macOS、Linux desktop 本机模式验证 DNS hook。
- Linux/OpenWrt client 验证高级设置仍可用。
- 托盘、面板、服务重启和机器重启后的状态恢复验证。

验收条件：agent 全量测试、GUI build/type/lint、embedded UI 检查、安装器 source guard、平台 smoke 全部通过。

### M5：发布和运维收口

**目标：** 形成可交付的版本合同和排障资料。

包含：

- 更新安装文档、桌面加速帮助文案和迁移说明。
- 输出状态、reason code、诊断字段和回滚说明。
- 固定版本的公共 DNS 兜底源和云端清单版本策略。
- 生成发布检查记录和已知限制清单。

验收条件：新环境可以按文档完成安装和初始化；旧版本可以升级或修复；失败场景可通过诊断字段定位。

## 7. 集成顺序

```text
M1 ──► M2 ──► M4 ──► M5
  └────► M3 ────────► M4
```

M1 完成前不重做桌面加速 UI；M2 完成前不移除旧 Proxy API；M3 完成前不改变安装器的默认服务 owner。

## 8. 安全、隐私和数据处理

- 身份来源仍是本机节点身份和本机登录会话；桌面自定义域名不上传。
- DNS 查询内容不进入普通日志、托盘 tooltip 或诊断摘要。
- 日志只允许记录 `source`、数量、版本、hash、reason code 和耗时。
- 外发目标只包括已配置的 gateway/tenant API；公共 DNS 兜底地址编译期固定。
- 私钥、token、完整域名清单不得进入 argv、错误提示、托盘菜单或安装器日志。

## 9. 验证门槛

每个工作包必须覆盖：

- 正常、拒绝、超时、重试、版本迁移、并发竞态。
- 配置 round-trip 和旧配置兼容。
- DNS 源优先级、源全部失败、源恢复。
- 重复安装和重复 apply 的幂等性。
- 日志和错误响应无域名清单、token、私钥泄露。
- GUI build、类型棘轮、lint、Go tests、embedded UI resource check。

## 10. 当前执行批次

**M1、M2 已完成，当前进入 M3。** 已完成桌面配置契约、域名规范化、DNS 源优先级、localapi 窄 API、旧 Proxy intent 投影迁移、DNS hook 幂等 apply、真实运行态字段解析、极简 WebUI、统一托盘状态，以及 `/api/v1/acceleration/install` 本机 PC runtime 安装入口。新增 `/api/v1/experience/state` 聚合向导产物与节点状态，桌面初始化不再需要拼接多个状态源；启用加速前会先幂等切换到 PC 模式，再保存并应用 DNS hook。下一步接入安装器的 preflight/reconcile/repair 和桌面首启流程。

## Agent 交付格式

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
```

## Definition of Done

只有在桌面用户无需理解网关高级概念、DNS 源优先级可解释、安装可重复执行、失败可修复、旧配置可恢复、三平台构建和关键测试通过后，本计划才算完成。
