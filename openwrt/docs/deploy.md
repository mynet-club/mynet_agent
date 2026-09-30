# 部署与安装指南

---

## 一、通过 ipk 安装（推荐）

在 OpenWrt 路由器上：

```bash
# 上传 ipk 到路由器
scp build/luci-app-mynet_2.1.6-1_all.ipk root@192.168.0.2:/tmp/

# SSH 登录后安装
opkg install /tmp/luci-app-mynet_2.1.6-1_all.ipk

# 清理 LuCI 缓存
rm -rf /tmp/luci-*
```

卸载：

```bash
opkg remove luci-app-mynet
```

---

## 二、手动文件部署（开发 / 快速测试）

推荐使用 `debug/sync.sh` 完成所有步骤：

```bash
# 默认目标：openwrt-qemu（/etc/hosts 别名）
bash debug/sync.sh all

# 指定目标路由器（物理机或任意 SSH 可达地址）
ROUTER=root@192.168.9.1 bash debug/sync.sh all
```

`ROUTER` 环境变量默认为 `openwrt-qemu`，可在调用时覆盖。

`sync.sh all` 等价于依次执行：

```bash
bash debug/sync.sh build   # 打包 ipk
bash debug/sync.sh deploy  # scp 上传 + opkg install
bash debug/sync.sh scripts # 同步 runtime 脚本 + proxy 脚本
bash debug/sync.sh firewall # 防火墙安装
```

若只需推送 Lua/视图变更而不重建 ipk：

```bash
ROUTER=root@192.168.9.1 bash debug/sync.sh lua
```

---

## 三、验证安装

```bash
ssh root@192.168.0.2

# 检查文件
ls /usr/lib/lua/luci/controller/mynet.lua
ls /usr/lib/lua/luci/model/mynet/
ls /usr/share/luci/view/mynet/
ls /www/luci-static/resources/mynet/

# 检查配置
cat /etc/mynet/conf/config.json
cat /etc/mynet/conf/route.conf

# 检查 LuCI 路由注册
lua -e "require('luci.controller.mynet')"
```

访问地址：

```
http://<router-ip>/cgi-bin/luci/admin/services/mynet
```

---

## 四、配置说明

默认配置文件路径：`/etc/mynet/conf/config.json`

```json
{
  "server_config": {
    "api_base_url": "https://your-server/api/v2",
    "timeout": 30
  }
}
```

也可以在 Web 界面的「设置」页面直接修改 API 服务器地址。

---

## 五、运行时文件位置

| 文件 | 路径 | 说明 |
|------|------|------|
| 配置 | `/etc/mynet/conf/config.json` | API 基础地址、超时 |
| 凭证 | `/etc/mynet/conf/credential.json` | 登录 Token（自动生成）|
| 路由 | `/etc/mynet/conf/route.conf` | 内核 OS 路由输入文件 |
| 代理角色 | `/etc/mynet/conf/proxy/proxy_role.conf` | 代理持久化配置（DNS_MODE / PROXY_MODE 等）|
| 代理状态 | `/etc/mynet/var/proxy_state.json` | 运行时状态（上次启动参数）|
| 代理策略参数 | `/etc/mynet/var/proxy_policy_params.env` | bash 环境变量文件，由 proxy.lua start() 更新后传给 route_policy.sh |
| 代理 hooks | `/etc/mynet/scripts/proxy/hooks/` | 代理启停钩子脚本（post_start / pre_start / stop）|
| route_policy.sh | `/etc/mynet/scripts/proxy/route_policy.sh` | 策略路由/防火墙管理主脚本（proxy.lua 调用此路径）|
| VPN init | `/etc/init.d/mynet` | 由 `rc.mynet` 提供 |

---

## 六、依赖检查

```bash
opkg list-installed | grep -E "luci-base|curl|libcurl-gnutls4|luci-lib-jsonc|bash|ca-bundle|mynet"
```

如缺少依赖：

```bash
opkg update
opkg install luci-base curl libcurl-gnutls4 luci-lib-jsonc bash ca-bundle
```

---

## 七、路由与代理部署注意事项

- `/etc/mynet/conf/route.conf` 中写入的是 OS 路由，格式应为 `cidr dev <vpn_iface>`
- 代理启停最终走 Lua `proxy.start()` / `proxy.stop()`，不要直接调用 shell `route_policy.sh start/stop`
- `proxy.lua` 的 `ROUTE_POLICY_SH` 常量指向 `/etc/mynet/scripts/proxy/route_policy.sh`（**非** `openwrt/` 子目录）；`sync.sh` 同时将脚本安装到两个路径以保持兼容
- `proxy_policy_params.env` 由 `proxy.lua start()` 在调用 `route_policy.sh start` 前更新；`route_policy.sh` 只读取不生成此文件
- DNS 劫持（`DNS_MODE=redirect`）使用 nftables DNAT，规则限定 `iifname br-lan`，**只拦截 LAN 客户端 DNS**；路由器自身 DNS 仍走 dnsmasq upstream
- 更新 `.lua` / `.htm` 后如页面未刷新，清理 `/tmp/luci-*` 缓存再重试
