# MyNet Lua 迁移问题报告

> 从 192.168.8.222（旧旁路由，Lean LuCI 18.06）迁移到 192.168.8.1（新主路由，OpenWrt 24.10.7）过程中发现的所有问题。

---

## 环境对比

| 项目 | 旧设备 (8.222) | 新设备 (8.1) |
|------|---------------|-------------|
| IP | 192.168.8.222 | 192.168.8.1 |
| 角色 | 旁路由 | 主路由 (PVE VM 201) |
| 系统 | OpenWrt (Lean LuCI 18.06) | OpenWrt 24.10.7 r29197, x86_64 |
| LuCI | `luci-base git-23.102.51528` | `luci-base 26.148.37199` |
| 防火墙 | iptables | nftables (fw4) |
| 包管理 | opkg | opkg (OpenWrt 24.10.7) |

---

## 问题清单

### 1. IPK 包格式不兼容

**现象**：GitHub Release 的 `.ipk` 实际是 Debian `ar` 格式，OpenWrt opkg 报 `Malformed package file`。

**影响**：所有 IPK（mynet、argon）都无法直接 `opkg install`。

**临时方案**：
```bash
# Mac 上用 bsdtar 解压 ar 包，提取 data.tar.gz
bsdtar -xf luci-app-mynet_2.1.8-1_all.ipk
# 手动 tar 管道传输到设备
cat data.tar.gz | ssh root@192.168.8.1 "tar xzf - -C /"
```

**建议**：GitHub Release 应发布标准 OpenWrt IPK 格式（gzip 压缩的 ar 包），或提供安装脚本自动处理。

---

### 2. OpenWrt 无 sftp-server

**现象**：SCP 失败 `sftp-server not found`。

**影响**：无法用 scp 传文件到 OpenWrt。

**临时方案**：
```bash
cat file | ssh root@host "cat > /path/to/file"
```

**建议**：文档中注明 OpenWrt 默认不支持 SCP，推荐用 wget 在设备上直接下载。

---

### 3. LuCI 菜单不显示（ACL 缺失）

**现象**：mynet 安装后 LuCI 侧边栏没有 MyNet 菜单。

**根因**：缺少 `/usr/share/rpcd/acl.d/luci-app-mynet.json` ACL 权限文件。

**修复**：
```bash
cat > /usr/share/rpcd/acl.d/luci-app-mynet.json << 'EOF'
{
    "luci-app-mynet": {
        "description": "Grant access to MyNet",
        "read": {
            "ubus": {
                "file": ["stat", "read", "exec"],
                "luci": ["getFeatures"]
            },
            "file": {
                "/etc/mynet/*": ["read"],
                "/etc/init.d/mynet": ["exec"],
                "/usr/lib/lua/luci/*": ["read"]
            }
        },
        "write": {
            "ubus": {
                "file": ["write", "exec"]
            },
            "file": {
                "/etc/mynet/*": ["write"],
                "/etc/init.d/mynet": ["exec"]
            }
        }
    }
}
EOF
```

**建议**：IPK 的 `postinst` 脚本应自动创建 ACL 文件，或在安装文档中明确说明。

---

### 4. Argon 主题版本不兼容

**现象**：手动解压部署 argon v2.3.2 后报错 `Unable to compile 'themes/argon/header' as Lua template: Unable to load Lua runtime`。

**根因**：手动部署缺少 `luci-compat` 和 `luci-lib-ipkg` 依赖，且文件不完整。

**修复**：
```bash
opkg install luci-compat luci-lib-ipkg
# 然后在设备上直接 wget 下载 IPK 安装
cd /tmp
wget --no-check-certificate -O luci-theme-argon.ipk \
  'https://github.com/jerrykuku/luci-theme-argon/releases/download/v2.3.2/luci-theme-argon_2.3.2-r20250207_all.ipk'
opkg install luci-theme-argon.ipk
```

**注意**：OpenWrt 24.10 必须用 v2.x 分支（master），不能用 18.06 分支。

**建议**：安装文档明确依赖链：`luci-compat` → `luci-lib-ipkg` → `luci-theme-argon`。

---

### 5. curl HTTPS 失效（libcurl 多版本冲突）

**现象**：`curl` 报 `Error relocating: curl_easy_ssls_import: symbol not found`。

