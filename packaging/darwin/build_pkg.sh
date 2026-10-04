#!/bin/sh
# 构建 macOS 图形化安装产物：MyNet_Agent_<ver>.pkg + .dmg
#
# 产物结构（Installer.app 双击 → 认证 → 落地 → postinstall 装服务）：
#   /opt/mynet/bin/{mynet,mynetd,mynet_tray}
#   /usr/local/bin/{gnb,gnb_ctl,gnb_es,gnb_crypto}          # systemd 单元同款路径
#   /opt/mynet/driver/gnb/bin/…                             # resolveGNBBinary 第一优先
#
# 职责边界不变：pkg 负责文件落地，服务装配调 `mynet install`（scripts/postinstall）。
# 未签名 —— 本机/内网验证用；对外分发需 codesign + notarization（另议）。
#
# 用法：
#   make pkg-darwin                    # 常规（需在 macOS 宿主机上）
#   VERSION=v0.0.4 GNB_DIST=/path make pkg-darwin
set -eu
cd "$(dirname "$0")/../.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo v0.0.0-dev)}"
PKG_VER="${VERSION#v}"
ARCH="$(uname -m)"
GNB_DIST="${GNB_DIST:-../mynet_gnb/dist}"
OUT=dist/pkg
STAGE="$OUT/root"
SCRIPTS=packaging/darwin/scripts
LDFLAGS="${LDFLAGS:--s -w}"

# 1) gnb 四件套：同构建强制（缺一不打包，R2）—— 混版本构建是 gnb 侧最深的坑
GNB_SRC="${GNB_DIR:-}"
if [ -z "$GNB_SRC" ]; then
  GNB_SRC=$(ls -td "$GNB_DIST"/gnb-*darwin-* 2>/dev/null | head -1 || true)
fi
[ -n "$GNB_SRC" ] && [ -d "$GNB_SRC" ] || {
  echo "✗ 找不到 gnb darwin 构建（GNB_DIST=$GNB_DIST）；先构建 mynet_gnb 或传 GNB_DIR" >&2
  exit 1
}
for prog in gnb gnb_ctl gnb_es gnb_crypto; do
  [ -f "$GNB_SRC/$prog" ] || { echo "✗ $GNB_SRC 缺 $prog —— 四件套不齐不打包" >&2; exit 1; }
done
# 基线核对：Makefile 声明的 GNB_BASELINE 提交号必须出现在构建目录名里
if [ -n "${GNB_BASELINE:-}" ]; then
  case "$GNB_SRC" in
    *"${GNB_BASELINE##*-}"*) ;;
    *) echo "✗ gnb 构建 $GNB_SRC 与声明基线 $GNB_BASELINE 不符" >&2; exit 1 ;;
  esac
fi
[ -f "$GNB_SRC/MANIFEST" ] && { echo "  gnb 来源: $GNB_SRC"; head -3 "$GNB_SRC/MANIFEST" | sed 's/^/    /'; }

# 2) 编译三件 Go 二进制
rm -rf "$STAGE"
mkdir -p "$STAGE/opt/mynet/bin" "$STAGE/opt/mynet/driver/gnb/bin" "$STAGE/usr/local/bin"

# 2b) 补丁栈判定 → 随包打包补丁相关内容：dist MANIFEST 声明了 p2-route-proxy
# 才带补丁包（patches/apply/revert/README）与 MANIFEST 对账件；未声明不带，
# 安装器不谎称出厂带 P2。
if grep -q '^patch_stack=.*p2-route-proxy' "$GNB_SRC/MANIFEST" 2>/dev/null; then
  # 默认相对路径要求 mynet_gnb 是有工作树的普通仓；它现在是 bare 仓（只有 .git），
  # 所以补丁内容从外部传 P2_DIR（与 GNB_DIR 同一套路，都是把来源交给调用方）。
  P2_SRC="${P2_DIR:-../mynet_gnb/packages/${GNB_BASELINE:-1.6.8-c411f14ee647}/p2-route-proxy}"
  if [ -d "$P2_SRC/patches" ]; then
    P2_DST="$STAGE/opt/mynet/gnb/packages/${GNB_BASELINE:-1.6.8-c411f14ee647}/p2-route-proxy"
    mkdir -p "$P2_DST"
    cp -R "$P2_SRC/patches" "$P2_SRC/README.md" "$P2_DST/"
    cp "$P2_SRC/apply.sh" "$P2_SRC/revert.sh" "$P2_DST/"
    chmod 755 "$P2_DST/apply.sh" "$P2_DST/revert.sh"
    cp "$GNB_SRC/MANIFEST" "$STAGE/opt/mynet/gnb/packages/MANIFEST"
    echo "  补丁内容随包: $P2_SRC → /opt/mynet/gnb/packages/.../p2-route-proxy"
  else
    echo "⚠ MANIFEST 声明 p2-route-proxy 但找不到 $P2_SRC —— 本次不带补丁内容" >&2
  fi
