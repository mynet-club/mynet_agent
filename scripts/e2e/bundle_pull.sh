#!/bin/sh
# bundle_pull.sh — V1-10① 的端到端验证：节点真能从 back 拉到 BundleEnvelope 并落盘
#
# 为什么要有这个脚本：此前的断点是「全仓没有任何代码**写** var/bundle.json」，
# 于是 `mynet refresh-config/--nodes --auto` 一律报
#   读 bundle 失败: …/var/bundle.json: no such file or directory
# 而 gateway 推来的 bundle_changed 也只是去跑那个注定失败的 refresh。
# 单元测试能证明 store/client 各自对，但证明不了**三个真二进制串起来通** ——
# 按 AGENTS.md §7.1 D2/D4，勾选必须附实测输出，且测试要打真的。
#
# 拓扑：假 back（会下发 envelope）→ 真 mynet_gateway（/v1/relay）→ 真 mynetd
#       → 落盘 $MYNET_HOME/var/bundle.json → 真 mynet refresh-config 能读到它
#
# 用法：sh scripts/e2e/bundle_pull.sh
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"

WORK=$(mktemp -d)
BIN="$WORK/bin"
HOME_DIR="$WORK/home"
mkdir -p "$BIN" "$HOME_DIR/conf"

# 端口避开固定值，防止与在跑的实例撞车
GW_PORT=18731
BACK_PORT=18732

