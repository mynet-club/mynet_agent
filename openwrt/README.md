# mynet_openwrt

OpenWrt 平台适配层（自 `mynet_lua` 迁出/更名），与 **`mynet_desktop`** 对等。

## 职责边界（目标态）

| 做（目标） | 不做（目标） |
|------------|--------------|
| LuCI 菜单 / 页面壳（渲染与导航） | 完整业务：节点同步、代理、心跳、启停逻辑 |
| ipk 打包、init、rpcd/ACL | 独立第二套业务守护 |
| 注册 ubus **会话与权限**，转发到 **mynetd** | uhttpd/nginx 反代 |

```text
LuCI / mynet_openwrt 壳
        │  /ubus  (mynet.*)
        ▼
     mynetd  ←—— mynet_agent
        │
   mynet / gnb
```

## 现状 vs 目标

当前树仍大量保留 `mynet_lua` 业务（代理、node 启停、心跳 Lua 实现）——**迁移源**，不是终态。

迁移方向：

1. 控制面迁至 `mynet_agent` 的 mynetd（HTTP → OpenWrt 侧 ubus 对象）
2. Lua 只保留：菜单、视图模板、静态资源托管、rpcd ACL
3. 与 `mynet_back` 的命令通道由 mynetd 承担，与桌面一致

## 构建

见 `docs/build.md`（原 mynet_lua 文档，包名待改 `luci-app-mynet` 路径注释仍可能写着 mynet_lua）。

## 历史

- 来源：`../mynet_lua`
- 非对等仓：`mynet_client`（Node 客户端，仅参考/归档）
