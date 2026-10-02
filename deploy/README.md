# mynet_gateway 主机部署（V3-3）

gateway 是**服务器侧**组件（不是用户机组件，所以不放 `mynet_desktop`）：
它跑在生产主机 `10.182.236.180` 上，绑回环，由主机 nginx 以 `gw.mynet.club` 对外。

## 装

```bash
# 1) 取二进制。它就在常规产物包里（make dist 的 5 平台 × 3 二进制之一），
#    目录形如 dist/mynet_agent_linux_amd64_<version>/
tar -xzf dist/mynet_agent_linux_amd64_<version>.tgz -C /tmp
install -d -m 755 /opt/mynet/bin
install -m 755 /tmp/mynet_agent_linux_amd64_<version>/mynet_gateway /opt/mynet/bin/

# 2) 用户与配置
useradd --system --no-create-home --shell /usr/sbin/nologin mynet 2>/dev/null || true
install -d -m 700 /etc/mynet
install -m 600 deploy/gateway.env.example /etc/mynet/gateway.env
$EDITOR /etc/mynet/gateway.env        # 填 MYNET_GATEWAY_UPSTREAM

# 3) 单元
install -m 644 deploy/systemd/mynet_gateway.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now mynet_gateway
systemctl status mynet_gateway --no-pager

# 4) 验活（应当返回 {"data":{"conns":0},"success":true}）
curl -sS http://127.0.0.1:8710/healthz
```

nginx 侧见 [`mynet_api/deploy/nginx.conf`](../../../mynet_api/deploy/nginx.conf) 的
`gw.mynet.club` 段 —— 反代到 `127.0.0.1:8710`，且**必须显式写 WS 升级头**
（`proxy_http_version 1.1` + `Upgrade` + `Connection`），漏了会 501/400，
而直接 curl 8710 却正常，极难定位。

## 为什么绑回环

1. gateway **自身不鉴权**（C-3：纯透传 + 连接状态表）。把它暴露到公网等于把节点面
   入口敞开；对外一律走 nginx。
2. 绑回环时**不需要** `-internal-token`；非回环则会被 `main.go` 的 fail-fast 拦住
   （空口令下 `/v1/notify` 完全不校验）。

## ⚠️ 还没做的（别按「已完成」理解）

- **presence 未接 Redis**：`gateway/presence_redis.go` 已实现，但
  `cmd/mynet_gateway/main.go` 里**没有调用点**（`NewRedisPresence` 目前只被测试用），
  所以跑的是内存 presence —— **单实例可用，多实例各看各的**。
  给 unit 配 Redis 目前也不会生效；多实例（`gw1`/`gw2`）要等 V4-5/V4-6。
- **生产安装本身未执行**：需要主机访问权限（闸门 **H8**）。上面的步骤是备好的，
  不是跑过的。
