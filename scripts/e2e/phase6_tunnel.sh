#!/bin/sh
# phase6_tunnel.sh — 0→1 的最后一环：**两节点真互通**，且证明流量真走隧道。
#
# 为什么不用现成的 g3_tunnel.sh / u1_mesh.sh：那两个只 ping，而且拓扑写死在
# 旧的 U1 手工配置上（/tmp/t1-gnb/…，绕过 back 下发）。ping 通只能说明
# 「两个 overlay IP 之间可达」，说明不了「是平台下发的配置让它可达的」，
# 更说明不了「包真的进了隧道而不是从物理口绕过去」。
# 按 AGENTS.md §7.1 D2/D4，这里每一步都断言，且互通要附三重独立证据。
#
# 本脚本从**工作站**跑，通过 ssh 驱动两台节点机。它做的事：
#   1. 节点侧：mynetd 拉 bundle → refresh-config 渲染 gnb 原生配置 →
#      service start gnb 拉起守护进程 → detector → refresh-routes → apply 装路由
#   2. 断言配置目录齐全、格式对（nodeid / route.conf 三段点分 / address.conf 四段）
#   3. 断言互通，并给出三重证据：
#        a. 双向 ping 0% 丢包
#        b. 在 gnb_tun 上 tcpdump 抓到 ICMP（抓的是隧道接口本身 —— 绕过隧道就不会有）
#        c. gnb 自己的 in/out 计数器增长（ping 前后对比）
#      另附 gnb_ctl -s 的 `Direct Point to Point`（gnb 自己的在线判据，PONG 位置位才打）
#   4. 断言心跳有数据（back 侧 node_monitor_snapshots + Redis presence=online）
#
# 用法：
#   P6_NODE_A=192.168.0.32 P6_NODE_B=192.168.0.16 \
#   P6_ID_A=2132596500330640 P6_ID_B=1005272783125838 \
#   P6_IP_A=10.0.0.10 P6_IP_B=10.0.0.16 \
#     sh scripts/e2e/phase6_tunnel.sh
#
# 前置（本脚本不做，因为要在 back 侧做）：两个节点已建、已上传公钥、
# 已是好友、连接申请已 send + accept、已设 endpoint。见 README 或
# docs/knowledge-base/production-gates-handoff.md。
#
# 依赖：ssh（免密）、curl、python3；节点机上要有 gnb / gnb_ctl / sudo -n。
set -u

A_HOST="${P6_NODE_A:-192.168.0.32}"
B_HOST="${P6_NODE_B:-192.168.0.16}"
A_ID="${P6_ID_A:-}"
B_ID="${P6_ID_B:-}"
A_IP="${P6_IP_A:-}"
B_IP="${P6_IP_B:-}"
A_USER="${P6_USER:-arthur}"
B_USER="${P6_USER:-arthur}"
MH="${P6_HOME:-/opt/mynet}"   # MYNET_HOME，绝对路径（systemd 安装后的默认位置）
GW="${P6_GW:-wss://stage-gw.mynet.club/v1/node/connect}"
API="${P6_API:-https://stage-api.mynet.club}"

pass=0; fail=0
ok()  { printf '  ✓ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ✗ %s\n' "$1"; fail=$((fail+1)); }
phase() { printf '\n━━━ %s ━━━\n' "$1"; }
die() { printf '\n✗ 停在：%s\n' "$1"; printf '  已通过 %s / 失败 %s\n' "$pass" "$fail"; exit 1; }

[ -n "$A_ID" ] && [ -n "$B_ID" ] && [ -n "$A_IP" ] && [ -n "$B_IP" ] ||
  die "需要 P6_ID_A / P6_ID_B / P6_IP_A / P6_IP_B（节点 ID 与平台分配的 overlay IP）"
command -v ssh >/dev/null 2>&1 || die "缺 ssh"

# 在节点机上跑一段脚本。用 base64 传，避免多层引号与全角标点吃掉变量名
# （本项目踩过的坑：`$VAR` 后紧跟全角标点时 shell 会把 UTF-8 字节并入变量名）。
#
# ⚠️ 动态值（MYNET_HOME / gateway / 对端 IP）一律用**环境变量**送进去，
# 不在生成期做字符串展开。原因就是下面 node_script 的那条注释。
on() { # <user@host> <script> [peer-ip]
  _b64=$(printf '%s' "$2" | base64)
  ssh -o BatchMode=yes -o ConnectTimeout=10 "$1" \
    "export LC_ALL=C P6_MH='$MH' P6_GW='$GW' P6_PEER='${3:-}'; echo $_b64 | base64 -d | sh" 2>&1 |
    grep -v 'cannot change locale'
}

