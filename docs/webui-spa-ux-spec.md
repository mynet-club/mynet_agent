# 本机 WebUI SPA 体验规范（SSOT）

> 适用：`mynet_gui` 内嵌本机面（`/gui/*`，服务由 `mynetd` 8096）。
> 取代：[webui-local-ux.md](webui-local-ux.md) 的旧 HTML 壳层与 [webui-ia.md](webui-ia.md) 的双层 Tab 布局；
> 迁移范围见 [webui-spa-playbook.md](webui-spa-playbook.md) / [webui-spa-page-map.md](webui-spa-page-map.md)。
> 不改的口径仍以 AGENTS.md 与 `spec/local-api.md` 为准。

## 1. 定位与原则

1. **地址即身份**：`127.0.0.1` / `localhost` 即本机面，路径不再带 `/local`。
2. **信息面与操作面分离**：信息区只读展示；配置/危险操作走「入口 → 弹窗/折叠面板」。
3. **一屏先回答「能不能用」**：顶部状态横幅 + 一句人话 + **唯一**主操作。
4. **低频操作不占版面**：密钥、切换节点、向导 → 入口按钮/菜单，不常驻表单。
5. **状态由后端算，前端只渲染**：不自行推导生命周期；`state/reason_code/message/retryable`。
6. **登录失效必须可见**：云端 401 / `need_login` → 横幅「需要登录 → 去登录」，不得吞成 502。

## 2. 信息架构与导航

侧栏顺序固定：

```
首页 · 节点 · 网络 · 设置 · 加速 · 诊断 · 日志 · 网络区
```

| 页 | 路由 | 职责 | 形态 |
|---|---|---|---|
| 首页 | `/home` | 状态一言之、启停/重启/修复、关键指标、对端摘要 | 信息 + 页头主操作 |
| 节点 | `/node` | 身份、当前设备、网络情况；管理菜单 | 信息卡（纯展示）+ `管理 ▾` |
| 网络 | `/network` | Tab：对端列表 / 对端路由 / 网段路由 / 拓扑 / 系统路由 | Tab 信息卡 |
| 设置 | `/settings` | 驱动（只读：node/address/route.conf）、运行形态（PC/GW）、parity、导出 | 信息 + 表单卡 |
| 加速 | `/acceleration` | 出口、DNS、域名、ad-block | 信息卡（卡头「配置」展开操作面） |
| 诊断 | `/diagnostics` | 运行检查、原始状态 | 操作 + 原始 |
| 日志 | `/logs` | 运行日志、清空 | 列表 |
| 网络区 | `/console` | 社区域 iframe（`/gui/app/*`，**不得嵌 `/gui/` 根**） | iframe |

网关（`form=gw`）额外：总览 / 对端 / 分流与 DNS / 出口与 ACL / 服务与插件。

## 3. 状态横幅（全站唯一强视觉）

| 状态 | 横幅 | 主操作 |
|---|---|---|
| `needs_login` | 需要登录 | **去登录** → `/onboarding` 登录步 |
| `needs_configuration` | 需要完成节点配置 | 去配置 → `/onboarding` |
| `needs_install` | 本机能力未就绪 | 去处理 → `/onboarding` |
| `degraded` | 降级运行 | 查看详情 → `/diagnostics` |
| `error` | 错误信息 + reason_code | 可重试时「重试」 |
| `ready` | 不显示 | — |

规则：
- 横幅动作**以横幅状态为准**，不依赖 `primary_action`（云端失效时它可能为空）。
- 云端会话失效优先于本机 `READY`：登录态覆盖运行态横幅。
- 执行中：动作按钮 `disabled` + 当前按钮 spinner。

## 4. 信息面 / 操作面

### 4.1 信息面
- 只使用文本/定义列表/徽章（`dl`/`fact-table`），**禁止输入框、下拉、开关**。
- 每卡一个主题，字段三列以内；未知值统一 `—`。
- 节点名优先：名称 → 云端名称 → `设备 .末段` → 短 ID（≤8 位 + …）。**永不把 16 位 ID 当主标签**。
- **设备名自动缓存**：mynetd 落盘 `var/node_names.json`；页面读取时缺条目或超过 10 分钟会自动静默拉取补齐（失败冷却 60s 保留旧名）。顶栏刷新是**强刷**（忽略缓存直接拉）。

### 4.2 操作面
- 入口：卡片右上「配置」/ 页头「管理 ▾」/ 弹窗。
- 加速页：信息卡（出口/DNS/域名）→「配置」展开对应面板；DNS 面板必须显示**实际地址**，不只序位。
- 域名面板：云端清单默认读本机缓存（`GET /acceleration/domains`）；「刷新云端」才访问 URL；大框上方提供搜索，命中实时写入框内。
- 危险操作（停止、卸载、写入/生成密钥）二次确认，文案说清后果。

## 5. 组件与视觉

