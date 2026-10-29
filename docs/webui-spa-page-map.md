# WebUI SPA 页面映射与契约冻结（M0）

> 依据：[webui-spa-playbook.md](webui-spa-playbook.md)。本文件是 M0 交付物，
> 冻结页面映射、状态模型、组件命名与 API 分组。**禁止新增手写 HTML 业务页面。**

## 1. 现有手写 HTML → Vue 路由映射

现有 `mynetd/localapi/webui/index.html`（约 3453 行）共 5 个一级页 + 子 tab。
迁移后主实现在 `mynet_gui`，路由树按桌面 / 网关双模式分组。

### 1.1 桌面模式（默认，capability=form:pc）

| 现有 HTML | 现有 tab/区块 | Vue 路由 | Vue 页面 | 说明 |
|---|---|---|---|---|
| `page-overview` + `desktop-acceleration-card` | hero 状态、DNS、域名、错误 | `/home` | `LocalHomePage` | 聚合 `experience/state` + `acceleration/status` |
| `page-config` / `config/proxy`（desktop-simple） | 出口、DNS 四行、域名清单 | `/acceleration` | `LocalAccelerationPage` | 三卡片：出口 / DNS / 域名 |
| `page-config` / `config/keys` + `page-overview` 节点 tab | 身份、密钥、版本 | `/node` | `LocalNodePage` | 本机身份与连接 |
| `page-config` / `config/runtime`（部分） | 启动、语言、主题、诊断导出 | `/settings` | `LocalSettingsPage` | 不含 gateway-only 字段 |
| （向导）wizard/login、config/active | 登录、初始化 | `/onboarding` | `LocalOnboardingPage` | 仅 `needs_login` / `needs_install` 时展示 |

桌面模式**隐藏**：分流模式、复杂 ACL、路由编辑、插件原始配置、服务端策略、
`desktop-legacy-card` 全部区块。

### 1.2 网关 / OpenWrt 模式（capability=form:gw 或 openwrt）

| 现有 HTML | 现有 tab | Vue 路由 | Vue 页面 |
|---|---|---|---|
| `page-overview` | nodes / system / peers | `/overview` | `LocalOverviewPage` |
| `page-overview` peers + `page-routes` peer | 对端 | `/peers` | `LocalPeersPage` |
| `page-routes` | lan / topo / sys | `/network` | `LocalNetworkPage` |
| `page-config` / `config/proxy` split+dns | 分流、DNS | `/split` | `LocalSplitPage` |
| `page-config` / `config/proxy` acl+exit | 出口、ACL | `/exit` | `LocalExitPage` |
| `page-config` / `config/runtime` plugins | 服务与插件 | `/services` | `LocalServicesPage` |
| `page-diag` | run / log / raw | `/diagnostics` | `LocalDiagnosticsPage` |
| `page-config` / `config/runtime` + `page-space` | 系统设置 | `/settings` | `LocalSettingsPage` |

### 1.3 共用 / 过渡

| 现有 | 处置 |
|---|---|
| `page-space`（控制台 embed / 连接） | 网关模式挂 `/overview` 子区；桌面隐藏 |
| `index.html` 整文件 | **纯跳转 `/gui/`**，无 fallback、无 API 调试台（2026-10-27 裁定） |
| `/gui/` SPA | 本机与社区域同一 SPA；地址是本机即本机面，路径不写 local |
| `/gui/api/v1/*` | **本机 relay**（剥 `/gui` 后经 gateway 转后端），平台面用；本机能力面走 `/api/v1/*` |

## 2. 状态模型（冻结）

页面统一使用以下状态层，**禁止**从零散字段自行推导生命周期：

| state | 含义 | UI 行为 |
|---|---|---|
| `loading` | 首次读取 | 骨架 / spinner |
| `ready` | 数据完整可操作 | 正常交互 |
| `degraded` | 部分数据不可用 | 可操作 + 警示条 |
| `needs_login` | 需要登录/续期 | 重新登录入口 |
| `needs_install` | 本机能力未安装 | 安装 / 修复入口 |
| `applying` | 配置正在应用 | 按钮 disabled + 进度 |
| `error` | 操作失败可重试 | 错误面板 + 重试 |

