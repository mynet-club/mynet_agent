# 编译构建指南

本项目为 OpenWrt LuCI 纯 Lua 包，**无需本地 C 编译工具链**，`Build/Compile` 步骤为空。
当前仓库提供本地打包脚本，日常构建直接使用 `bash debug/sync.sh build` 即可。

---

## 包信息

| 字段 | 值 |
|------|----|
| 包名 | `luci-app-mynet` |
| 版本 | `2.1.6-1` |
| 架构 | `all`（纯 Lua，与 CPU 架构无关）|
| 依赖 | `luci-base`, `curl`, `libcurl-gnutls4`, `luci-lib-jsonc`, `bash`, `ca-bundle` |

---

## 一、仓库内直接构建（推荐）

```bash
cd /path/to/mynet_lua
bash debug/sync.sh build
```

输出文件：

```bash
build/luci-app-mynet_2.1.6-1_all.ipk
```

该脚本会完成：

- 打包 LuCI Lua / View / 静态资源
- 生成中文 `.lmo` 翻译文件
- 生成可直接 `opkg install` 的 `.ipk`

---

## 二、在 OpenWrt SDK 中编译

### 1. 将源码放入 OpenWrt 包目录

```bash
# 假设 OpenWrt 源码树根目录为 /openwrt
cp -r mynet_lua /openwrt/package/luci-app-mynet
```

或通过 feeds 方式：

```bash
# 在 feeds.conf / feeds.conf.default 中添加本地 feed
src-link mynet /path/to/mynet_lua_parent

# 更新并安装
./scripts/feeds update mynet
./scripts/feeds install luci-app-mynet
```

### 2. 配置

```bash
make menuconfig
# 路径: LuCI → 3. Applications → luci-app-mynet
# 选择为 <M>（模块）或 <*>（内置）
```

### 3. 编译包

```bash
# 单独编译（推荐调试时使用）
make package/luci-app-mynet/compile V=s

# 同时生成 ipk
make package/luci-app-mynet/install V=s
```

输出 ipk 路径（通常）：

```
bin/packages/<arch>/base/luci-app-mynet_2.1.6-1_all.ipk
```

---

## 三、手动打包（不进 OpenWrt 树）

如果只需生成 ipk 用于快速测试，可以手动模拟 OpenWrt 包安装结构：

```bash
#!/bin/bash
PKG=luci-app-mynet_2.1.6-1_all
mkdir -p /tmp/$PKG/data/usr/lib/lua/luci/controller
mkdir -p /tmp/$PKG/data/usr/lib/lua/luci/model/mynet
mkdir -p /tmp/$PKG/data/usr/share/luci/view/mynet
mkdir -p /tmp/$PKG/data/www/luci-static/resources/mynet/css
mkdir -p /tmp/$PKG/data/www/luci-static/resources/mynet/js
mkdir -p /tmp/$PKG/data/etc/mynet/conf

cp luasrc/controller/mynet.lua      /tmp/$PKG/data/usr/lib/lua/luci/controller/
cp luasrc/model/mynet/*.lua         /tmp/$PKG/data/usr/lib/lua/luci/model/mynet/
cp luasrc/view/mynet/*.htm          /tmp/$PKG/data/usr/share/luci/view/mynet/
cp htdocs/luci-static/resources/mynet/css/mynet.css \
                                    /tmp/$PKG/data/www/luci-static/resources/mynet/css/
cp htdocs/luci-static/resources/mynet/js/mynet.js \
                                    /tmp/$PKG/data/www/luci-static/resources/mynet/js/
cp root/etc/mynet/conf/config.json  /tmp/$PKG/data/etc/mynet/conf/

# 生成 control 文件
mkdir -p /tmp/$PKG/CONTROL
cat > /tmp/$PKG/CONTROL/control <<EOF
Package: luci-app-mynet
Version: 2.1.6-1
Architecture: all
Depends: luci-base, curl, libcurl-gnutls4, luci-lib-jsonc, bash, ca-bundle
Section: luci
Description: MyNet VPN management interface for OpenWrt.
EOF

# 打包
cd /tmp && tar czf $PKG.ipk ./CONTROL ./data 2>/dev/null || \
  (cd /tmp/$PKG && tar czf ../data.tar.gz data && tar czf ../control.tar.gz CONTROL && \
   echo "2.0" > /tmp/debian-binary && \
   cd /tmp && ar cr $PKG.ipk debian-binary control.tar.gz data.tar.gz)

echo "ipk 已生成: /tmp/$PKG.ipk"
```

---

## 四、版本升级

修改 [Makefile](../Makefile) 中的 `PKG_VERSION` 和 `PKG_RELEASE`：

```makefile
PKG_VERSION:=2.1.6
PKG_RELEASE:=1
```

同时需要同步更新：

- [debug/sync.sh](../debug/sync.sh) 中的 `PKG_VERSION`
- [luasrc/model/mynet/util.lua](../luasrc/model/mynet/util.lua) 中的 `APP_VERSION`
- [CHANGELOG.md](../CHANGELOG.md) 中的版本记录

---

## 注意事项

- 本项目无 C/C++ 代码，不需要交叉编译工具链
- Lua 文件修改后**无需重新编译**，直接 `scp` 覆盖到路由器即可测试
- `route.conf` 的 OS 路由格式应保持为 `cidr dev <iface>`，不要改回 `via <peer_vpn_ip>`
- 修改完 `.htm` 模板后需清理 LuCI 缓存：`rm -rf /tmp/luci-*`