node_script() { # 生成节点侧要跑的脚本（无参；动态值由 on() 用环境变量送进去）
  # ⚠️⚠️ 这里必须是**引号 heredoc**（<<'NODE'），一个字符都不能在生成期展开。
  #
  # 之前用的是非引号 <<NODE，于是脚本里所有 `$`、`$( )`、以及**注释里的 markdown
  # 反引号**都会在「生成脚本」这一步就被我的工作站 shell 执行掉。实测：注释里
  # 写了一句「退回手工 `mynetd -once`」，生成时它真的在工作站上跑了 mynetd，
  # 报 `command substitution: syntax error near unexpected token '<'`，
  # 而生成出来的脚本因为反引号被吃掉变成**未闭合**，整个文件 EOF 报错。
  # 更早几版能跑通纯属运气 —— 被误执行的内容恰好无害。
  #
  # 结论：往远端送脚本时，「生成」与「执行」必须分离；生成期不做任何展开，
  # 参数走环境变量。这条比脚本本身值钱。
  cat <<'NODE'
set -u
H="$P6_MH"
G="$H/var/gnb"
PEER="$P6_PEER"
SU="sudo -n env MYNET_HOME=$H MYNET_BIN=$H/bin/mynet PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"

echo "STEP version=$($H/bin/mynet version 2>&1)"

# 1) 拉 bundle。优先用 systemd 单元（生产形态）：restart 会连带跑一遍**启动生命周期**
#    （refresh-config → service start → apply --auto），正是要验的东西。
#    单元不存在时退回手工 mynetd -once（-once 现在会等启动拉取结束才退出，
#    见 cmd/mynetd/main.go 的 bundlePullDone）。
#    一律走 sudo：装成服务后 $H/var 是 root 属主，普通用户写不进去。
if systemctl show -p LoadState --value mynetd.service 2>/dev/null | grep -qx loaded; then
  echo "STEP via=systemd"
  sudo -n rm -f "$H/var/bundle.json" "$H/var/bundle.etag"
  sudo -n systemctl restart mynetd
  i=0; until [ -f "$H/var/bundle.json" ] || [ "$i" -ge 40 ]; do sleep 0.5; i=$((i+1)); done
  echo "STEP pull_rc=0"
else
  echo "STEP via=manual-once"
  sudo -n rm -f "$H/var/bundle.json" "$H/var/bundle.etag"
  timeout 60 $SU MYNET_GATEWAYS="$P6_GW" "$H/bin/mynetd" -listen 127.0.0.1:18811 -once > /tmp/p6-pull.log 2>&1
  echo "STEP pull_rc=$?"
fi
if [ -f "$H/var/bundle.json" ]; then
  echo "STEP bundle_bytes=$(wc -c < "$H/var/bundle.json")"
else
  echo "STEP bundle_bytes=0"
fi
P6_BUNDLE="$H/var/bundle.json" python3 - <<'PY'
import json, os
p = os.environ["P6_BUNDLE"]
try:
    d = json.load(open(p))
except Exception as e:
    print("STEP bundle_parse_fail=%s" % e); raise SystemExit
g = d.get("gnb") or {}
net = d.get("network") or {}
print("STEP gen=%s" % d.get("generation"))
print("STEP addresses=%s" % json.dumps(net.get("addresses")))
print("STEP osroutes=%s" % json.dumps(net.get("routes")))
print("STEP peer_pubs=%d" % len((g.get("identity") or {}).get("peer_public_keys") or {}))
print("STEP static_nodes=%d" % len((g.get("discovery") or {}).get("static_nodes") or []))
PY

# 2) 渲染 gnb 原生配置（sudo：要写 $H/var/gnb 与其下密钥）
$SU "$H/bin/mynet" refresh-config --auto > /tmp/p6-refresh.log 2>&1
echo "STEP refresh_rc=$?"
for pat in node_conf:node.conf route_conf:route.conf address_conf:address.conf; do
  k=${pat%%:*}; n=${pat#*:}
  if sudo -n test -f "$G/$n"; then echo "STEP has_$k=1"; else echo "STEP has_$k=0"; fi
done
echo "STEP nodeid_line=$(sudo -n grep -c '^nodeid ' "$G/node.conf" 2>/dev/null)"
echo "STEP bad_node_key=$(sudo -n grep -c '^node ' "$G/node.conf" 2>/dev/null)"
echo "STEP route_bad_sep=$(sudo -n grep -c ' ' "$G/route.conf" 2>/dev/null)"
echo "STEP addr_fields=$(sudo -n head -1 "$G/address.conf" 2>/dev/null | awk -F'|' '{print NF}')"

# 密钥文件必须**恰好** 128 / 64 字节且无换行：gnb 用裸 read() 且要求读满，
# 读不满就 exit(0)（gnb_keys.c:68-73, 84-89, 108-112）。
# 用 sudo find 拿文件名（root 才穿得过 700 的 security/）+ sudo wc -c FILE
# （用参数而非 `<` 重定向：重定向由外层 shell 以普通用户身份做会 EACCES，
#  且 dash 不在重定向里展开通配符）。
for pat in 'security:*.private:priv_bytes' 'security:*.public:pub_bytes' 'ed25519:*.public:peerpub_bytes'; do
  d=${pat%%:*}; rest=${pat#*:}; g2=${rest%%:*}; k=${rest##*:}
  f=$(sudo -n find "$G/$d" -maxdepth 1 -name "$g2" -type f 2>/dev/null | head -1)
  if [ -n "$f" ]; then n=$(sudo -n wc -c "$f" 2>/dev/null | awk '{print $1}'); else n=0; fi
  echo "STEP $k=${n:-0}"
done

# 3) 起驱动。systemd 托管时 mynet service 会**委托 systemctl**
#    （见 driver/gnb.go 的 systemdManages：gnb --systemd 不写 pid 文件，
#     不委托就会 spawn 出第二个 gnb 抢同一个 tun 与 UDP 端口）
timeout 60 $SU "$H/bin/mynet" service start gnb > /tmp/p6-start.log 2>&1
echo "STEP start_rc=$?"
sed 's/^/STEP start_msg=/' /tmp/p6-start.log
timeout 30 $SU "$H/bin/mynet" service status gnb 2>&1 | sed 's/^/STEP status=/'
# 正则刻意不用括号分组，减少一层解析器边界情况
echo "STEP gnb_procs=$(ps -eo args | grep -c '^/usr/local/bin/gnb ')"

# 4) 装 OS 路由（gnb 自己不写内核路由表；tun 重建后路由会丢，必须重装）
$SU "$H/bin/mynet" detector > /dev/null 2>&1
timeout 60 $SU "$H/bin/mynet" apply --auto > /tmp/p6-apply.log 2>&1
echo "STEP apply_rc=$?"
sed 's/^/STEP apply_msg=/' /tmp/p6-apply.log
echo "STEP route_to_peer=$(ip route get "$PEER" 2>/dev/null | head -1)"
NODE
}

# ── 逐节点跑装配 ─────────────────────────────────────────────
run_node() { # <label> <user@host> <peer-ip>
  phase "节点 $1（$2）装配"
  _out=$(on "$2" "$(node_script)" "$3")
  printf '%s\n' "$_out" | grep '^STEP ' | sed 's/^STEP /    /'
  _g() { printf '%s\n' "$_out" | sed -n "s/^STEP $1=//p" | head -1; }

  [ "$(_g bundle_bytes)" -gt 0 ] 2>/dev/null && ok "$1: bundle 已落盘（$(_g bundle_bytes) 字节，gen=$(_g gen)）" \
    || { bad "$1: bundle 未落盘"; return 1; }
  [ "$(_g peer_pubs)" -ge 1 ] 2>/dev/null && ok "$1: bundle 带对端公钥（$(_g peer_pubs) 个）" \
    || bad "$1: bundle 里没有对端公钥 —— 授权连接没建立或没重新发布"
  [ "$(_g static_nodes)" -ge 1 ] 2>/dev/null && ok "$1: bundle 带对端传输地址（$(_g static_nodes) 个）" \
    || bad "$1: bundle 里没有 static_nodes —— 节点没设 endpoint，gnb 找不到对端"
  [ "$(_g osroutes)" != "[]" ] && [ -n "$(_g osroutes)" ] && ok "$1: bundle 带 OS 路由 $(_g osroutes)" \
    || bad "$1: network.routes 为空 —— ping 会走默认路由从物理口出去"

  [ "$(_g has_node_conf)" = 1 ] && [ "$(_g has_route_conf)" = 1 ] && [ "$(_g has_address_conf)" = 1 ] \
    && ok "$1: 配置目录三件齐全（node.conf / route.conf / address.conf）" \
    || bad "$1: 配置目录缺文件"
  [ "$(_g nodeid_line)" = 1 ] && ok "$1: node.conf 用的是 gnb 认的 \`nodeid\` 键" \
    || bad "$1: node.conf 缺 nodeid 行"
  [ "$(_g bad_node_key)" = 0 ] && ok "$1: 没有 gnb 不认的 \`node \` 键（旧缺陷）" \
    || bad "$1: node.conf 里还有 \`node \` —— gnb 会静默丢弃这行"
  [ "$(_g route_bad_sep)" = 0 ] && ok "$1: route.conf 无空格分隔行（旧缺陷会让整份被跳过）" \
    || bad "$1: route.conf 有空格分隔的行"
  [ "$(_g addr_fields)" = 4 ] && ok "$1: address.conf 是 4 段（attrib|uuid|host|port）" \
    || bad "$1: address.conf 段数=$(_g addr_fields)，应为 4"
  [ "$(_g priv_bytes)" = 128 ] && ok "$1: security/*.private 恰好 128 字节（无换行）" \
    || bad "$1: 私钥文件 $(_g priv_bytes) 字节，必须恰好 128 —— gnb 裸 read() 读不满就 exit(0)"
  [ "$(_g pub_bytes)" = 64 ] && ok "$1: security/*.public 恰好 64 字节" \
    || bad "$1: 本机公钥文件 $(_g pub_bytes) 字节，必须恰好 64"
  [ "$(_g peerpub_bytes)" = 64 ] && ok "$1: ed25519/*.public 恰好 64 字节" \
    || bad "$1: 对端公钥文件 $(_g peerpub_bytes) 字节，必须恰好 64"

  case "$(_g status)" in *running=true*) ok "$1: gnb 守护进程在跑（$(_g status)）" ;;
    *) bad "$1: gnb 没在跑（$(_g status)）" ;; esac
  # ⚠️ 必须恰好 1 个。`gnb --systemd` 会忽略 node.conf 的 pid-file 键
  # （gnb_conf_file.c:349 带 `&& 0 == conf->systemd_daemon`），于是没有 pid 文件 →
  # IsProcessAlive 恒 false → Start() 会 spawn **第二个 gnb**，两个进程抢同一个
  # tun 与 UDP 端口，症状是隧道时通时断而两边日志都"正常"。
  # mynet service 现在会委托 systemctl（driver/gnb.go 的 systemdManages），这条钉住它。
  [ "$(_g gnb_procs)" = 1 ] && ok "$1: gnb 恰好 1 个实例（没有与 systemd 起重复进程）" \
    || bad "$1: gnb 实例数=$(_g gnb_procs)，必须是 1（>1 说明 service 没委托 systemctl）"
  case "$(_g route_to_peer)" in *gnb_tun*) ok "$1: 到对端的路由走 gnb_tun" ;;
    *) bad "$1: 到对端的路由不走 gnb_tun（$(_g route_to_peer)）" ;; esac
  return 0
}

