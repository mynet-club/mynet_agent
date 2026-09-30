# MyNet LuCI — 核心流程图

> ⚠️ **已迁移**：业务操作契约已整合到 SSOT spec：
> - 向导流程 → [spec/wizard.md](../../mynet_ctl/docs/knowledge-base/spec/wizard.md)
> - 服务启停 → [spec/service.md](../../mynet_ctl/docs/knowledge-base/spec/service.md)
> - 代理分流 → [spec/proxy.md](../../mynet_ctl/docs/knowledge-base/spec/proxy.md)
>
> 本文档保留 Mermaid 流程图作为可视化参考，不再维护业务逻辑。

> 本文档使用 Mermaid 语法，可在 GitHub 页面直接渲染。

---

## 1. 安装向导流程（Wizard Flow）

首次访问或未配置时，用户通过向导完成初始化。

```mermaid
flowchart TD
    A[用户访问 /wizard] --> B{有登录凭证?}
    B -- 否 --> C[显示 Landing 页]
    C --> C1{用户选择}
    C1 -- MyNet 在线模式 --> D[跳转 /login 登录]
    C1 -- 离线模式 --> E[跳转 /guest]

    B -- 是 --> F{当前模式?}
    F -- guest --> E
    F -- mynet --> G{已有完整配置?}

    G -- 是 --> H[跳转 /node 节点页]
    G -- 否 --> I{已选 Zone?}

    I -- 否 --> J[显示 Zone 选择]
    J --> J1[加载 Zone 列表]
    J1 --> J2{只有1个 Zone?}
    J2 -- 是 --> J3[自动选择]
    J2 -- 否 --> J4[用户手动选择]
    J3 --> K
    J4 --> K

    I -- 是 --> K[显示 Node 选择]
    K --> K1[加载节点列表]
    K1 --> K2[用户点击 Use This Node]
    K2 --> K3[confirm 确认对话框]
    K3 -- 确认 --> L[生成 mynet.conf]
    L --> M[下载配置包 bundle API]
    M --> N[写入 node.conf + route.conf + address.conf + 密钥]
    N --> H

    D --> D1[输入邮箱 + 密码]
    D1 --> D2{登录成功?}
    D2 -- 是 --> D3[保存 Token + 凭证]
    D3 --> J
    D2 -- 否 --> D4[显示错误 + 重试]
```

---

## 2. 节点切换流程（Node Switch Flow）

从节点管理页或向导页切换到不同节点。

```mermaid
flowchart TD
    A[用户点击 Change Node] --> B{当前模式?}
    B -- MyNet --> C[跳转 /wizard?tab=zone]
    B -- Guest --> D[跳转 /guest]

    C --> E[选择 Zone]
    E --> F[选择新节点]
    F --> G[confirm 确认切换]
    G -- 确认 --> H[POST wizard_select_node]
    H --> I[generate_mynet_conf]
    I --> J[refresh_configs_bundle]
    J --> K{bundle API 可用?}
    K -- 是 --> L[单请求获取全部配置+密钥]
    K -- 否 --> M[fallback: 3次独立请求]
    L --> N[写入本地配置文件]
    M --> N
    N --> O[跳转 /node 节点页]

    D --> P[选择离线节点]
    P --> Q[点击 Use This Config]
    Q --> R[更新 local_node_id]
    R --> S[重新生成 mynet.conf + route.conf]
    S --> T[页面刷新]
```

---

## 3. GNB 自动安装流程（GNB Auto-Install Flow）

Settings 页面或 Dashboard 检测到 GNB 未安装时触发。

```mermaid
flowchart TD
    A[触发安装] --> B{gnb_ctl 已存在?}
    B -- 是 --> C[返回 already_ok]
    B -- 否 --> D{安装进程已运行?}
    D -- 是 --> E[返回 already_running]
    D -- 否 --> F[检测平台架构]

    F --> G[uname -m → 映射 gnb_arch]
    G --> H{架构可识别?}
    H -- 否 --> I[返回错误: 未知平台]
    H -- 是 --> J[预装依赖]
    J --> J1[bash / kmod-tun / curl-gnutls / ca-bundle]

    J1 --> K[拉取 apps.json 索引]
    K --> L[拉取 GNB manifest]
    L --> M[解析最新稳定版本]
    M --> N[选择对应架构资源包]
    N --> O[生成安装脚本]
    O --> P[后台执行: 下载 → 解压 → 安装]
    P --> Q[写入锁文件 + PID]
    Q --> R[返回 started + 版本信息]

    R --> S[前端轮询 install_status]
    S --> T{安装完成?}
    T -- 否 --> S
    T -- 是 --> U[刷新页面 → GNB 可用]
```

