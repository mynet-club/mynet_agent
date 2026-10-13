#!/bin/sh
# dev-push-0.32.sh —— 开发机 → 0.32 一键联调（B6）。
#
#   sh scripts/dev-push-0.32.sh [--host 192.168.0.32] [--user arthur] [--target /opt/mynet]
#
# 只推「同构建套件」+ 校验和：
#   agent: mynet / mynetd（darwin 或 linux/amd64 按目标选）
#   gnb:   gnb / gnb_ctl / gnb_es / gnb_crypto（同一构建，缺一不推）
#
# 落盘前在远端 sha256sum -c 通过才覆盖；失败不落地。
# 推的是**开发联调包**，不是生产分发 —— 生产只吃 CI/ctl 产物。

set -eu

HOST="192.168.0.32"
USER_NAME="${USER:-arthur}"
TARGET="/opt/mynet"
AGENT_DIST=""
GNB_DIST=""
DRY=0

while [ $# -gt 0 ]; do
    case "$1" in
        --host) HOST="$2"; shift 2 ;;
        --user) USER_NAME="$2"; shift 2 ;;
        --target) TARGET="$2"; shift 2 ;;
        --agent-dist) AGENT_DIST="$2"; shift 2 ;;
        --gnb-dist) GNB_DIST="$2"; shift 2 ;;
        --dry-run) DRY=1; shift ;;
        -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

REPO_AGENT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_GNB="$(cd "$REPO_AGENT/../mynet_gnb" 2>/dev/null && pwd || echo "")"

echo "▸ dev-push → ${USER_NAME}@${HOST}:${TARGET}"

# ── 1) 找 agent 产物 ─────────────────────────────────────
if [ -z "$AGENT_DIST" ]; then
    # 目标是 linux/amd64 时用 linux 包；本机联调可用 darwin
    AGENT_DIST=$(ls -d "$REPO_AGENT"/dist/mynet_agent_linux_amd64_* 2>/dev/null | grep -v '\.tgz$' | tail -1)
fi
if [ -z "$AGENT_DIST" ] || [ ! -d "$AGENT_DIST" ]; then
    echo "✗ 找不到 agent 产物目录。先: cd mynet_agent && make dist" >&2
    echo "  或显式 --agent-dist dist/mynet_agent_linux_amd64_..." >&2
    exit 1
fi

# ── 2) 找 gnb 同构建套件 ─────────────────────────────────
# 0.32 是 linux/amd64 → 优先 linux 套件；没有再退 darwin（本机自测）
if [ -z "$GNB_DIST" ] && [ -n "$REPO_GNB" ]; then
    GNB_DIST=$(ls -d "$REPO_GNB"/dist/gnb-*-linux-* 2>/dev/null | tail -1)
fi
if [ -z "$GNB_DIST" ] && [ -n "$REPO_GNB" ]; then
    GNB_DIST=$(ls -d "$REPO_GNB"/dist/gnb-*-darwin-* 2>/dev/null | tail -1)
fi
if [ -z "$GNB_DIST" ] || [ ! -d "$GNB_DIST" ]; then
    echo "✗ 找不到 gnb 套件目录。先: cd mynet_gnb && make gnb-dist" >&2
    exit 1
fi

# 同构建强制（R2）：四件套缺一不推
for prog in gnb gnb_ctl gnb_es gnb_crypto; do
    if [ ! -x "$GNB_DIST/$prog" ] && [ ! -x "$GNB_DIST/$prog.exe" ]; then
        echo "✗ gnb 套件缺 $prog —— 不推（同构建强制）" >&2
        exit 1
    fi
done
if [ ! -f "$GNB_DIST/MANIFEST" ]; then
    echo "⚠ gnb 套件无 MANIFEST（建议 make gnb-dist，带基线/补丁戳）" >&2
fi

echo "  agent: $AGENT_DIST"
echo "  gnb:   $GNB_DIST"

if [ "$DRY" -eq 1 ]; then
    echo "（--dry-run）以上未推送"
    exit 0
fi

# ── 3) 打同构建 tar + 校验和 ─────────────────────────────
STAMP=$(date +%Y%m%d-%H%M%S)
STAGE=$(mktemp -d /tmp/dev-push.XXXXXX)
trap 'rm -rf "$STAGE"' EXIT

PKG="mynet-dev-$STAMP"
mkdir -p "$STAGE/$PKG"
cp "$AGENT_DIST"/mynet "$AGENT_DIST"/mynetd "$STAGE/$PKG/" 2>/dev/null || {
    cp "$AGENT_DIST"/mynet.exe "$AGENT_DIST"/mynetd.exe "$STAGE/$PKG/" 2>/dev/null || true
}
cp "$AGENT_DIST"/INSTALL.md "$STAGE/$PKG/" 2>/dev/null || true
for prog in gnb gnb_ctl gnb_es gnb_crypto; do
    cp "$GNB_DIST/$prog" "$GNB_DIST/$prog.exe" "$STAGE/$PKG/" 2>/dev/null || true
done
cp "$GNB_DIST/MANIFEST" "$STAGE/$PKG/" 2>/dev/null || true

cd "$STAGE"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$PKG" && sha256sum * > SHA256SUMS)
else
    (cd "$PKG" && shasum -a 256 * > SHA256SUMS)
fi
tar czf "$PKG.tgz" "$PKG"
echo "  本地包: $STAGE/$PKG.tgz"

# ── 4) 推送 + 远端校验后落盘 ─────────────────────────────
ssh "${USER_NAME}@${HOST}" "mkdir -p ${TARGET}/.incoming"
scp -q "$PKG.tgz" "${USER_NAME}@${HOST}:${TARGET}/.incoming/"

ssh "${USER_NAME}@${HOST}" "set -e
    cd ${TARGET}/.incoming
    tar xzf $PKG.tgz
    cd $PKG
    sha256sum -c SHA256SUMS
    # 校验通过才覆盖（同套件整目录落位）
    mkdir -p ${TARGET}/bin ${TARGET}/driver/gnb/bin
    cp -f mynet mynetd ${TARGET}/bin/ 2>/dev/null || cp -f mynet.exe mynetd.exe ${TARGET}/bin/
    cp -f gnb gnb_ctl gnb_es gnb_crypto ${TARGET}/driver/gnb/bin/ 2>/dev/null \\
      || cp -f gnb.exe gnb_ctl.exe gnb_es.exe gnb_crypto.exe ${TARGET}/driver/gnb/bin/
    cp -f MANIFEST ${TARGET}/driver/gnb/bin/ 2>/dev/null || true
    echo '✓ 已落位 ${TARGET}'
    ${TARGET}/bin/mynet version 2>/dev/null || ${TARGET}/bin/mynet.exe version 2>/dev/null || true
    cd ..
    rm -rf $PKG $PKG.tgz
"

echo "✓ 推送完成：${USER_NAME}@${HOST}:${TARGET}"
echo "  远端验收: ssh ${USER_NAME}@${HOST} '${TARGET}/bin/mynet subject --json'"