run_node A "$A_USER@$A_HOST" "$B_IP" || die "节点 A 装配未通过"
run_node B "$B_USER@$B_HOST" "$A_IP" || die "节点 B 装配未通过"

# ── 互通 + 三重证据 ──────────────────────────────────────────
phase "互通验证（三重独立证据）"

# 证据 c 的前半：ping 前的 gnb 计数器
counters() { # <user@host>
  # LC_ALL=C 要传进 sudo 的 env：sudo 起的 bash 会吐
  # `cannot change locale (en_US.UTF-8)` 到 stderr，被 on() 的 2>&1 收进来
  # 就会污染这里的输出（实测把 B 的计数值前面粘了一行警告）。
  on "$1" 'sudo -n env LC_ALL=C PATH=/usr/local/bin:/usr/bin:/bin gnb_ctl -b '"$MH"'/var/gnb/gnb.map -c -s 2>/dev/null | awk "/^in /{i=\$2} /^out /{o=\$2} END{print i\"/\"o}"' | tail -1
}
BEFORE_A=$(counters "$A_USER@$A_HOST"); BEFORE_B=$(counters "$B_USER@$B_HOST")
printf '    ping 前计数器 A=%s B=%s\n' "$BEFORE_A" "$BEFORE_B"

# 证据 b：在 A 的 gnb_tun 上抓包（后台起，抓完再取）
on "$A_USER@$A_HOST" 'rm -f /tmp/p6-cap.txt; sudo -n timeout 30 tcpdump -ni gnb_tun -c 6 icmp > /tmp/p6-cap.txt 2>/dev/null &' >/dev/null
sleep 3