---

## 4. VPN 服务启动流程（Service Start Flow）

用户在 Dashboard 或 Service 页面启动 GNB VPN。

```mermaid
flowchart TD
    A[用户点击 Start VPN] --> B[POST api/vpn_start]
    B --> C["/etc/init.d/mynet start"]

    C --> D[rc.mynet init 脚本]
    D --> D1[读取 mynet.conf]
    D1 --> D2[定位 GNB 二进制]
    D2 --> D3{kmod-tun 已加载?}
    D3 -- 否 --> D4[modprobe tun]
    D3 -- 是 --> D5[启动 GNB 进程]
    D4 --> D5

    D5 --> E[GNB 绑定 TUN 接口]
    E --> F[执行 route.mynet apply]
    F --> F1[读取 route.conf]
    F1 --> F2[注入路由表项]

    F2 --> G[执行 firewall.mynet apply]
    G --> G1[配置 mynet zone]
    G1 --> G2[设置转发规则]
    G2 --> G3[firewall reload]

    G3 --> H{启动成功?}
    H -- 是 --> I[返回 success]
    H -- 否 --> J[分析错误 → 生成提示]
    J --> K[返回 error + hint]

    I --> L[前端刷新状态: ● Running]
```

---

## 5. 代理分流运行流程（Proxy Traffic Split Flow）

通过 GNB 隧道进行 nftables + 策略路由分流。代理流量**不走内核主路由表**，而是 fwmark → ip rule → table 200 → gnb_tun；同时**所有已知国外 DNS 服务器（8.8.8.8/1.1.1.1/9.9.9.9 等）通过系统 /32 主路由强制走 gnb_tun**，杜绝 DNS 污染。

```mermaid
flowchart TD
    A[用户点击 Enable / Start Proxy] --> B{GNB 正在运行?}
    B -- 否 --> C[返回错误: 先启动 GNB]
    B -- 是 --> D["proxy.start(opts) 读取 proxy_role.conf"]

    D --> D1["运行参数<br/>mode: client/server/both<br/>region: domestic/international/non_domestic<br/>dns_mode: none/redirect/resolv/split<br/>dns_server / dns_domestic_server / proxy_peers"]
    D1 --> D2["update_config() 持久化到 proxy_role.conf"]

    D2 --> E["route_inject() 向 GNB route.conf 注入 pipe 路由<br/>peer_nid|x.0.0.0|255.0.0.0 (/8)<br/>+ 172.x/192.x 用 /16 跳过私有段<br/>共 ~714 条/peer<br/>告诉 GNB 数据层将公网 IP 通过隧道转发"]

    E --> F["proxy.sh generate<br/>读取 interip.txt / chinaip.txt<br/>生成 proxy_route.conf (nft 元素声明)"]

    F --> G["写入 proxy_policy_params.env<br/>FW_TYPE / TABLE_ID=200 / FWMARK=0xc8<br/>NODE_REGION + MATCH_MODE<br/>(non_domestic ⇒ MATCH_MODE=inverted)"]

    G --> H["route_policy.sh start"]
    H --> H1["ensure_route_table → /etc/iproute2/rt_tables 注册 mynet_proxy"]
    H1 --> H2["add_default_routes<br/>ip route add default via peer_vip dev gnb_tun table 200<br/>(多 peer 时使用 nexthop 多路径)"]
    H2 --> H3["add_ip_rules<br/>ip rule add fwmark 0xc8 table 200 prio 31800"]
    H3 --> H4["fix_wan_gateway_route<br/>WAN 网关 /32 host route → wan dev<br/>(防止 GNB 学到的 /24 覆盖 WAN 直连)"]

    H4 --> H5{MATCH_MODE?}
    H5 -- normal --> I1["nft mangle_prerouting<br/>ip daddr @mynet_proxy → mark 0xc8<br/>(命中 IP 集合 ⇒ 走代理)"]
    H5 -- inverted --> I2["nft mangle_prerouting<br/>ip daddr != @mynet_proxy → mark 0xc8<br/>(未命中国内集合 ⇒ 走代理)"]

    I1 --> K
    I2 --> K

    K{DNS_MODE?}
    K -- none --> L1[保持现有 dnsmasq 配置不变]
    K -- redirect --> L2["nft dns_intercept (nat prerouting dstnat)<br/>iifname br-lan udp/tcp dport 53<br/>dnat → peer_vip:53"]
    K -- resolv --> L3["改写 /tmp/resolv.conf.d/resolv.conf.auto<br/>nameserver = peer_vip"]
    K -- split --> L4["dns_split.sh setup<br/>dnsmasq.conf 写入 server=223.5.5.5 (国内)<br/>+ /etc/dnsmasq.d/gfwlist.conf<br/>每条 server=/{domain}/8.8.8.8 (国外域名)<br/>+ extra-domains.conf (npm/docker/openai..)<br/>dnsmasq reload"]

    L1 --> M
    L2 --> M
    L3 --> M
    L4 --> M["proxy_state.json 写入 start_ts / mode / region / dns_mode"]
    M --> N["route_foreign_dns<br/>ip route replace 8.8.8.8/32 dev gnb_tun<br/>+ 8.8.4.4 / 1.1.1.1 / 1.0.0.1 / 9.9.9.9 / 208.67.x<br/>(/32 优先级高于 /8 GNB 路由,确保 DNS 不被污染)"]
    N --> O["register_firewall_include<br/>(防火墙重启自动恢复策略路由)"]
    O --> P[返回 success]

    style A fill:#e8f5e9
    style C fill:#ffebee
    style N fill:#fff3e0
    style L4 fill:#e3f2fd
    style P fill:#e8f5e9
```