fi
CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$STAGE/opt/mynet/bin/mynet" ./cmd/mynet
CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -tags embed -o "$STAGE/opt/mynet/bin/mynetd" ./cmd/mynetd
CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$STAGE/opt/mynet/bin/mynet_tray" ./cmd/mynet_tray
# embed 构建自检：WebUI 没嵌进去的 mynetd 装出来是白屏（历史踩过的坑）
strings "$STAGE/opt/mynet/bin/mynetd" | grep -q crumb-page || {
  echo "✗ mynetd 缺内嵌 WebUI —— 构建缺 -tags embed" >&2; exit 1
}
cp "$GNB_SRC/gnb" "$GNB_SRC/gnb_ctl" "$GNB_SRC/gnb_es" "$GNB_SRC/gnb_crypto" "$STAGE/usr/local/bin/"
cp "$GNB_SRC/gnb" "$GNB_SRC/gnb_ctl" "$GNB_SRC/gnb_es" "$GNB_SRC/gnb_crypto" "$STAGE/opt/mynet/driver/gnb/bin/"
chmod 755 "$STAGE/opt/mynet/bin"/* "$STAGE/usr/local/bin"/* "$STAGE/opt/mynet/driver/gnb/bin"/*
# 脚本必须可执行 —— 644 会在 installer 的 Validating packages 阶段报
# "An error occurred while running scripts"（实测）
chmod 755 "$SCRIPTS/preinstall" "$SCRIPTS/postinstall"
# com.apple.provenance 删不掉（内核自管，SIP 保护）；pkgbuild 会把 xattr 编成
# AppleDouble 进 payload，Installer 安装时还原成目标文件的 xattr 而非 ._* 落盘文件。
# 这里仍尽力清一遍其它可删属性（quarantine 等），失败不算错。
xattr -r -c "$STAGE" 2>/dev/null || true

# 3) 组件包 → 产品包（productbuild 给 Installer.app 用的分发壳）
PKG="$OUT/MyNet_Agent_${VERSION}.pkg"
pkgbuild --root "$STAGE" --scripts "$SCRIPTS" \
  --identifier club.mynet.agent --version "$PKG_VER" \
  --install-location / "$OUT/mynet_agent_component.pkg"
cat > "$OUT/Distribution" <<EOF
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>MyNet Agent ${VERSION} (darwin/${ARCH})</title>
    <options customize="never" rootVolumeOnly="true"/>
    <choices-outline>
        <line choice="default">
            <line choice="club.mynet.agent"/>
        </line>
    </choices-outline>
    <choice id="default"/>
    <choice id="club.mynet.agent" visible="false">
        <pkg-ref id="club.mynet.agent"/>
    </choice>
    <pkg-ref id="club.mynet.agent" version="${PKG_VER}" onConclusion="none">mynet_agent_component.pkg</pkg-ref>
</installer-gui-script>
EOF
productbuild --distribution "$OUT/Distribution" --package-path "$OUT" "$PKG"

# 4) dmg：安装向导的载体（拖 pkg 进卷宗，双击走系统界面）
hdiutil create -volname "MyNet Agent" -srcfolder "$PKG" -ov -format UDZO \
  "dist/MyNet_Agent_${VERSION}.dmg" >/dev/null

echo "✓ $PKG"
echo "✓ dist/MyNet_Agent_${VERSION}.dmg"
echo "  本机验证: sudo installer -pkg $PKG -target /"