# 证据 a：双向 ping
pingdir() { # <label> <user@host> <target-ip>
  _o=$(on "$2" "ping -c 5 -W 2 $3 2>&1 | tail -2")
  printf '%s\n' "$_o" | sed 's/^/    /'
  case "$_o" in *" 0% packet loss"*) ok "$1 → $3：0% 丢包" ;;
    *) bad "$1 → $3：有丢包" ;; esac
}
pingdir A "$A_USER@$A_HOST" "$B_IP"
pingdir B "$B_USER@$B_HOST" "$A_IP"

sleep 4
# 证据 b 的后半：取抓包结果
CAP=$(on "$A_USER@$A_HOST" 'cat /tmp/p6-cap.txt 2>/dev/null')
printf '%s\n' "$CAP" | head -6 | sed 's/^/    /'
_n=$(printf '%s\n' "$CAP" | grep -c 'ICMP echo')
[ "$_n" -ge 2 ] 2>/dev/null && ok "在 gnb_tun 上抓到 $_n 个 ICMP 包（抓的是隧道接口本身，绕过隧道就不会有）" \
  || bad "gnb_tun 上没抓到 ICMP —— 流量可能没进隧道"
printf '%s\n' "$CAP" | grep -q "echo request" && printf '%s\n' "$CAP" | grep -q "echo reply" \
  && ok "抓到的是成对的 request/reply（不是单边）" || bad "抓包里 request/reply 不成对"

