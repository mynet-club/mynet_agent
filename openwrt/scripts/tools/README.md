# 网络诊断与优化工具

## 📁 目录结构

```
scripts/tools/
├── diagnose_network.sh          # 通用网络诊断脚本
├── remote_diagnose.sh           # 远程诊断工具
├── check_openwrt_masq.sh        # OpenWrt MASQ 检查
├── youtube-test.sh              # YouTube 访问性能测试
├── optimize_gnb_conntrack.sh    # GNB VPN Conntrack 优化
├── verify-manifest.sh           # Manifest 验证工具
└── README.md                    # 本文档
```

---

## 🛠️ 脚本说明

### 1. diagnose_network.sh
**用途**: 诊断 MyNet 节点间的网络连接问题和防火墙配置

**使用场景**:
- 检查节点间的网络连通性
- 验证防火墙 MASQ 配置
- 诊断路由表问题

**使用方法**:
```bash
./diagnose_network.sh
```

---

### 2. remote_diagnose.sh
**用途**: 在远程节点上运行诊断命令

**使用场景**:
- 从本地诊断远程 OpenWrt 路由器
- 批量检查多个节点
- 快速收集节点状态

**使用方法**:
```bash
./remote_diagnose.sh <node_ip>
```

---

### 3. check_openwrt_masq.sh
**用途**: 检查 OpenWrt 防火墙的 MASQ 规则

**使用场景**:
- 验证 NAT 配置是否正确
- 检查 zone 之间的 masquerading
- 排查 NAT 转发问题

**使用方法**:
```bash
# 在 OpenWrt 路由器上执行
./check_openwrt_masq.sh
```

---

### 4. youtube-test.sh
**用途**: 测试 YouTube 访问性能和质量

**使用场景**:
- 验证 DNS 劫持配置
- 测试 SNI Proxy 性能
- 监控 YouTube 访问速度

**测试项目**:
- DNS 解析正确性
- 网络延迟 (ping)
- HTTP/HTTPS 响应时间
- 视频流播放能力

**使用方法**:
```bash
./youtube-test.sh
```

**输出示例**:
```
=========================================
  YouTube 访问性能测试
=========================================
时间: 2025-01-19 14:30:00
客户端: MacBook-Pro (192.168.20.125)

[1/5] DNS 解析测试
---
✓ DNS 解析正确: www.youtube.com → 192.168.88.253
  耗时: 15ms

[2/5] 网络延迟测试
---
✓ Ping 正常: 平均延迟 77ms, 丢包率 0%
```

---

### 5. optimize_gnb_conntrack.sh ⭐ NEW
**用途**: 优化 OpenWrt 上 GNB VPN 的连接追踪 (conntrack) 策略

**核心策略**:
1. **VPN 隧道流量 (UDP)** → 绕过 conntrack (NOTRACK)
2. **应用层 TCP 连接** → 优化超时参数
3. **应用层 keepalive** → 不依赖内核超时

**适用场景**:
- ✅ OpenWrt 路由器运行 GNB VPN 客户端
- ✅ NAT 后的设备通过 VPN 访问外部服务
- ✅ 出现连接状态冲突导致的访问超时
- ✅ GitHub/YouTube 等站点间歇性超时

**解决的问题**:
- 旧的 conntrack 状态干扰新连接
- VPN 隧道因 conntrack 追踪导致延迟增加
- 大量连接超时，清除 conntrack 后立即恢复

**实现原理**:

#### 为什么需要 NOTRACK？
GNB VPN 的 UDP 隧道是**长连接**，不需要 Linux conntrack 追踪状态：
- ❌ **错误做法**: 增大 UDP conntrack 超时 → 仍然有追踪开销
- ✅ **正确做法**: 让 VPN 流量完全绕过 conntrack → 零开销

参考：WireGuard 使用 `PersistentKeepalive=25` 在应用层维持连接，不依赖内核。

#### 什么流量需要 conntrack？
NAT 后的**应用层 TCP 连接**需要 conntrack 追踪（用于 NAT 转换），但要优化参数：
- `ESTABLISHED` 超时从 5 天缩短到 1 小时（避免旧状态干扰）
- `TIME_WAIT` 缩短到 30 秒（快速回收端口）
- `CLOSE_WAIT` 缩短到 30 秒

**使用方法**:
```bash
# 在 OpenWrt 路由器上执行
scp scripts/tools/optimize_gnb_conntrack.sh root@192.168.20.1:/tmp/
ssh root@192.168.20.1
cd /tmp
chmod +x optimize_gnb_conntrack.sh
./optimize_gnb_conntrack.sh
```

