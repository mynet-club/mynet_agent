OpenWrt VPN 跨网段互通设计指南

本文针对在 OpenWrt 环境下，通过 VPN 打通两个 LAN 网络的设计进行系统总结。涵盖旁路由与非旁路由、网段冲突、NAT/masq 使用、fw3/fw4 实施差异及具体配置示例。

⸻

1. 背景与问题

1.1 网络场景
	•	两个独立局域网（LAN），分别有旁路由 A 和 B，VPN 隧道已建立。
	•	LAN 内主机默认网关可能不同：
	•	旁路由模式：部分节点默认网关指向旁路由，部分指向主路由。
	•	非旁路由模式：所有节点默认网关指向 VPN 节点。
	•	目标：
	•	打通两端 LAN，使 LAN 内节点可互访。
	•	在不同网络结构（旁路与非旁路）下正确处理回包。

⸻

1.2 核心问题
	1.	旁路由回包问题：
	•	LAN 内有主机默认网关非旁路由时，VPN 访问回包可能走错网关，导致访问失败。
	2.	网段冲突问题：
	•	两端 LAN 网段相同，VPN 通信时可能源/目的 IP 冲突。
	3.	防火墙与 NAT 的协作：
	•	zone 的 ACCEPT 放行流量，但不自动修改路由表。
	•	NAT（masq）策略需根据流量方向和场景谨慎设计。

⸻

2. 设计原则

2.1 流量方向与 NAT

场景	NAT/masq	说明
VPN↔VPN zone 互通	不做 NAT	保持源 IP 真实，便于双方识别
VPN→LAN（旁路由内）	必须 SNAT	旁路由 LAN 内不同默认网关的节点回包必须通过旁路由 → VPN
LAN→VPN	不做 NAT	LAN 发起流量源 IP 保留，便于对方 LAN 主机响应
LAN→WAN	保留 WAN zone masq	正常互联网访问

2.2 网段冲突处理
	•	如果两端 LAN 网段相同，有三种解决方案：
	
	方案1：VPN zone 全局SNAT
	•	在 VPN zone 上直接设置 masq=1（SNAT），把 VPN 出口流量源 IP 改为 VPN 节点自身 LAN IP。
	•	保证回包能正确返回 VPN。
	•	缺点：对端看不到真实源IP
	
	方案2：修改其中一端LAN网段
	•	将其中一端的LAN网段改为不冲突的网段（如192.168.1.0/24 → 192.168.2.0/24）
	•	需要重新规划IP地址，但能保持源IP可见性
	
	方案3：使用端口映射
	•	只暴露特定服务端口，而不是整个网段互通
	•	适用于只需要访问特定服务的场景

⸻

2.3 防火墙 zone 配置
	•	VPN zone：
	•	input/output/forward=ACCEPT
	•	network=VPN 接口
	•	masq=0（VPN↔VPN 不做 NAT）
	•	LAN/WAN zone：
	•	保持默认 ACCEPT
	•	WAN 保留 masq=1（互联网访问）
	•	Forwarding：
	•	LAN ↔ VPN 双向 ACCEPT
	•	WAN ↔ VPN 双向 ACCEPT（如果需要）

注意：防火墙 zone 只是放行流量，不会自动修改路由表。

⸻

3. OpenWrt fw4 示例配置

假设 A 旁路由：
	•	LAN：192.168.0.0/24，LAN IP=192.168.0.181
	•	VPN zone：gnb_tun，VPN IP=10.1.0.35

# 1. 定义 VPN zone
uci add firewall.zone
uci set firewall.@zone[-1].name='mynet'
uci set firewall.@zone[-1].input='ACCEPT'
uci set firewall.@zone[-1].output='ACCEPT'
uci set firewall.@zone[-1].forward='ACCEPT'
uci set firewall.@zone[-1].network='gnb_tun gnb_tun_9016'
uci set firewall.@zone[-1].masq='0'

# 2. Forwarding
uci add firewall forwarding
uci set firewall.@forwarding[-1].src='lan'
uci set firewall.@forwarding[-1].dest='mynet'

uci add firewall forwarding
uci set firewall.@forwarding[-1].src='mynet'
uci set firewall.@forwarding[-1].dest='lan'

uci add firewall forwarding
uci set firewall.@forwarding[-1].src='wan'
uci set firewall.@forwarding[-1].dest='mynet'

uci add firewall forwarding
uci set firewall.@forwarding[-1].src='mynet'
uci set firewall.@forwarding[-1].dest='wan'

# 3. VPN->LAN SNAT
uci add firewall.redirect
uci set firewall.@redirect[-1].name='vpn-to-lan-snat'
uci set firewall.@redirect[-1].src='mynet'
uci set firewall.@redirect[-1].dest='lan'
uci set firewall.@redirect[-1].target='SNAT'
uci set firewall.@redirect[-1].src_dip='192.168.0.0/24'
uci set firewall.@redirect[-1].snat_ip='192.168.0.181'

