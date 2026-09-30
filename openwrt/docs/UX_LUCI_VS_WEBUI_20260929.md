# LuCI 实测 vs 本机 WebUI — 交互体验对比与建议

> 日期：2026-09-29
> 载体：QEMU VM `openwrt-qemu` / 192.168.101.2 · OpenWrt 24.10.1 + Argon 主题
> 对照：`mynet_agent/mynetd/localapi/webui`（本机 `/ui/`）
> 方法：浏览器实测 DOM 流程 + SSH 对照真实进程状态

---

## 1. 流程实测结论（能不能跑通）

| 流程 | 结果 | 证据 |
|---|---|---|
| LuCI 登录 | ✅ | root + 口令 → Dashboard |
| Services → MyNet 菜单 | ✅ | 一级菜单 + 5 子页 |
| Dashboard 状态展示 | ✅ | 设备/依赖/GNB 状态/路由数 |
| **Stop** | ✅ | UI 显示 Stopped；SSH 确认 gnb/gnb_es 已停 |
| **Start** | ✅ | UI Running + Uptime 00:00:02 + tun UP + 路由 1/4 |
| Node 配置/密钥 | ✅ | conf ✓、密钥脱敏显示 |
| Operations 五 Tab | ✅ | GNB/Network/Peers/Log/Diagnostics |
| Settings（GNB 二进制） | ✅ | Bundled vs System，路径+版本清晰 |
| Diagnostics 入口 | ✅ | Run Diagnostics 按钮 + 说明 |
| 云端节点信息 | ⚠️ | **`API error: 401: Unauthenticated.` 原样露出** |

**结论：本机运维主路径（看状态 → 启停 → 配置/诊断）在 LuCI 上完整可跑。**

---

## 2. 信息架构对比

| 维度 | **LuCI（路由器）** | **WebUI（本机）** | 评价 |
|---|---|---|---|
| 定位 | 路由器运维面 | 节点面板（桌面/服务器） | 分工正确 |
| 结构 | 一级菜单 + 页内 Tab | 三行壳：面包屑 + 页面 Tab + 核心卡片 | WebUI 更现代 |
| 首屏 | Dashboard 信息密度高 | 概览核心卡片更聚焦 | 各有优势 |
| 启停入口 | Dashboard + Operations **两处** | 概览一处 | LuCI 重复 |
| 实时性 | 有轮询（15s/60s） | **几乎无轮询** | LuCI 更好 |
| 危险操作 | 部分 confirm | 部分 confirm | 都不全 |
| 错误展示 | 直接露出 API 原文 | toast 自逝 | **两边都弱** |

---

## 3. 用户习惯差异（真实体感）

### LuCI 用户（路由器主人）
- 习惯 **LuCI 全局侧栏**（Status/System/Services/Network）——MyNet 只是其中一个服务
- 期望「看得见接口、防火墙、依赖」——Dashboard 做到了
- 期望 **Start/Stop 立刻反映到状态行** —— 本轮实测 OK
- 对 **`API error 401`** 这种裸错误会懵：不知道是该登录云端还是坏了

### WebUI 用户（本机节点）
- 习惯 **单页 SPA + 核心卡片**，少跳转
- 期望「当前网络 / 状态 / 主操作」一眼可见 —— 三行壳是按这个设计的
- 期望长任务有进度；现在 15s 超时易误判失败
- 期望状态别过期——缺轮询/推送

---

## 4. 问题清单（按优先级）

| ID | 面 | 问题 | 建议 |
|---|---|---|---|
| **U1** | LuCI | Node 页裸露 `API error 401: Unauthenticated` | 改成「云端未登录 / 凭证过期 → 去登录」，不要把 HTTP 原文塞进表格行 |
| **U2** | LuCI | Dashboard 与 Operations 双启停入口，语义略异 | 统一到一条 `service/op`，Dashboard 只留主操作 |
| **U3** | WebUI | 无状态刷新（展示层） | 轻量拉 `/api/v1/status` 快照（不是再轮询 OS） |
| **U4** | WebUI | 长命令 15s 超时假失败 | 异步 job 或加长 + 进度 |
| **U5** | 两边 | 危险操作 confirm 不全 | 停服务/改密钥/覆盖配置统一确认 |
| **U6** | 两边 | 错误只 toast / 裸文本 | 常驻错误区 + 可复制 stderr |
| **U7** | LuCI | 多处 `Start`/`Start GNB` 按钮可及名重复 | 明确 aria/唯一文案（无障碍+自动化） |

---

## 5. 建议（体验向，不改架构）

### 5.1 LuCI 侧（路由器）
1. **云端错误本地化**：401/网络失败 → 「未连接云端」「凭证过期」+ 一键去登录；细节进折叠「原始数据」
2. **启停收敛**：Dashboard 只保留主操作（启动/停止/重启）；Operations 面向深度运维
3. **状态灯语义**：Running/Stopped/Starting/Failed 分色；Start 后短暂「启动中」而非立刻跳终态
4. **危险操作**：Stop / Generate New Key / Import 覆盖 二次确认，文案写清影响（隧道断、对端需换钥）

### 5.2 WebUI 侧（本机）
1. **展示刷新**：5–10s 拉 `/api/v1/status`（mynetd 已有采集，不 fork）
2. **长任务**：`apply`/`switchConfig` 用已有 steps 字段做进度条，超时改异步
3. **向导补齐**：登录 → **选节点** → **下载配置**（与 `wizard/state` 对齐）
4. **错误区**：最近 N 条失败含 stderr，替代纯 toast

### 5.3 跨端一致（用户习惯对齐）
| 约定 | 建议 |
|---|---|
| 主操作位置 | 概览/Dashboard 核心卡片右侧 |
| 状态色 | 绿=Running / 灰=Stopped / 黄=过渡 / 红=Failed |
| 反馈 | 成功 Toast 1.5s + 局部刷新；失败常驻 |
| 危险操作 | 统一 confirm 标题「要停止隧道吗？」+ 后果说明 |
| 术语 | 「节点 / 隧道 / 路由 / 转发出口」两端同词（现 LuCI 混用 GNB Service / VPN） |

---

## 6. 实测摘录

```text
# Start/Stop 经 LuCI 真实生效
Stop  → UI "Stopped · gnb · Boot: Enabled"
        SSH: gnb stopped, no gnb_es
Start → UI "Running · Uptime 00:00:02 · gnb_tun UP · Routes 1/4"

# Node 页问题
row "API error: api error 401: Unauthenticated."   ← U1

# 按钮歧义（自动化/无障碍）
role=button[name=Start] matched 3 elements:
  "Start GNB" | "Start" | "Restart"               ← U7
```

---

## 7. 总结

| 问题 | 答案 |
|---|---|
| 流程能跑通吗？ | **能**——登录、看状态、启停、配置、诊断主路径全通 |
| 用户习惯？ | LuCI=「路由器里一个服务」；WebUI=「节点专属面板」 |
| 最该改什么？ | **U1 裸 API 错误**、**U3/U4 WebUI 实时与超时**、**U5/U6 反馈与确认** |

LuCI 在「看得见系统、状态会自己动」上目前**优于** WebUI；WebUI 在「结构清晰、任务聚焦」上更好。
把两边的反馈层（错误、确认、刷新）对齐，用户体验会明显顺。
