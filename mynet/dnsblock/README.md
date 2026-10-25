# DNS hook 广告与恶意域名拦截

默认开启，仅在 PC DNS hook 正在处理系统查询时生效；不改变隧道、加速开启意愿或 DNS 所有权恢复规则。与 ctl 的加速白名单独立。未覆盖应用自带 DoH、直接 IP 访问或与正文共用域名的广告。

- ads.txt：35条人工维护广告/广告跟踪服务父域，覆盖自身和子域。
- urlhaus.txt：418条 URLhaus hostfile 恶意软件投递主机快照，精确匹配；不扩大到共享主机的父域或其全部子域。来源、上游时间、原始 SHA256 及条款链接保存在文件头。属于域名信誉拦截，不是终端病毒扫描。这两份内置表只在尚无成功云端缓存时兜底，不修改宿主 hosts 文件；日常维护与更新由 ctl 管理。
- 优先级：本机 allow 例外 > block（默认+自定义）> 加速/直连选择。命中在缓存和上游之前结束；CNAME目标也检查。不会新增加速路由。
- A→127.0.0.1，AAAA→::1，其他类型→NOERROR空答案；地址TTL60秒；不继承上游AD认证标志，不复制EDNS附加区。
- 查询使用不可变哈希表逐级后缀查找，不遍历整张清单。每10秒重读配置，切换清除应答缓存并对账路由；损坏配置保留运行中的旧规则并记录错误，首次启动遇到损坏配置明确失败。

## 配置

网页「本机加速」区域有独立开关。CLI：

```sh
mynet proxy dns-hook ad-block status
mynet proxy dns-hook ad-block off
mynet proxy dns-hook ad-block on
mynet proxy dns-hook ad-block refresh  # 只更新名单，不启动hook
```

配置文件跟随本机配置目录，默认 `$MYNET_HOME/conf/local/dns_adblock.json`：

```json
{"enabled":true,"block":["ads.example.com"],"allow":["safe.ads.example.com"]}
```

block/allow均包含子域。allow只为所列域名放行，CNAME目标若也在拦截表中须显式放行目标。默认恶意主机表只做精确匹配，避免把遭入侵的一个站点扩大为整个托管平台。

## 验证

自动化覆盖拦截优先级、无上游/路由副作用、A/AAAA/HTTPS/其他类型、EDNS与标志、事务ID/TTL、恶意主机精确边界、例外、CNAME、运行规则替换及坏配置保留、开关持久化；DNS压缩指针改为有限迭代并测试循环输入。真实隔离UDP worker验证回环应答，不修改宿主DNS。


## ctl 管理与缓存

维护源为 ctl 仓库 scripts/proxy-lists/ad-domains.txt（广告父域，包含子域）、malware-domains.txt（恶意主机，精确匹配）。一行一个纯域名，允许空行和#注释；不使用hosts格式、URL或通配符。ctl 发布脚本在GeoIP下载前发布两份名单，并为meta写入ad_domains/malware_domains计数、大小、SHA256。

客户端下载 `/d/plugins/mp/ad-domains.txt`、`/d/plugins/mp/malware-domains.txt`，沿用MYNET_APPS_INDEX基址。hook启动后台刷新，此后每小时刷新；网络请求不在查询路径。每份最大1MiB，完整校验两份后原子保存conf/local/dns_block_baseline.json（版本哈希含两份规则）；任何HTTP/格式/大小/读写失败不覆盖旧缓存。不依赖加速白名单下载成功。无缓存才使用内置兜底，已有缓存则完全替换内置，合法空清单也能撤回规则。本机block/allow与enabled配置不被下载覆盖。

成功保存后10秒规则对账周期内应用；命令status显示source=ctl/builtin及version，网页显示名单来源。refresh不启动hook、不重新抢占系统DNS。上游URLhaus快照仍需在ctl维护后发布；客户端不直接从URLhaus拉取。