# 4. 静态路由配置（重要！）
# 添加对端LAN网段的静态路由，假设B端LAN为192.168.1.0/24，通过VPN IP 10.1.0.36到达
uci add network route
uci set network.@route[-1].interface='gnb_tun'
uci set network.@route[-1].target='192.168.1.0/24'
uci set network.@route[-1].gateway='10.1.0.36'

# 或者使用ip route命令
# ip route add 192.168.1.0/24 via 10.1.0.36 dev gnb_tun

# 5. 提交并重启
uci commit firewall
uci commit network
/etc/init.d/firewall restart
/etc/init.d/network restart

B 旁路由同理，LAN、VPN IP、SNAT IP 替换为 B 对应值。

**配置验证：**
```bash
# 验证防火墙配置
uci show firewall | grep -E "(zone|forwarding|redirect)"

# 验证网络配置
uci show network | grep -E "(route|interface)"

# 测试连通性
ping -c 4 对端VPN_IP
ping -c 4 对端LAN_IP
```

⸻

4. fw3 与 fw4 差异

特性	fw3	fw4
zone masq	option masq 1	option masq 1，行为类似，但 fw4 对 nat/forward 更严格
redirect/NAT	fw3 通过 config nat 或 iptables	fw4 使用 config redirect 或 /etc/firewall.user iptables 规则
interface 绑定	fw3 zone 绑定物理接口	fw4 zone 支持接口 + alias + vlan，可绑定多个虚拟接口
SNAT 精细控制	多用 /etc/firewall.user	可以用 fw4 redirect 实现 VPN→LAN SNAT

总结：fw4 提供更灵活的 zone/redirect 管理，适合旁路由 VPN 场景。

⸻

5. 特殊注意点
	1.	旁路由必须单独做 VPN→LAN SNAT，否则 LAN 内默认网关不同的主机回包会断掉。
	2.	VPN↔VPN 互通不做 masq，保持源 IP 真实。
	3.	LAN→VPN 不做 NAT，LAN 内原始 IP 保留，便于对方 LAN 响应。
	4.	网段冲突场景：VPN zone 可直接 masq=1，将源 IP 改成 VPN 节点自身 IP。
	5.	防火墙 zone 只负责放行流量，不修改路由表，静态路由可能仍然需要在旁路由上配置。

⸻

7. 故障排查指南

7.1 常见问题诊断
	•	VPN隧道已建立但无法访问对端LAN：
	•	检查路由表：ip route show
	•	检查防火墙规则：iptables -L -n -v
	•	检查是否有对端LAN的静态路由

	•	能ping通对端VPN IP但无法访问LAN内设备：
	•	检查SNAT规则是否正确配置
	•	检查对端设备的路由返回路径

	•	访问对端LAN有去无回：
	•	检查旁路由的SNAT配置
	•	确认LAN内设备的默认网关设置

7.2 调试命令
```bash
# 查看路由表
ip route show
route -n

# 查看防火墙规则
iptables -L -n -v
iptables -t nat -L -n -v

# 查看接口状态
ip addr show
ifconfig

# 测试连通性
ping -c 4 目标IP
traceroute 目标IP

# 查看连接状态
netstat -rn
ss -tuln
```

⸻

9. 安全考虑

9.1 防火墙安全
	•	避免开放不必要的端口到WAN
	•	VPN zone的WAN访问规则要谨慎配置
	•	建议使用DROP默认策略，明确ACCEPT需要的流量

9.2 访问控制
	•	可以配置更精细的防火墙规则限制VPN访问范围
	•	例如只允许访问特定端口或特定IP段
	
```bash
# 例：只允许VPN访问LAN的SSH和HTTP服务
uci add firewall rule
uci set firewall.@rule[-1].name='vpn-to-lan-ssh'
uci set firewall.@rule[-1].src='mynet'
uci set firewall.@rule[-1].dest='lan'
uci set firewall.@rule[-1].dest_port='22'
uci set firewall.@rule[-1].proto='tcp'
uci set firewall.@rule[-1].target='ACCEPT'
```

9.3 日志记录
	•	启用防火墙日志记录，便于问题诊断
	•	配置：uci set firewall.@defaults[0].syn_flood='1'
	•	配置：uci set firewall.@defaults[0].input='REJECT'

⸻

8. 性能优化建议

8.1 MTU调优
	•	VPN接口MTU通常需要比物理接口小
	•	建议设置：ip link set dev gnb_tun mtu 1420
	•	或在接口配置中设置：uci set network.gnb_tun.mtu='1420'

8.2 防火墙优化
	•	减少不必要的NAT规则
	•	使用精确的源/目标匹配
	•	避免过于宽泛的ACCEPT规则

⸻

6. 总结
	•	旁路由场景：VPN→LAN 必须 SNAT；VPN↔VPN 不 NAT；LAN→VPN 不 NAT；zone masq 可保持 0。
	•	非旁路场景：VPN↔VPN 保持真实源 IP即可，VPN→LAN 可不做 SNAT。
	•	网段冲突：VPN zone 上 masq=1 是简单有效的解决方法。
	•	fw3/fw4：核心配置相似，但 fw4 更适合管理多个虚拟接口和精细 NAT。