# 证据 c 的后半：计数器增长
AFTER_A=$(counters "$A_USER@$A_HOST"); AFTER_B=$(counters "$B_USER@$B_HOST")
printf '    ping 后计数器 A=%s B=%s\n' "$AFTER_A" "$AFTER_B"
[ "$AFTER_A" != "$BEFORE_A" ] && ok "A 的 gnb in/out 计数器有增长（${BEFORE_A} → ${AFTER_A}）" \
  || bad "A 的 gnb 计数器没变 —— 包没经过 gnb"
[ "$AFTER_B" != "$BEFORE_B" ] && ok "B 的 gnb in/out 计数器有增长（${BEFORE_B} → ${AFTER_B}）" \
  || bad "B 的 gnb 计数器没变 —— 包没经过 gnb"

# gnb 自己的在线判据
for spec in "A:$A_USER@$A_HOST" "B:$B_USER@$B_HOST"; do
  _l=${spec%%:*}; _h=${spec#*:}
  # 同上：只取最后一行并剔除非数字，避免 sudo 的 setlocale 警告把计数值污染成非数字
  _s=$(on "$_h" 'sudo -n env LC_ALL=C PATH=/usr/local/bin:/usr/bin:/bin gnb_ctl -b '"$MH"'/var/gnb/gnb.map -c -s 2>/dev/null | grep -cE "Direct Point to Point"' | tail -1 | tr -dc '0-9')
  [ "${_s:-0}" -ge 1 ] 2>/dev/null && ok "$_l: gnb_ctl 报 Direct Point to Point（PONG 位置位，直连非中继）" \
    || bad "$_l: 没有 Direct Point to Point（可能是中继或没通）"
done

# ── 心跳/监控有数据 ──────────────────────────────────────────
phase "心跳与监控数据（0→1 的最后一项）"
for spec in "A:$A_ID" "B:$B_ID"; do
  _l=${spec%%:*}; _id=${spec#*:}
  _r=$(curl -sS -m 20 "$API/api/v1/monitor/node/info" -H "X-Node-Id: $_id" 2>/dev/null | head -c 200)
  # 节点面端点要签名，这里只看它不是 404/500 —— 真正的判据在 back 侧的库与 Redis，
  # 那需要服务器权限，本脚本不假设能登 back，所以只给出提示而不假装验过。
  case "$_r" in *'"success"'*) ok "$_l: /monitor/node/info 有响应" ;;
    *) printf '  ⚠ %s: /monitor/node/info 需要节点面签名，本脚本未验（%s）\n' "$_l" "$(printf '%s' "$_r" | head -c 80)" ;;
  esac
done
cat <<'NOTE'
  ℹ 心跳落库与 presence 要在 back 侧查（需服务器权限），命令如下：
      SELECT node_id, COUNT(*), MAX(snapshot_at) FROM node_monitor_snapshots
        WHERE node_id IN (<A>,<B>) GROUP BY node_id;
      SELECT node_id,node_status,last_seen FROM nodes WHERE node_id IN (<A>,<B>);
      redis-cli -n 1 hgetall "<prefix>node:status:<A>"     # 期望 status=online
      redis-cli -n 1 get "<prefix>node:vpn_interfaces:<A>" # 期望 ifname/status=up/ip
    ⚠ Redis 键有前缀 Str::slug(APP_NAME).'_database_'，裸 node:* 扫不到。
NOTE

printf '\n════════ Phase 6：%s 通过 / %s 失败 ════════\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