- 共享组件：`MynetStatusBanner` / `MynetMetricCard` / `MynetConfigCard` / `MynetFieldRow` / `MynetAsyncState` / `MynetErrorPanel` / `MynetTopology`。
- 页最大宽 1280px；<900px 单列；卡片圆角 12px；状态必须文字 + 颜色双表达。
- 顶栏保持 44px 高：品牌 + 形态徽章 + 会话面包屑 `网络区 → 用户 → 节点名` + 语言/主题/刷新。

## 6. 异步与错误

- 统一：`loading / ready / degraded / needs_login / needs_configuration / needs_install / applying / error`。
- 局部失败不挡整页（如名称映射失败仍渲染对端表），但**认证失效必须上抛横幅**。
- 名称/清单类请求统一经 `LocalApiError`（401 与文案嗅探 → `needLogin`）。

## 7. 验收清单

- [x] 八页均「信息面无输入控件」（AUDIT_PASS，2026-10-27）
- [x] 横幅动作可点且落在正确页面（登录/配置/诊断）— LOGIN_FLOW_PASS
- [x] 云端 401 时出现「去登录」，点击进入登录步
- [x] 加速页 DNS 显示地址；域名搜索实时过滤
- [x] 对端/拓扑显示名称或 `设备 .N`，不显示裸长 ID
- [x] 1280 / 900 / 640 无横向溢出（AUDIT_PASS）
- [x] `make check`、GUI vitest、类型棘轮绿

---

## 8. 状态判据（并入自 webui-local-ux.md §1.1，仍有效）

状态由**磁盘产物 + 进程 + 内核**共同判定；前端不推导。

| 状态 | 判据（须全部满足） | 探测来源 |
|---|---|---|
| UNINSTALLED | `gnb` 不在 PATH 且不在 install 足迹 | `which gnb` / `mynet driver list` |
| INSTALLED | 二进制在，但无节点配置 | `nodeConfigured()`（认 D4-E 布局） |
| CONFIGURED | 节点配置 ∧ bundle ∧ 驱动配置非空 | `wizard/state` |
| RUNNING | 进程活 ∧ tun 有 overlay IP ∧ 每个对端 /32 路由存在 | `node-status` |
| DEGRADED | 进程活，但路由/IP/对端任一缺失 | `node-status.missing_routes` |
| FAILED | `service start` 非零，或启动后窗口内无进程 | `node-status.state` |

**一言之 + 唯一主按钮**（后端 `one_line` / `primary_action` 权威）：

| 状态 | 一言之 | 主按钮 |
|---|---|---|
| 未安装 | 未安装 gnb 驱动 | 安装 |
| 已安装 | 已安装，尚未配置节点 | 去配置 |
| 已配置 | 已配置 x.x.x.x，隧道未启动 | 启动隧道 |
| 运行中 | 运行中 · utun4 · IP · 对端 N/N 在线 | 重启隧道 |
| 降级 | 进程在，但有路由缺失 | 修复路由 |
| 失败 | 启动失败：原因 | 重试启动 |

## 9. 按钮可用性矩阵（并入自 webui-local-ux.md §3）

| 全局态 | 安装 | 配置向导 | 启动 | 重启 | 停止 | 修复路由 | 卸载 |
|---|---|---|---|---|---|---|---|
| UNINSTALLED | **主** | 可点 | 禁用 | 禁用 | 禁用 | 禁用 | 隐藏 |
| INSTALLED | 隐藏 | **主** | 禁用 | 禁用 | 禁用 | 禁用 | 可点 |
| CONFIGURED | 隐藏 | 可点 | **主** | 禁用 | 禁用 | 可点 | 可点 |
| RUNNING | 隐藏 | 可点 | 隐藏 | **主** | 可点 | 隐藏 | 可点 |
| DEGRADED | 隐藏 | 可点 | 可点 | **主** | 可点 | 可点 | 可点 |
| FAILED | 视缺口 | 可点 | **主·重试** | 禁用 | 禁用 | 可点 | 可点 |
| 执行中 | 全 disabled，当前按钮 spinner | | | | | | |

补充：错误就地展示（横幅 + 按钮下红字）；停止/卸载/密钥写入二次确认；同一时刻只有一个 primary。

## 10. route.conf 约定（并入自 webui-local-ux.md §0）

- 每条对端 **/32 主机行**，第一条恒为**本机**；网段行仅网关公告 LAN 时出现。
- 行格式 `<nodeid>|<ip>|<点分掩码>`；overlay 地址**绝不用网段行**。
- 页面展示：目标 / 下一跳 / 接口 / 状态；缺失项进「缺失路由」并在横幅降级。

## 11. 页面分区映射（旧区 → 新页）

| 旧区（webui-local-ux §2） | 新落点 |
|---|---|
| ① 状态横幅 | 全站 `MynetStatusBanner` |
| ② 节点身份 | 节点页「本机身份」 |
| ③ 安装与配置 | 节点页「当前设备」+ 管理菜单 |
| ④ 运行 | 首页指标 + 节点页「网络情况」 |
| ⑤ 对端与路由 | 网络页（拓扑 / 对端 / 网段 / 原始） |