### 三种 Region 匹配模式

| Region | IP 集合数据源 | nft 匹配条件 | 适用场景 |
|---|---|---|---|
| `domestic` (默认) | `interip.txt` (国际/海外 IP, ~17000 条) | `ip daddr @set → mark` | 节点在国内,海外流量走 peer |
| `international` | `chinaip.txt` (国内 IP) | `ip daddr @set → mark` | 节点在海外,国内流量走 peer (回国加速) |
| `non_domestic` (新) | `chinaip.txt` (国内 IP) | `ip daddr != @set → mark` | **反向放行**: 集合外的所有流量都走代理,适合"全局代理但国内直连"场景 |

### 四种 DNS 模式

| dns_mode | 工作方式 | LAN 客户端 DNS 路径 | 适用场景 |
|---|---|---|---|
| `none` | 不修改任何 DNS 配置 | 现有 dnsmasq → 现有上游 | 已有自定义 DNS 方案 |
| `redirect` | nft DNAT 拦截 br-lan dport 53 | DNAT → peer_vip:53 (绕过 dnsmasq) | 强制使用 peer 的 smartdns |
| `resolv` | 覆写本机 resolv.conf.auto | LAN 仍走 dnsmasq → peer_vip | 仅修改路由器自身 DNS |
| `split` (新) | dnsmasq 默认走国内 + GFW list 走国外 | dnsmasq 智能分流 (国内 CDN+海外干净 IP) | **推荐**: 无需 peer smartdns 即可分流 |

### 国外 DNS 服务器系统级路由（始终生效）

无论 `dns_mode` 设置如何,代理 `start()` 时都会执行 `route_foreign_dns`:

```text
ip route replace 8.8.8.8/32       dev gnb_tun
ip route replace 8.8.4.4/32       dev gnb_tun
ip route replace 1.1.1.1/32       dev gnb_tun
ip route replace 1.0.0.1/32       dev gnb_tun
ip route replace 9.9.9.9/32       dev gnb_tun
ip route replace 208.67.222.222/32 dev gnb_tun
ip route replace 208.67.220.220/32 dev gnb_tun
```

**作用**: /32 主表路由优先级高于 GNB 注入的 /8,确保任何客户端发往这些公共 DNS 的查询直接通过隧道,**不经过 ISP DNS 拦截/污染**。`stop()` 时通过 `unroute_foreign_dns` 全部撤销。

### DNS 流量路径详细对比

| 流量来源 / dns_mode | none | redirect | resolv | split |
|---|---|---|---|---|
| LAN 客户端 → 53 | dnsmasq → 原上游 | DNAT → peer_vip | dnsmasq → peer_vip | dnsmasq 分流 (国内 223.5.5.5 / 国外 8.8.8.8) |
| 路由器自身 → 53 | dnsmasq → 原上游 | dnsmasq → 原上游 (OUTPUT 不 DNAT) | resolver → peer_vip | dnsmasq 分流 |
| 客户端直连 8.8.8.8 | /32 主表 → gnb_tun | /32 主表 → gnb_tun | /32 主表 → gnb_tun | /32 主表 → gnb_tun |

### 代理停止流程

