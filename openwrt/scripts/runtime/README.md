# MyNet OpenWrt Runtime

本目录是 MyNet 在 OpenWrt 上的运行时脚本源码。

## 目录结构

```
scripts/runtime/
├── rc.mynet            # init.d 服务脚本源码
├── route.mynet         # 路由管理脚本源码
├── firewall.mynet      # 防火墙管理脚本源码
└── modules/
    ├── route.sh        # 路由模块
    └── firewall.sh     # 防火墙模块
```

## 部署后的系统路径

```
/etc/init.d/mynet                       ← rc.mynet
/etc/mynet/scripts/route.mynet          ← route.mynet
/etc/mynet/scripts/firewall.mynet       ← firewall.mynet
```

这些文件由 Makefile / release workflow / `debug/sync.sh` 复制到运行时路径。

## 路由管理

```bash
# 应用路由（读取 route.conf，添加内核路由）
MYNET_HOME=/etc/mynet sh /etc/mynet/scripts/route.mynet start \
  --config /etc/mynet/conf/route.conf --interface gnb_tun_16

# 移除路由
MYNET_HOME=/etc/mynet sh /etc/mynet/scripts/route.mynet stop \
  --config /etc/mynet/conf/route.conf --interface gnb_tun_16
```

### route.conf 格式

```
# <cidr> dev <vpn_iface>
10.150.14.220/32 dev gnb_tun_16
192.168.8.0/24 dev gnb_tun_16
```

> 注意：不要使用 `via <gateway>` 格式，tun 设备的对端 VPN IP 不是 on-link 网关。

## 相关文档

- [OpenWrt VPN 跨网段互通设计](../../docs/openwrt_gw.md)

## 📋 依赖要求

- OpenWrt 系统
- MyNet (GNB/WireGuard) 已安装
- root权限或sudo访问
- 基本网络工具 (ip, iptables等)

## 💡 使用建议

1. **首次部署**: 使用`./deploy-service.sh --auto-yes`
2. **配置调整**: 修改`templates/`下的模板文件
3. **手动调试**: 使用`route_gw_simple.sh`进行手动配置
4. **故障排查**: 查看`docs/`目录下的详细文档

---

**注意**: 部署前请备份重要配置文件，建议先在测试环境中验证。