cleanup() {
  # ⚠️ 按 PID 杀，不按名字（AGENTS.md §3：多实例可同名并存，禁 pkill/killall）
  for p in "$WORK"/*.pid; do
    [ -f "$p" ] || continue
    pid=$(cat "$p" 2>/dev/null || true)
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  sleep 0.3
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

pass=0; fail=0
ok()  { printf '  ✓ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ✗ %s\n' "$1"; fail=$((fail+1)); }

echo "▸ 构建三个真二进制"
go build -o "$BIN/mynet_gateway" ./cmd/mynet_gateway
go build -o "$BIN/mynetd" ./cmd/mynetd
go build -o "$BIN/mynet" ./cmd/mynet
ok "构建完成（mynet_gateway / mynetd / mynet）"

# ── 节点身份：用 C-2 的 Go↔PHP 双端互认向量（mynet/keys/testdata/node_face_vectors.json）
cat > "$HOME_DIR/conf/mynet.conf" <<'CONF'
NODE_ID=1245816621804034
NODE_PRIV_HEX=a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1
NODE_PUB_HEX=0000000000000000000000000000000000000000000000000000000000000000
BACK_PUB_HEX=db48257e1237976a74ad8cfedca00213408fe89ac6251f1b930245f242b5c31a
CONF
chmod 600 "$HOME_DIR/conf/mynet.conf"

# ── 假 back：只实现节点面会用到的两个端点 ────────────────────
# envelope 的字段按 spec/schemas/bundle-envelope-v2.schema.json 的 required 给全，
# 少一个就会被 mynetd 的 validateEnvelope 拒掉（那正是要验的行为）。
cat > "$WORK/fakeback.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

ENVELOPE = {
    "schema_version": 2,
    "generation": "gen-e2e-1",
    "checksum": "sha256:" + "0" * 64,
    "generated_at": "2026-09-25T00:00:00Z",
    "node": {"id": "1245816621804034", "interface": "gnb0", "mtu": 1400},
    "driver": "gnb",
    "network": {"addresses": ["10.23.0.5/24"]},
    # 带上 gnb intent，好让 refresh-config 真写出 node.conf —— 只验「文件读到了」
    # 证明不了整条链通。私钥用 C-2 的公开测试向量（node_face_vectors.json），不是真凭据。
    "gnb": {
        "identity": {
            "node_id": "1245816621804034",
            "key_algorithm": "ed25519",
            "private_key": "a1" * 32,
            "public_key": "00" * 32,
        },
        "listeners": ["0.0.0.0:9202"],
        "routes": [{"node_id": "4336588521958812", "prefix": "10.23.1.0/24"}],
    },
    "wireguard": None,
}
ETAG = '"gen-e2e-1"'
HITS = []

class H(BaseHTTPRequestHandler):
    def _send(self, code, body, extra=None):
        raw = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if raw:
            self.wfile.write(raw)

    def do_GET(self):
        path = self.path
        HITS.append(path)
        sys.stderr.write("fakeback: GET %s sig=%s inm=%r\n" % (
            path, "有" if self.headers.get("X-Node-Signature") else "无",
            self.headers.get("If-None-Match")))
        sys.stderr.flush()
        if path.startswith("/api/v1/monitor/node/bundle"):
            if self.headers.get("If-None-Match") == ETAG:
                self._send(304, b"", {"ETag": ETAG, "Cache-Control": "private, no-cache"})
            else:
                self._send(200, ENVELOPE, {"ETag": ETAG, "Cache-Control": "private, no-cache"})
        elif path.startswith("/api/v1/monitor/commands"):
            self._send(200, {"success": True, "node_id": 1245816621804034, "commands": []})
        else:
            self._send(404, {"success": False, "error": "not found"})

    def do_POST(self):
        HITS.append(self.path)
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        self._send(200, {"success": True})

    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

echo "▸ 起假 back（:$(printf '%s' "$BACK_PORT")）"
python3 "$WORK/fakeback.py" "$BACK_PORT" > "$WORK/back.log" 2>&1 &
echo $! > "$WORK/back.pid"

echo "▸ 起真 mynet_gateway（:$(printf '%s' "$GW_PORT") → upstream 假 back）"
"$BIN/mynet_gateway" -listen "127.0.0.1:$GW_PORT" \
  -upstream "http://127.0.0.1:$BACK_PORT" > "$WORK/gw.log" 2>&1 &
echo $! > "$WORK/gw.pid"

# 等两个进程真的在听（不用 sleep 猜）
i=0
until curl -sf "http://127.0.0.1:$GW_PORT/healthz" >/dev/null 2>&1; do
  i=$((i+1)); [ "$i" -gt 60 ] && { bad "gateway 起不来"; tail -5 "$WORK/gw.log"; exit 1; }
  sleep 0.2
done
ok "gateway /healthz 可达"

# ── 起点：确认 var/bundle.json 确实不存在（否则测不出「拉到」）──
if [ -e "$HOME_DIR/var/bundle.json" ]; then
  bad "起点不干净：var/bundle.json 已存在"
else
  ok "起点确认：var/bundle.json 不存在"
fi

echo "▸ 起真 mynetd（MYNET_HOME=${HOME_DIR}）"
MYNET_HOME="$HOME_DIR" \
MYNET_GATEWAYS="ws://127.0.0.1:$GW_PORT/v1/node/connect" \
MYNET_BIN="$BIN/mynet" \
  "$BIN/mynetd" -listen "127.0.0.1:18733" > "$WORK/mynetd.log" 2>&1 &
echo $! > "$WORK/mynetd.pid"

# 等启动拉取落盘
i=0
until [ -f "$HOME_DIR/var/bundle.json" ]; do
  i=$((i+1))
  if [ "$i" -gt 60 ]; then
    bad "启动后 12s 内没有落盘 var/bundle.json"
    echo "    ── mynetd 日志 ──"; sed 's/^/    /' "$WORK/mynetd.log" | tail -8
    echo "    ── 假 back 日志 ──"; sed 's/^/    /' "$WORK/back.log" | tail -8
    exit 1
  fi
  sleep 0.2
done
ok "启动即拉取：var/bundle.json 已落盘"

# ── 内容核对：必须是 back 下发的那一份，且执行器能解码 ──
if grep -q 'gen-e2e-1' "$HOME_DIR/var/bundle.json"; then
  ok "内容是 back 下发的 envelope（generation=gen-e2e-1）"
else
  bad "落盘内容不含期望的 generation：$(head -c 120 "$HOME_DIR/var/bundle.json")"
fi
if [ -s "$HOME_DIR/var/bundle.etag" ]; then
  ok "etag 已持久化（$(tr -d '\n' < "$HOME_DIR/var/bundle.etag")）—— 下次可走条件请求"
else
  bad "etag 未持久化"
fi
if grep -q 'sig=有' "$WORK/back.log"; then
  ok "假 back 收到了**带签名**的请求（X-Node-Signature 在）"
else
  bad "请求没带签名 —— 真 back 会 401"
fi

# ── 执行器侧：refresh-config 不再报「读 bundle 失败」──
echo "▸ 跑真 mynet refresh-config --auto（执行器只**读**这个文件）"
set +e
MYNET_HOME="$HOME_DIR" "$BIN/mynet" refresh-config --auto > "$WORK/refresh.log" 2>&1
rc=$?
set -e
if grep -q '读 bundle 失败' "$WORK/refresh.log"; then
  bad "执行器仍报「读 bundle 失败」—— 说明写的和读的不是同一个路径"
  sed 's/^/      /' "$WORK/refresh.log" | head -5
elif [ "$rc" -ne 0 ]; then
  bad "执行器读到了 bundle 但 refresh-config 退出码 ${rc}"
  sed 's/^/      /' "$WORK/refresh.log" | head -5
else
  ok "执行器读到了 bundle 且 refresh-config 成功（退出码 0）"
fi

# 整条链的终点：驱动原生配置真的落盘了（不只是「文件读到了」）
if [ -f "$HOME_DIR/var/gnb/node.conf" ]; then
  ok "refresh-config 写出了 var/gnb/node.conf（$(tr -d '\n' < "$HOME_DIR/var/gnb/node.conf" | head -c 60)…）"
else
  bad "refresh-config 成功却没写出 node.conf —— 链路终点没落地"
fi
if [ -f "$HOME_DIR/var/gnb/route.conf" ]; then
  ok "envelope 的 gnb.routes 落成了 route.conf"
else
  bad "缺 route.conf（envelope 里带了 gnb.routes）"
fi

# ── 304：再拉一次不该重写文件 ──
before=$(cksum "$HOME_DIR/var/bundle.json" | awk '{print $1}')
MYNET_HOME="$HOME_DIR" MYNET_GATEWAYS="ws://127.0.0.1:$GW_PORT/v1/node/connect" \
  "$BIN/mynetd" -listen "127.0.0.1:18734" -once > "$WORK/mynetd2.log" 2>&1 || true
# 不再 sleep：`-once` 现在会等启动拉取结束才退出（cmd/mynetd/main.go 的 -once 分支）。
# 此前这里靠 `sleep 1` 糊一个竞态 —— 拉取跑在 goroutine 里而 -once 做完心跳就 return，
# 于是它可能在拉取完成前退出。实测同一条命令在两台真机上一台落盘、一台 var/ 全空，
# **两边退出码都是 0、日志都没错误**。而下面原先那个 `before == after` 分支
# 在「拉取根本没发生」时同样成立 → 判过，等于没测。
if ! grep -q '启动拉取 bundle' "$WORK/mynetd2.log"; then
  bad "第二次启动的日志里既无「完成」也无「失败」—— 拉取根本没跑（-once 提前退出的竞态复发）"
elif grep -q 'unchanged' "$WORK/mynetd2.log"; then
  ok "第二次启动走条件请求拿到 304（unchanged），未重写文件"
else
  after=$(cksum "$HOME_DIR/var/bundle.json" | awk '{print $1}')
  if [ "$before" = "$after" ]; then
    ok "第二次启动后文件未变（cksum 一致）"
  else
    bad "第二次启动改动了 bundle.json，但日志没说明原因"
  fi
fi

echo
printf 'bundle_pull e2e：%s 通过 / %s 失败\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
