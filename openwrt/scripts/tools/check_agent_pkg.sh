#!/bin/sh
# 校验 mynet-agent 包的打包不变量（V2-9）。
#
# 为什么需要：真出 ipk 要 OpenWrt SDK（本机没有、CI 也没打这个包），
# 所以「缺 files/mynetd 时必须构建失败」这条一旦写错，**没有任何环节会发现**。
# 本脚本做两件事：
#   1. 静态断言 Makefile 里的不变量
#   2. 把 Makefile 中那段守卫**抽出来真跑**——验的是 Makefile 的实际文本，
#      不是我另写一份复刻（复刻通过不代表 Makefile 通过）
#
# 用法：sh scripts/tools/check_agent_pkg.sh [Makefile 路径]
set -eu

MK=${1:-Makefile}

if [ ! -f "$MK" ]; then
  echo "✗ 找不到 $MK" >&2
  exit 1
fi

fail=0
note() { printf '  %s\n' "$1"; }
bad() { printf '  ✗ %s\n' "$1"; fail=1; }
ok() { printf '  ✓ %s\n' "$1"; }

echo "▸ mynet-agent 打包不变量（${MK}）"

# ── 1. 取出 Package/mynet-agent 的定义块与 install 块 ─────────────
agent_block=$(awk '/^define Package\/mynet-agent$/,/^endef$/' "$MK")
install_block=$(awk '/^define Package\/mynet-agent\/install$/,/^endef$/' "$MK")

if [ -z "$agent_block" ]; then
  bad "找不到 Package/mynet-agent 定义块"
fi
if [ -z "$install_block" ]; then
  bad "找不到 Package/mynet-agent/install 块"
fi
[ "$fail" -eq 0 ] || exit 1

# ── 2. 静态不变量 ────────────────────────────────────────────────

# 2a. mynet-agent 是编译产物包，不得是 PKGARCH:=all
if printf '%s\n' "$agent_block" | grep -qE '^[[:space:]]*PKGARCH[[:space:]]*:?=[[:space:]]*all'; then
  bad "Package/mynet-agent 设了 PKGARCH:=all —— 它装的是编译产物 mynetd，架构相关；"\
"设 all 会让 ipk 声称适配所有架构，opkg 不拦、运行期才崩"
else
  ok "mynet-agent 未设 PKGARCH:=all（按目标架构出包）"
fi

# 2b. install 里必须引用 files/mynetd
if printf '%s\n' "$install_block" | grep -q 'files/mynetd'; then
  ok "install 引用了 files/mynetd"
else
  bad "install 块没有引用 files/mynetd"
fi

# 2c. 装二进制的动作不得被 `|| true` 吞掉
#     （注意：postinst/prerm 里的 `|| true` 是合理的，只查 install 块）
if printf '%s\n' "$install_block" | grep -E 'INSTALL_BIN.*mynetd' | grep -q '||[[:space:]]*true'; then
  bad "install 里装 mynetd 的动作带 || true —— 缺文件时会静默产出空壳 ipk"
else
  ok "装 mynetd 的动作未被 || true 兜底"
fi

# 2c-2. mynet（L6 唯一写入者）同样必须硬依赖
if printf '%s\n' "$install_block" | grep -q 'files/mynet'; then
  ok "install 引用了 files/mynet"
else
  bad "install 块没有引用 files/mynet —— 路由唯一写入者缺失时 Lua apply 会断"
fi
if printf '%s\n' "$install_block" | grep -E 'INSTALL_BIN.*[[:space:]]mynet' | grep -q '||[[:space:]]*true'; then
  bad "install 里装 mynet 的动作带 || true"
else
  ok "装 mynet 的动作未被 || true 兜底"
fi

# 2d. 必须存在显式的失败守卫
if printf '%s\n' "$install_block" | grep -q '\[ ! -f ./files/mynetd \]'; then
  ok "存在显式失败守卫（[ ! -f ./files/mynetd ]）"
