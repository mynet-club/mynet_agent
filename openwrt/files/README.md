Place GOOS=linux binaries here (E-5 构建产物, from `mynet_agent` `make dist`):

- `files/mynetd` — 纯通信层（必填；缺失则 mynet-agent 拒绝出包）
- `files/mynet`  — L6 执行器，路由唯一写入者 `mynet apply`（必填；Lua `apply_routes` 会调它）

来源：`mynet_agent` 的 `make dist` 产物（`GOOS=linux`）。
⚠️ 注意 Go 的 `mipsle` 在 OpenWrt/ctl 侧叫 `mipsel`。