```mermaid
flowchart TD
    A[用户点击 Stop / Disable] --> B["proxy.stop()"]
    B --> C["unroute_foreign_dns<br/>删除所有 8.8.8.8/32 等主表路由"]
    C --> D["route_policy.sh stop"]
    D --> D1["stop_dns_intercept<br/>删除 nft dns_intercept chain<br/>清理 dns_route_pin 临时记录的 DNS /32"]
    D1 --> D2["stop_server_mode<br/>删除 server_postrouting/forward chain"]
    D2 --> D3["unroute_foreign_dns (二次保险)"]
    D3 --> D4["nft delete table inet mynet_proxy<br/>(一次性清掉所有 set/chain)"]
    D4 --> D5["循环 ip rule del 直到无 lookup mynet_proxy"]
    D5 --> D6["ip route flush table 200"]
    D6 --> D7{DNS_MODE?}
    D7 -- split --> D8["rm /etc/dnsmasq.d/gfwlist.conf<br/>清理 dnsmasq.conf #mynet-dns-split-* 段<br/>恢复 noresolv=0 + 默认国内 DNS"]
    D7 -- redirect/resolv --> D9["uci 恢复 dhcp.dnsmasq.server=223.5.5.5,119.29.29.29"]
    D7 -- none --> D10[跳过 dnsmasq 恢复]
    D8 --> E
    D9 --> E
    D10 --> E["proxy.route_restore()<br/>strip GNB route.conf 中的 #----proxy begin/end---- 段<br/>(含 proxy-server 段)"]
    E --> F["rm proxy_state.json<br/>uci delete firewall.mynet_proxy include"]
    F --> G[返回 success]
```

---

## 6. 页面导航总览（Navigation Map）

```mermaid
flowchart LR
    subgraph 菜单页面
        IDX[Dashboard<br/>/index]
        NODE[Node<br/>/node]
        SVC[Operations<br/>/service]
        PLG[Plugins<br/>/plugin]
        SET[Settings<br/>/settings]
    end

    subgraph 功能页面
        WIZ[Wizard<br/>/wizard]
        LOGIN[Login<br/>/login]
        GUEST[Guest<br/>/guest]
        PROXY[Proxy<br/>/proxy]
    end

    subgraph "兼容重定向 (旧路由)"
        ZONES["/zones"] -.-> NODE
        NODES["/nodes"] -.-> NODE
        STATUS["/status"] -.-> IDX
        DIAG["/diagnose"] -.-> SVC
        LOG["/log"] -.-> SVC
        NET["/network"] -.-> SVC
        GNB_MON["/gnb"] -.-> SVC
        NM["/node/manager"] -.-> NODE
    end

    IDX --> NODE
    NODE -- "Change Node" --> WIZ
    NODE -- "Change Node (Guest)" --> GUEST
    PLG -- "Configure" --> PROXY
    WIZ -- "Login" --> LOGIN
    WIZ -- "Offline" --> GUEST
    LOGIN -- "成功" --> WIZ
    SET -- "GNB Install" --> IDX
```

---

## 7. 认证流程（Authentication Flow）

```mermaid
sequenceDiagram
    participant U as 浏览器
    participant C as Controller
    participant A as auth.lua
    participant CR as credential.lua
    participant API as MyNet API

    U->>C: 访问受保护页面
    C->>CR: cred_m.load()
    CR-->>C: {token, refresh_token, expire_at}

    alt Token 有效
        C->>C: 继续处理请求
    else Token 过期
        C->>A: auth.ensure_valid()
        A->>API: POST /auth/refresh {refresh_token}
        alt 刷新成功
            API-->>A: {new_token, new_expire}
            A->>CR: 保存新凭证
            A-->>C: 凭证对象
        else 刷新失败
            A->>API: POST /auth/login {email, password}
            alt 重登录成功
                API-->>A: {token, refresh_token}
                A->>CR: 保存凭证
                A-->>C: 凭证对象
            else 全部失败
                A-->>C: nil
                C->>U: 重定向 → /login
            end
        end
    end
```

---

## 8. 配置同步流程（Config Sync / Bundle API）

```mermaid
flowchart TD
    A[触发配置同步] --> B["调用 refresh_configs_bundle(node_id)"]
    B --> C[确保认证有效]
    C --> D[请求 Bundle API]
    D --> E{API 支持 bundle?}

    E -- 是 --> F[收到完整配置包]
    F --> G[写入 node.conf]
    F --> H[写入 route.conf]
    F --> I[写入 address.conf]
    F --> J[写入 ed25519 对端公钥]
    F --> K{包含自身密钥?}
    K -- 是 --> L[写入 private_key + public_key]
    K -- 否 --> M[跳过]

    H --> N[同步顶层 route.conf]
    N --> O{Proxy 已启用?}
    O -- 是 --> P[重新注入代理路由]
    O -- 否 --> Q[完成]

    E -- 否 --> R[Fallback: Legacy 模式]
    R --> S[GET /nodes/id/config → node.conf]
    R --> T[GET /route/node/id → route.conf]
    R --> U[GET /nodes/id/keys → 更新公钥]
    S --> Q
    T --> Q
    U --> Q

    P --> Q
    L --> Q
    M --> Q
```