后端返回：`state`、`reason_code`、`message`、`retryable`、`data`。
前端**不得**通过错误字符串判断状态。

`experience/state` 的 `state` 枚举（`wizard.go:128-147`）：

```text
NEEDS_LOGIN → NEEDS_CONFIGURATION → INITIALIZING
    → READY | READY_OFFLINE | DEGRADED | NEEDS_INSTALL
```

映射到 UI 状态层：

| experience/state | UI state | primary_action |
|---|---|---|
| NEEDS_LOGIN | `needs_login` | login |
| NEEDS_CONFIGURATION | `needs_install` / onboarding | configure |
| NEEDS_INSTALL | `needs_install` | install |
| INITIALIZING | `loading` / `applying` | retry |
| READY | `ready` | — |
| READY_OFFLINE | `ready`（降级提示） | start |
| DEGRADED | `degraded` | fix-routes |

## 3. 组件命名（冻结）

共享组件层，前缀 `Mynet`，**只接收结构化 props，不直接调用 axios**：

| 组件 | 职责 |
|---|---|
| `MynetPageHeader` | 标题、面包屑、主操作 |
| `MynetStatusBanner` | 顶部状态摘要（文字+颜色双表达） |
| `MynetMetricCard` | 指标卡 |
| `MynetConfigCard` | 配置卡（单一主动作） |
| `MynetFieldRow` | 表单行（标签/输入/帮助） |
| `MynetPriorityList` | DNS 等优先级可排序列表 |
| `MynetNodeSelect` | 节点/出口选择 |
| `MynetDomainEditor` | 域名清单编辑 |
| `MynetCloudPreview` | 云端清单预览（只读） |
| `MynetAsyncState` | loading/ready/error 统一壳 |
| `MynetErrorPanel` | 错误 + reason_code + 重试 |
| `MynetConfirmAction` | 危险操作确认 |

API 调用放在 composable/store，组件只处理展示、校验和事件。

## 4. API 分组（冻结）

本机 API 四组（均走 `mynetd/localapi`）：

### experience（初始化、能力、统一状态、安装/修复）

| 端点 | 状态 | 说明 |
|---|---|---|
| `GET /api/v1/experience/state` | **已实现** `wizard.go:107` | 聚合向导+节点状态 |
| `GET /api/v1/experience/capabilities` | **已实现** `capabilities_api.go` | form + features；经 `config form --json` |
| `POST /api/v1/acceleration/install` | **已实现** `acceleration_api.go:19` | PC runtime 安装 |

### acceleration（桌面加速）

| 端点 | 状态 |
|---|---|
| `GET /api/v1/acceleration/status` | **已实现** |
| `PUT /api/v1/acceleration` | **已实现** |
| `POST /api/v1/acceleration/apply` | **已实现** |
| `GET /api/v1/acceleration/domains` | **已实现**（基线经执行器 `dns-hook domains status`） |
| `POST /api/v1/acceleration/domains/refresh` | **已实现**（执行器 `dns-hook domains refresh`；下载不进 mynetd） |

### node（节点身份、运行态、对端、路由事实）

| 端点 | 状态 |
|---|---|
| `GET /api/v1/node-status` | **已实现** |
| `GET /api/v1/config/identity` | **已实现** |
| `GET /api/v1/nodes` | **已实现**（本机会话表） |
| `GET /api/v1/status` | **已实现** |

### diagnostics（日志、检查、导出）

| 端点 | 状态 |
|---|---|
| `GET /api/v1/logs` | **已实现** |
| `GET /api/v1/system.json` | **已实现** |
| `POST /api/v1/command` | **已实现**（执行器） |
| `GET /api/v1/experience/capabilities` 中的 check/export 钩子 | **待实现** |