**根因**：`libcurl-gnutls4` (8.9.1) 和 `libcurl4` (8.19.0) 同时安装，动态链接冲突。安装 mynet 依赖时 opkg 自动拉入了 `libcurl-gnutls4`。

**修复**：
```bash
opkg remove --force-depends libcurl-gnutls4
opkg install --force-reinstall libcurl4 curl
# 验证
curl --version
# curl 8.19.0 (x86_64-openwrt-linux-gnu) libcurl/8.19.0 mbedTLS/3.6.6
```

**建议**：IPK 依赖声明中不要引入 `libcurl-gnutls4`，应依赖 `libcurl4` 或 `libcurl-mbedtls`。

---

### 6. GNB 二进制缺失

**现象**：`gnb_ctl not found at /etc/mynet/driver/gnb/bin/gnb_ctl`。

**根因**：IPK 只包含 LuCI 应用层，不包含 GNB 驱动二进制。

**修复**：
```bash
cd /tmp
wget --no-check-certificate -O gnb_linux_amd64.tgz \
  'https://github.com/luoyueliang/opengnb/releases/download/v1.6.4/gnb_linux_amd64_v1.6.4.tgz'
tar xzf gnb_linux_amd64.tgz
mkdir -p /etc/mynet/driver/gnb/bin
cp /tmp/bin/* /etc/mynet/driver/gnb/bin/
chmod +x /etc/mynet/driver/gnb/bin/*
# 验证
/etc/mynet/driver/gnb/bin/gnb -v
# GNB version Dev 1.6.4  protocol version 1.6.0
```

**建议**：
- IPK 安装时自动检测并下载 GNB 二进制，或在 LuCI 界面提供"安装驱动"按钮
- 明确文档说明 GNB 二进制需要单独安装

---

### 7. nftables 防火墙阻止 GNB 隧道转发

**现象**：8.1 自身能 ping 通 GNB 节点，但 Mac 通过 8.1 转发时 100% 丢包。

**根因**：OpenWrt 24.10 使用 nftables (fw4)，`accept_to_mynet` 和 `accept_from_mynet` 链为空，LAN → GNB 隧道的转发被 `policy drop` 丢弃。

**防火墙规则分析**：
```
chain forward {
    type filter hook forward priority filter; policy drop;
    iifname "br-lan" jump forward_lan
    ...
}

chain forward_lan {
    jump accept_to_wan          # LAN → WAN ✅
    jump accept_to_mynet        # LAN → MyNet  链为空！
    jump accept_to_lan
}

chain accept_to_mynet {
    # 空的！没有任何规则
}
```

**修复**：
```bash
# 添加 LAN → GNB 隧道放行
nft add rule inet fw4 accept_to_mynet oifname "gnb_tun_178" counter accept
# 添加 GNB 隧道 → LAN 放行
nft add rule inet fw4 accept_from_mynet iifname "gnb_tun_178" counter accept
```

**持久化**（写入 `/etc/firewall.user`）：
```bash
# MyNet GNB tunnel - allow LAN <-> GNB forwarding
nft add rule inet fw4 accept_to_mynet oifname "gnb_tun_178" counter accept
nft add rule inet fw4 accept_from_mynet iifname "gnb_tun_178" counter accept
```

**建议**：`/etc/init.d/mynet` 的 `start` 函数应自动添加 nftables 规则，或在 `start` 时检测并修复。

---

### 8. 回程路由缺失

**现象**：Mac (192.168.9.183) 到 GNB 节点的包能到 8.1，但回程不通。

**根因**：8.1 的默认路由走 `192.168.1.1` (eth1/WAN)，不知道 `192.168.9.0/24` (Mac 所在网段) 应走 `192.168.8.209` (kwrt)。

**修复**：
```bash
ip route add 192.168.9.0/24 via 192.168.8.209
```

**持久化**（写入 `/etc/rc.local`，注意在 `exit 0` 之前）：
```bash
# Route to Mac network via kwrt (192.168.9.1)
ip route add 192.168.9.0/24 via 192.168.8.209 2>/dev/null
```

**建议**：这是网络拓扑特定问题，但文档应提醒用户在多网段环境中检查回程路由。

---

### 9. ClashX Meta TUN 模式劫持流量

**现象**：Mac 到 10.182.236.0/24 的流量被 ClashX Meta 的 utun6 虚拟网卡拦截。

**根因**：ClashX Meta TUN 模式的 `auto-route: true` 会添加默认路由，劫持所有流量。

