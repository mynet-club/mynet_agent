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
- `gateway` **不做鉴权**——只做透传 + 连接状态表，鉴权归 back
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

## 文档

- 规则：`mynet_ctl/docs/knowledge-base/AGENTS.md`
- 设计：`mynet-overview.md` · 仓库：`repo-migration.md` · 计划：`dev-plan.md`（§3 计划顺序表）