云端认证失效必须返回 `401 + need_login=true`，UI 显示重新登录入口，**禁止当成空数据**。

## 5. 布局规范（冻结）

- 页面最大宽度 1280px，内容区 12 列栅格。
- 默认「上 2 下 1」：顶部状态摘要；中部左右两张主卡；底部跨列详情卡。
- 宽屏主卡 `6+6`；超宽屏允许 `4+4+4`；&lt;900px 单列。
- 卡片内只保留一个主动作。
- 状态必须文字 + 颜色双重表达。

## 6. Capability 门控（冻结）

- 高级页面只在 capability **明确声明**支持时显示。
- **禁止**用操作系统名称在前端硬编码判断。
- capability 来源：`GET /api/v1/experience/capabilities`（待实现时，前端可临时读
  `experience/state` 的 `form` / platform 字段，但不得扩展猜测逻辑）。

期望 capability 形状（冻结）：

```json
{
  "form": "pc | gw",
  "platform": "windows | darwin | linux | openwrt | ...",
  "features": {
    "desktop_acceleration": true,
    "dns_hook": true,
    "proxy_policy": false,
    "acl": false,
    "routes_edit": false,
    "plugins": false
  }
}
```

## 7. 配置交互（冻结）

所有配置页：**读取 → 编辑草稿 → 校验 → 保存 → 应用 → 刷新事实状态**。

1. 进入页面只读事实状态。
2. 用户编辑本地草稿，未保存显示 dirty。
3. 保存按钮只提交合法配置。
4. 应用失败保留草稿并提供重试。
5. 离开页面有未保存变更必须提示。

## 8. 验收（对应 playbook §10）

- 1280 / 900 / 640 三种宽度无横向溢出。
- 所有异步操作有 loading / 成功 / 失败 / 重试。
- 云端 401、网络断开、本机服务停止、DNS hook 未安装均有明确界面。
- 桌面用户看不到 gateway-only 高级配置。
- 前端单元测试覆盖状态映射、表单校验、优先级排序、错误恢复。
- 构建产物可嵌入 `mynetd`，并通过本机 API smoke。

## 9. M0 冻结清单

- [x] 本映射文件（页面、状态、组件、API、布局、capability）
- [x] playbook §2–§8 视为冻结规范，修改需显式修订本文件
- [x] **禁止新增手写 HTML 业务页面**（`index.html` 进入兼容维护期）

## 10. 实施进度（mynet_gui）

| 里程碑 | 状态 | 落点 |
|---|---|---|
| M0 冻结 | ✅ | 本文件 |
| M1 壳/设计系统/API client | ✅ | `localApi.ts`、`experience.ts`、`mynet/*`、`LocalLayout`、`/*` |
| M2 首页与初始化 | ✅ 壳 | `LocalHomePage` / `LocalOnboardingPage` 绑 `experience/state` |
| M3 桌面加速三卡片 | ✅ 壳 | `LocalAccelerationPage` 出口/DNS/域名 + dirty/apply |
| M4 节点/网络/诊断 | ✅ 网关页落地 | `LocalOverviewPage`/`Peers`/`Network`/`Split`/`Exit`/`Services`/`Diagnostics` + capability 门控 |
| M5 收敛与 smoke | ✅ 首批 | `index.html` 纯跳转 SPA（无 fallback）；路径去掉 /local。**未做**：真浏览器三宽度实测、安装包/嵌入二进制联调 |

**本机 API 客户端口径**：能力面 = 同源 `/api/v1`（localapi 自有端点）；
平台面 = `/gui/api/v1`（**本机 relay**，剥前缀后经 gateway 转后端）。两者都在 8096。

**后端契约（2026-10-27 补齐）**：`GET /experience/capabilities`、`GET/POST /acceleration/domains[/refresh]`
已落 `localapi` + 执行器 `mynet proxy dns-hook domains status|refresh`。
capabilities 缺失时前端按桌面默认隐藏 gateway-only 项，不猜 OS。