**修复**：在 ClashX Meta 配置 (`~/.config/clash.meta/config.yaml`) 中添加：
```yaml
tun:
  enable: true
  stack: system
  auto-route: true
  auto-detect-interface: true
  dns-hijack:
  - any:53
  route-exclude-address:
  - 10.182.236.0/24
  - 192.168.8.0/24
```

**建议**：文档中提醒使用 TUN 模式代理的用户需要排除 GNB 网段。

---

## 完整安装流程（OpenWrt 24.10）

```bash
# 1. 安装基础依赖
opkg install luci-compat luci-lib-ipkg lua libopenssl3

# 2. 安装 Argon 主题（v2.x 适配 OpenWrt 24.10）
cd /tmp
wget --no-check-certificate -O luci-theme-argon.ipk \
  'https://github.com/jerrykuku/luci-theme-argon/releases/download/v2.3.2/luci-theme-argon_2.3.2-r20250207_all.ipk'
opkg install luci-theme-argon.ipk

wget --no-check-certificate -O luci-app-argon-config.ipk \
  'https://github.com/jerrykuku/luci-app-argon-config/releases/download/v0.9/luci-app-argon-config_0.9_all.ipk'
opkg install luci-app-argon-config.ipk

wget --no-check-certificate -O luci-i18n-argon-config-zh-cn.ipk \
  'https://github.com/jerrykuku/luci-theme-argon/releases/download/v1.8.4/luci-i18n-argon-config-zh-cn_0.9-20220424_all.ipk'
opkg install luci-i18n-argon-config-zh-cn.ipk

# 3. 设置 Argon 为默认主题
uci set luci.main.mediaurlbase='/luci-static/argon'
uci commit luci
rm -rf /tmp/luci-modulecache/* /tmp/luci-indexcache*
/etc/init.d/rpcd restart
/etc/init.d/uhttpd restart

# 4. 安装 GNB 驱动
cd /tmp
wget --no-check-certificate -O gnb_linux_amd64.tgz \
  'https://github.com/luoyueliang/opengnb/releases/download/v1.6.4/gnb_linux_amd64_v1.6.4.tgz'
tar xzf gnb_linux_amd64.tgz
mkdir -p /etc/mynet/driver/gnb/bin
cp /tmp/bin/* /etc/mynet/driver/gnb/bin/
chmod +x /etc/mynet/driver/gnb/bin/*

# 5. 添加 ACL 文件
cat > /usr/share/rpcd/acl.d/luci-app-mynet.json << 'EOF'
{
    "luci-app-mynet": {
        "description": "Grant access to MyNet",
        "read": {
            "ubus": { "file": ["stat", "read", "exec"], "luci": ["getFeatures"] },
            "file": {
                "/etc/mynet/*": ["read"],
                "/etc/init.d/mynet": ["exec"],
                "/usr/lib/lua/luci/*": ["read"]
            }
        },
        "write": {
            "ubus": { "file": ["write", "exec"] },
            "file": { "/etc/mynet/*": ["write"], "/etc/init.d/mynet": ["exec"] }
        }
    }
}
EOF

# 6. 添加防火墙规则（nftables）
cat >> /etc/firewall.user << 'EOF'
# MyNet GNB tunnel - allow LAN <-> GNB forwarding
nft add rule inet fw4 accept_to_mynet oifname "gnb_tun_178" counter accept
nft add rule inet fw4 accept_from_mynet iifname "gnb_tun_178" counter accept
EOF

# 7. 重启服务
/etc/init.d/mynet restart
/etc/init.d/firewall restart
```

---

## 给 mynet_lua 项目的改进建议

1. **IPK 打包**：确保 GitHub Release 的 IPK 是标准 OpenWrt 格式，避免 Debian ar 格式
2. **依赖声明**：IPK Control 文件应声明 `luci-compat`、`luci-lib-ipkg`、`libcurl4`（非 gnutls）
3. **ACL 文件**：IPK 应包含 `/usr/share/rpcd/acl.d/luci-app-mynet.json`
4. **防火墙集成**：`/etc/init.d/mynet` 的 `start` 函数应自动添加 nftables 转发规则
5. **GNB 驱动安装**：提供 `mynet install-driver` 命令或 LuCI 界面按钮，自动下载对应架构的 GNB 二进制
6. **安装文档**：明确 OpenWrt 24.10 的安装步骤，包括依赖安装顺序和防火墙配置
