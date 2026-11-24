# mynet_agent

MyNet 节点侧控制面（Go）。**一份源码，多套构建**。

## 架构：mynetd 纯通信，系统能力在执行器

```
mynetd/     纯通信层（常驻）：心跳组装 · WS + 身份密钥 · 取指令/结果上报 · 读 system.json · 命令分派
mynet/      执行器（偶发、用完即抛）：detector · metrics · apply · driver · service · commands
gateway/    边缘网关：WS（节点面）+ HTTPS（用户面）· 纯透传 · 不鉴权
shared/     共享：domain(BundleEnvelope) · config · logger · utils
```

**硬约束**（违反即架构瓦解，`make guard` 会拦）：
- `mynetd/` **不得**依赖系统能力（`detector`/`tools/`/`infrastructure`/`coordinator`/`node/`/`service/`/gopsutil）
- `gateway` **不做鉴权**——只做透传 + 连接状态表，鉴权归后端
- `relaypolicy` **只做路径白名单**，不含鉴权逻辑

## 开发

```bash
make install-hooks   # 装 git hooks（pre-commit 轻量 / pre-push 重量）
make guard           # 安全/结构/产物红线（秒级）
make check           # guard + lint + test
```

### WebUI：三种加载（发布只许 embed）

| 构建 | 命令 | 用途 |
|---|---|---|
| **embed** | `go build -tags embed` | **发布**（Makefile `pack` 就是这个） |
| **dev** | `go build -tags dev` | **仅开发**：从 `MYNET_WEBUI_DIR` 或 `mynetd/localapi/webui` 读盘，改前端不用重编 |
| **none** | `go build`（无 tag） | OpenWrt / 瘦构建，WebUI 不进包（LuCI 托管） |

```bash
# 开发热更新前端
go run -tags dev ./cmd/mynetd -listen 127.0.0.1:8096
# 打开 http://127.0.0.1:8096/ui/ ，直接改 mynetd/localapi/webui/ 即生效
```

⚠️ **`-tags dev` 不得进发布包**；`scripts/pack.sh` / Makefile `pack` 固定 `-tags embed`。

## 目录模型（两层架构：取消配置投影）

全平台统一只分两层（macOS/Linux 默认 `MYNET_HOME=/opt/mynet`，OpenWrt 默认 `/etc/mynet`，Windows 默认 `C:\ProgramData\MyNet`）：

1. **持久化配置与状态（`$MYNET_HOME/conf/` + `state/`）**：全系统唯一配置真相源，**不再向 `/var/run/mynet/` 复制任何配置文件**：
   - `conf/*.json`：mynet 自身配置（`mode.json`、`gateways.json`、`agent.json`）
   - `conf/<form>/<nodeId>/`（`form=local|online`）：`gnb` 与 `wg` 的**唯一配置目录**（密钥 + `node/route/address/proxy/utc.conf` 或 `wg.conf`），`gnb -c` 直接打开此处
   - `conf/utc/utcd.conf`：UTC 客户端唯一配置（ADR-0037 一端一配）
   - `state/`：持久化状态（`last-good`、`utc/` 历史水位与端侧 leases）
2. **运行态临时数据（`$MYNET_HOME/var/` + `logs/`）**：仅存与配置无关的临时产物（`bundle.json` 缓存、`system.json`、PID、socket、日志），随时可清空重建。

## 文档

- UTC 客户端：mynetd 通过 `mynet/utc/endpoint` 托管 `utcd --config`；默认单一配置为 `conf/utc/utcd.conf`（一端一配，旧 `hub.json` / `hubd` 已退役），可由 `MYNET_UTC_CONFIG` 指定，二进制位于安装前缀的 `bin/utcd`（OpenWrt 自动兼容 `/usr/sbin/utcd`）。utcd 自己认证、取池和维护消费水位。
  启动契约与未验收边界：[BOOT-0 客户端契约](../mynet_utc/docs/productization/BOOT0_CLIENT.md)；三节点现场：[接手手册](../mynet_utc/docs/productization/UTC_THREE_NODE_TAKEOVER_20261120.md)。

- 托盘优化：[Tray 渐进优化 Playbook](docs/TRAY_OPTIMIZATION_PLAYBOOK.md)（沿用现有 App/FSM，局部修复与交互优化）
- 规则：`mynet_ctl/docs/knowledge-base/AGENTS.md`
- 设计：`mynet-overview.md` · 仓库：`repo-migration.md` · 计划：`dev-plan.md`（§3 计划顺序表）
