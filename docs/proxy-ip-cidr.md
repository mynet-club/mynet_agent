# PC 本机加速：预设与自定义 IPv4 CIDR 静态钉扎

## 背景

PC 本机加速默认通过 `dns-hook` 按「域名 → DNS 答案 IP」动态钉扎路由，租约随 DNS TTL 过期。对于长连接且具备 IP 亲和的 gRPC 客户端（如 Gemini.app），在 DNS TTL 过期后客户端仍可能复用旧 IP，导致路由回落物理口触发地区拦截（如 `1060 / FAILED_PRECONDITION`）。

为彻底解决该问题，PC 本机加速支持与域名分流同构的**双层 IPv4 前缀静态钉扎**：
- **预设基线（Baseline）**：`ctl` 发布的 `/d/plugins/mp/proxy-ips.txt`，由 `dns-hook` 后台每小时拉取落盘（404 视为空表），可通过 `mynet proxy dns-hook domains status|refresh` 查看和手动刷新。
- **本机自定义（Custom）**：`conf/local/proxy_intent.json` 中的 `domain_rules.custom_ips`，本机叠加、不回写云端。

两层合并后的 IPv4 前缀**不经 DNS 解析、永不因 TTL 过期、不受共享 CDN 降级（`markSharedIfPinned`）影响**，随本机加速意图启停自动装/撤。

## CLI 用法

```bash
# 添加单 IP（自动规范化为 /32）或 IPv4 CIDR（自动抹除主机位）
mynet plugin domains ip add 142.250.0.0/15
mynet plugin domains ip add 203.0.113.9/24   # 规范化为 203.0.113.0/24

# 删除（支持等价形式，如 1.2.3.4 与 1.2.3.4/32、含主机位的 CIDR）
mynet plugin domains ip rm 203.0.113.55/24

# 查看预设 + 自定义与合并后的生效前缀条数
mynet plugin domains list

# 查看/刷新云端预设基线（含 proxy_ips）
mynet proxy dns-hook domains status
mynet proxy dns-hook domains refresh
```

## 安全限制与注意事项

1. **仅支持 IPv4**：拒绝 IPv6、域名、空串及非标准格式。
2. **前缀宽度限制**：仅允许 `/8` 到 `/32`（拒绝 `0.0.0.0/1` 等过宽前缀），并拒绝保留网段（`0/8`、`127/8`、`169.254/16`、`224/4`、`240/4`）。
3. **本机直连网段保护**：运行时若某显式前缀与本机物理口直连网段重叠，会自动跳过并记录 `dns-hook.log`，防止劫持局域网或默认网关。
4. **容量上限**：活跃显式前缀与动态 IP 合计不超过 `512` 条；超限时报错且不驱逐已有有效路由。
5. **谨慎填写共享 CDN 网段**：若将国内常用 CDN 网段写入 `custom_ips`，该段内所有流量都会绕行隧道出口。
6. **兼容性说明**：历史上曾写入 `custom_ips` 但此前被 `net.ParseIP` 忽略的 CIDR 条目，升级后将开始生效。

## macOS 现网迁移：收编 Google 网段并卸载临时 LaunchDaemon

```bash
# 1. 写入 Google 常用 IPv4 网段
for cidr in \
  142.250.0.0/15 \
  172.217.0.0/16 \
  172.253.0.0/16 \
  74.125.0.0/16 \
  216.58.192.0/19 \
  216.239.32.0/19 \
  173.194.0.0/16 \
  108.177.0.0/17 \
  64.233.160.0/19 \
  209.85.128.0/17; do
  mynet plugin domains ip add "$cidr"
done

# 2. 应用配置并核验路由指向隧道口
mynet proxy apply
route -n get 142.250.4.188
route -n get 172.253.118.132

# 3. 确认生效后卸载临时 LaunchDaemon
sudo launchctl bootout system /Library/LaunchDaemons/com.mynet.google-routes.plist
sudo rm -f /Library/LaunchDaemons/com.mynet.google-routes.plist
```