else
  bad "缺少显式失败守卫 —— 缺 files/mynetd 时构建行为未定义"
  exit 1
fi
if printf '%s\n' "$install_block" | grep -q '\[ ! -f ./files/mynet \]'; then
  ok "存在显式失败守卫（[ ! -f ./files/mynet ]）"
else
  bad "缺少显式失败守卫 —— 缺 files/mynet 时构建行为未定义"
  exit 1
fi

# ── 3. 把守卫抽出来真跑 ──────────────────────────────────────────
# 抽出从 `@if [ ! -f ./files/mynetd ]` 到配对的 `fi` 之间的行，
# 去掉行首的 @ 与制表符缩进，交给 sh 执行。
guard=$(printf '%s\n' "$install_block" | awk '
  /\[ ! -f \.\/files\/mynetd \]/ { grab = 1 }
  grab { print }
  grab && /^[[:space:]]*fi[[:space:]]*$/ { exit }
' | sed 's/^[[:space:]]*@\{0,1\}//')

if [ -z "$guard" ]; then
  bad "守卫抽取失败（守卫文本变了？本检查需同步更新）"
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 3a. 缺文件 → 必须失败
( cd "$tmp" && mkdir -p . && sh -c "$guard" >/dev/null 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 0 ]; then
  bad "缺 files/mynetd 时守卫**没有失败**（退出码 0）—— 正是要防的空壳 ipk 场景"
else
  ok "缺 files/mynetd 时守卫失败（退出码 ${rc}）"
fi

# 3b. 缺文件时的报错必须说清怎么补（否则现场只能靠猜）
msg=$( cd "$tmp" && sh -c "$guard" 2>&1 || true )
if printf '%s' "$msg" | grep -q 'files/mynetd'; then
  ok "报错指明了 files/mynetd"
else
  bad "报错没有指明缺的是哪个文件"
fi
if printf '%s' "$msg" | grep -qiE 'mipsel|mipsle'; then
  ok "报错提醒了 mipsle/mipsel 命名差异"
else
  note "· 报错未提醒 mipsle/mipsel 差异（建议补上）"
fi

# 3c. 文件存在 → 守卫必须放行
mkdir -p "$tmp/files" && : > "$tmp/files/mynetd"
if ( cd "$tmp" && sh -c "$guard" >/dev/null 2>&1 ); then
  ok "files/mynetd 存在时守卫放行"
else
  bad "files/mynetd 存在时守卫仍然失败 —— 会挡住正常构建"
fi

# 3d. mynet 守卫：抽出并真跑（与 mynetd 同一套方法，防「写了但没生效」）
# 注意避开 mynetd 的 `[ ! -f ./files/mynetd ]`（它以 mynet 前缀开头）
guard_mynet=$(printf '%s\n' "$install_block" | awk '
  /if \[ ! -f \.\/files\/mynet \]/ { grab = 1 }
  grab { print }
  grab && /^[[:space:]]*fi[[:space:]]*$/ { exit }
' | sed 's/^[[:space:]]*@\{0,1\}//')
if [ -z "$guard_mynet" ]; then
  bad "mynet 守卫抽取失败"
else
  ( cd "$tmp" && sh -c "$guard_mynet" >/dev/null 2>&1 ) && rc=0 || rc=$?
  if [ "$rc" -eq 0 ]; then
    bad "缺 files/mynet 时守卫**没有失败**"
  else
    ok "缺 files/mynet 时守卫失败（退出码 ${rc}）"
  fi
  : > "$tmp/files/mynet"
  if ( cd "$tmp" && sh -c "$guard_mynet" >/dev/null 2>&1 ); then
    ok "files/mynet 存在时守卫放行"
  else
    bad "files/mynet 存在时守卫仍然失败"
  fi
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "✓ mynet-agent 打包不变量通过"
else
  echo "✗ mynet-agent 打包不变量未通过"
  exit 1
fi