**执行流程**:
1. 自动检测 GNB 使用的 UDP 端口
2. 添加 nftables NOTRACK 规则
3. 优化 TCP conntrack 超时参数
4. 保存配置到 `/etc/sysctl.conf`
5. 生成防火墙持久化脚本 `/etc/firewall.user`
6. 检查 GNB keepalive 配置

**输出示例**:
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  GNB VPN Conntrack 优化
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

步骤 1: 检测 GNB 端口
ℹ️  检测到端口: 9001

步骤 2: 添加 NOTRACK 规则
ℹ️  为 UDP 端口 9001 添加 NOTRACK 规则
✅ 已添加入站 NOTRACK 规则
✅ 已添加出站 NOTRACK 规则

步骤 3: 优化 TCP Conntrack 超时
✅ TCP ESTABLISHED: 3600s (1小时)
✅ TCP TIME_WAIT: 30s
✅ TCP CLOSE_WAIT: 30s

步骤 4: 保存配置
✅ 已保存到 /etc/sysctl.conf
✅ 已生成防火墙持久化规则

步骤 5: 检查 GNB Keepalive
✅ 找到 GNB 配置: /etc/mynet/driver/gnb/gnb.conf

当前状态
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Conntrack 容量: 56320
当前使用: 345

TCP 超时配置:
  ESTABLISHED: 3600s
  TIME_WAIT: 30s
  CLOSE_WAIT: 30s

NOTRACK 规则:
udp dport 9001 notrack
udp sport 9001 notrack
```

**验证效果**:
```bash
# 检查 NOTRACK 规则是否生效
nft list chain inet fw4 raw_prerouting | grep notrack

# 查看当前 conntrack 使用情况
cat /proc/sys/net/netfilter/nf_conntrack_count

# 测试访问
curl -I -m 10 https://www.youtube.com/
curl -I -m 10 https://github.com/
```

**注意事项**:
- ⚠️ 需要 OpenWrt 23.05+ (nftables fw4)
- ⚠️ NOTRACK 规则在防火墙重启后会自动应用（通过 `/etc/firewall.user`）
- ⚠️ 如果 GNB 端口变化，需要重新运行脚本

**参考文档**:
- WireGuard PersistentKeepalive 机制
- Linux nftables raw table 文档
- OpenWrt firewall4 (fw4) 文档

---

### 6. verify-manifest.sh
**用途**: 验证 MyNet 发布包的 manifest 文件

**使用场景**:
- CI/CD 构建验证
- 发布前检查
- 版本校验

**使用方法**:
```bash
./verify-manifest.sh <manifest_file>
```

---

## 🎯 常见使用场景

### 场景 1: OpenWrt 路由器网络故障排查
```bash
# 1. 检查基础网络
./diagnose_network.sh

# 2. 检查 MASQ 配置
./check_openwrt_masq.sh

# 3. 测试外网访问
./youtube-test.sh
```

### 场景 2: GNB VPN 连接不稳定
```bash
# 1. 优化 conntrack
./optimize_gnb_conntrack.sh

# 2. 验证效果
./youtube-test.sh

# 3. 检查连接状态
cat /proc/sys/net/netfilter/nf_conntrack_count
nft list chain inet fw4 raw_prerouting | grep notrack
```

### 场景 3: 远程节点批量诊断
```bash
# 诊断多个节点
for ip in 192.168.0.2 192.168.20.1 192.168.8.1; do
    echo "=== 诊断 $ip ==="
    ./remote_diagnose.sh $ip
done
```

---

## 📝 开发指南

### 添加新脚本
1. 使用项目标准头部格式（参考 `diagnose_network.sh`）
2. 提供清晰的错误提示和输出格式
3. 使用颜色区分信息级别（成功、警告、错误）
4. 更新本 README 文档

### 颜色规范
```bash
RED='\033[0;31m'     # 错误
GREEN='\033[0;32m'   # 成功
YELLOW='\033[1;33m'  # 警告
BLUE='\033[0;34m'    # 信息
NC='\033[0m'         # 重置
```

### 函数规范
```bash
print_success() { echo -e "${GREEN}✅ $1${NC}"; }
print_error()   { echo -e "${RED}❌ $1${NC}"; }
print_warning() { echo -e "${YELLOW}⚠️  $1${NC}"; }
print_info()    { echo -e "${BLUE}ℹ️  $1${NC}"; }
```

---

## 🔗 相关文档

- [MyNet 架构文档](../../docs/ARCHITECTURE.md)
- [网络快速参考](../../docs/QUICK_REFERENCE.md)
- [用户指南](../../docs/USER_GUIDE.md)
- [OpenWrt 运行时文档](../runtime/README.md)

---

**最后更新**: 2025-01-19
**维护者**: MyNet Team
