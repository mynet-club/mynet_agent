# MyNet WebUI SPA 重构规范与执行 Playbook

状态：规划阶段，暂不编码。  
目标：将整个 MyNet WebUI 从嵌入式手写 HTML 迁移为可维护的 Vue 3 + Quasar SPA，同时保留 `mynetd/localapi` 作为本机能力边界。

## 1. 目标与边界

WebUI 需要同时服务两类用户：

- 桌面用户：Windows、macOS、未来 Linux desktop，本机模式，优先完成“能不能用、现在什么状态、下一步做什么”。
- 网关/客户端用户：Linux gateway、OpenWrt，允许看到分流、出口、ACL、路由和诊断等高级能力。

前端不直接读取本机文件、进程或系统路由。所有本机状态通过 `mynetd/localapi` 获取；云端数据通过已有 relay/API 获取。页面不得根据零散字段自行推导生命周期状态。

## 2. 技术基线

统一使用现有 `mynet_gui`：

- Vue 3 + TypeScript
- Quasar 组件和响应式栅格
- Pinia 管理会话、节点、配置和运行态
- Vue Router 管理页面级导航
- 现有主题、国际化、通知、断线重连能力继续复用

`mynetd/localapi/webui/index.html` 进入兼容维护期，只保留最小 fallback 页面和 API 调试入口；新交互不得继续堆入这份文件。

## 3. 信息架构

### 桌面模式

桌面用户默认看到四个一级页面：

1. **首页**：本机连接、加速状态、出口、DNS hook、最近错误。
2. **加速**：出口选择、DNS 优先级、域名清单。
3. **节点**：本机身份、连接状态、版本、更新/修复。
4. **设置**：启动行为、语言、主题、诊断导出。

桌面模式隐藏 gateway-only 的分流模式、复杂 ACL、路由编辑、插件原始配置和服务端策略。

### 网关/OpenWrt 模式

保留并整理为：

1. 总览
2. 节点与对端
3. 网络/路由
4. 分流与 DNS
5. 出口与 ACL
6. 服务与插件
7. 诊断与日志
8. 系统设置

高级页面只在 capability 明确声明支持时显示，不能用操作系统名称在前端硬编码判断。

## 4. 页面布局规范

- 页面最大宽度 1280px，内容区使用 12 列栅格。
- 默认采用“上 2 下 1”的结构：顶部状态摘要；中部左右两张主卡；底部一张跨列详情卡。
- 宽屏：主卡 `6 + 6`；超宽屏允许 `4 + 4 + 4`。
- 小于 900px：自动单列流式布局。
- 卡片内只保留一个主动作；次要动作放入工具区或菜单。
- 表单标签、输入和帮助文字使用统一密度，禁止大面积空白卡片。
- 状态必须有文字和颜色双重表达，不能只靠颜色。

## 5. 组件规范

建立共享组件层：

- `MynetPageHeader`
- `MynetStatusBanner`
- `MynetMetricCard`
- `MynetConfigCard`
- `MynetFieldRow`
- `MynetPriorityList`
- `MynetNodeSelect`
- `MynetDomainEditor`
- `MynetCloudPreview`
- `MynetAsyncState`
- `MynetErrorPanel`
- `MynetConfirmAction`

组件只接收结构化 props，不直接调用 axios。API 调用放在 composable/store 中，组件只处理展示、校验和事件。

## 6. 状态模型

所有页面统一使用以下状态层：

- `loading`：首次读取
- `ready`：数据完整且可操作
- `degraded`：部分数据不可用，但页面仍可操作
- `needs_login`：需要登录/续期
- `needs_install`：本机能力未安装
- `applying`：配置正在应用
- `error`：操作失败，可重试

后端返回 `state`、`reason_code`、`message`、`retryable`、`data`。前端不得通过错误字符串判断状态。

## 7. 配置交互规范

所有配置页面采用“读取 → 编辑草稿 → 校验 → 保存 → 应用 → 刷新事实状态”流程：

1. 进入页面只读事实状态。
2. 用户编辑本地草稿，未保存时显示 dirty 标识。
3. 保存按钮只提交合法配置。
4. 应用失败必须保留草稿并提供重试。
5. 页面离开时有未保存变更必须提示。

## 8. API 契约规划

本机 API 分成四组：

- `experience`：初始化、能力、统一状态、安装/修复。
- `acceleration`：桌面加速配置、DNS hook、出口、域名。
- `node`：节点身份、节点运行态、对端和路由事实。
- `diagnostics`：日志、检查、导出和最近失败原因。

桌面页面优先使用聚合 API，避免一个页面拼接多个低层接口。推荐契约：

```text
GET  /api/v1/experience/state
GET  /api/v1/experience/capabilities
GET  /api/v1/acceleration/status
PUT  /api/v1/acceleration
POST /api/v1/acceleration/apply
GET  /api/v1/acceleration/domains
POST /api/v1/acceleration/domains/refresh
```

云端认证失效必须返回 `401 + need_login=true`，UI 显示重新登录入口，禁止当成空数据。

## 9. 迁移里程碑

### M0：冻结规范

- 冻结本文件、状态模型、组件命名和 API 分组。
- 列出现有 HTML 页面与 Vue 页面映射。
- 禁止新增手写 HTML 业务页面。

### M1：SPA 壳与设计系统

- 建立统一页面壳、导航、响应式栅格、卡片、表单和状态组件。
- 接入本机 API client 与统一错误处理。
- 完成桌面/网关 capability 切换。

### M2：首页与初始化

- 使用 `experience/state` 重做首页和首次启动流程。
- 安装、配置、启动、修复统一为一个状态机。

### M3：桌面加速

- 三卡片布局：出口、DNS、域名。
- 出口只显示后端确认可用的节点。
- DNS 四行优先级和实际候选值完整可编辑。
- 云端清单刷新、预览、认证错误和离线缓存。

### M4：节点、网络、诊断

- 节点/对端/路由页面迁移。
- 日志、诊断、导出和修复动作统一。

### M5：旧页面收敛与发布

- 旧 HTML 只保留 fallback。
- 嵌入式资源、桌面包、OpenWrt 包统一构建。
- 完成桌面、小屏、网关三套 smoke 测试。

## 10. 验收标准

- 页面在 1280px、900px、640px 三种宽度下无横向溢出。
- 所有异步操作有 loading、成功、失败、重试状态。
- 云端 401、网络断开、本机服务停止、DNS hook 未安装均有明确界面。
- 桌面用户看不到 gateway-only 高级配置。
- 网关用户可以完整访问高级配置。
- 前端单元测试覆盖状态映射、表单校验、优先级排序和错误恢复。
- 构建产物可嵌入 `mynetd`，并通过本机 API smoke 测试。

## 11. 当前决策

- 不再继续扩展 `mynetd/localapi/webui/index.html` 的业务交互。
- 新 UI 以现有 `mynet_gui` 为唯一主实现。
- API 和状态机先于页面迁移；页面不猜事实。
- 每个里程碑完成后才切换一组路由，避免一次性大爆炸迁移。
