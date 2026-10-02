package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OpenWrt 执行器：把 mynet CLI 形状的 argv 翻译成 lua -e 脚本，
// 调 luci.model.mynet.* 既有能力（SSOT：桌面 exec mynet / OpenWrt lua -e）。
//
// L6 路由写入仍归 `mynet apply`（route-apply-unification）——Lua 侧
// node.apply_routes / clear_routes 内部会 shell 出去调它，三道闸不抄第二份。

// luaBin 解析 lua 解释器。MYNET_LUA 可覆盖（OpenWrt 上通常是 /usr/bin/lua）。
func luaBin() string {
	if v := os.Getenv("MYNET_LUA"); v != "" {
		return v
	}
	return "lua"
}

// executorKind 当前装配的执行器名（日志 / 诊断用）。
func executorKind() string {
	if os.Getenv("MYNET_EXECUTOR") == "lua" {
		return "lua"
	}
	return "mynet"
}

// selectExecutor 按环境选执行器。
//
// MYNET_EXECUTOR=lua 强制走 lua -e；默认仍是 exec mynet（桌面）。
// OpenWrt 装配（procd / mynetd.init）应设 MYNET_EXECUTOR=lua。
func selectExecutor() func(argv []string) (string, string, error) {
	if os.Getenv("MYNET_EXECUTOR") == "lua" {
		return execLuaStd
	}
	return execMynetStd
}

// execLuaStd 适配 commands.Executor 签名。
func execLuaStd(argv []string) (string, string, error) {
	out, err := execLua(argv)
	if err != nil {
		return "", "", err
	}
	m, _ := out.(map[string]any)
	stdout, _ := m["stdout"].(string)
	stderr, _ := m["stderr"].(string)
	return stdout, stderr, nil
}

// execLua 跑 lua -e 执行一条 mynet 形状的命令。
func execLua(argv []string) (any, error) {
	script, err := planLuaScript(argv)
	if err != nil {
		return map[string]any{"argv": argv, "error": err.Error()}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, luaBin(), "-e", script)
	// 脚本是常量模板，argv 经 JSON 进环境变量——不拼 shell，不进 -e 字符串。
	// 这样 node_id 等参数不会被当成 lua 语法注入。
	payload, jerr := json.Marshal(argv)
	if jerr != nil {
		return map[string]any{"argv": argv, "error": jerr.Error()}, jerr
	}
	cmd.Env = append(os.Environ(), "MYNET_ARGV="+string(payload))
	// OpenWrt/桌面统一口径：MYNET_HOME 决定 system.json / bundle / keys 落盘位置。
	// 不传则 mynet detector 写到 ~/.mynet，apply 又去读 /etc/mynet/var —— 闭环断。
	if os.Getenv("MYNET_HOME") == "" {
		cmd.Env = append(cmd.Env, "MYNET_HOME=/etc/mynet")
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	// ⚠️ 不要把 script 放进响应：localapi 会原样吐给浏览器/调用方，
	// 等于把整个 lua 执行器源码泄露出去。脚本只进日志级别调试需要时再开。
	out := map[string]any{
		"argv":   argv,
		"stdout": stdout.String(),
		"stderr": stderr.String(),
		"bin":    luaBin(),
	}
	if err != nil {
		out["error"] = err.Error()
		return out, err
	}
	return out, nil
}

// planLuaScript 把 mynet CLI argv 翻译成 lua -e 源码。
//
// 映射面与 commands.Plan 产出的 argv 对齐（refresh-* / apply / service / metrics /
// detector）。参数经 MYNET_ARGV JSON 读取，脚本本身是常量——可单测、可审计。
func planLuaScript(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty argv")
	}
	cmd := argv[0]
	// 与 commands.allowedSubcommand / Plan 的子命令面对齐；这里只放
	// Lua 侧有对应实现的项。detector 在 OpenWrt 上暂无等价物，明确报错
	// 而不是假装成功（D1：桩不算实现）。
	switch cmd {
	case "apply", "refresh-routes", "refresh-address", "refresh-config",
		"service", "metrics", "detector", "nodes", "config", "status":
		// ok
	default:
		return "", fmt.Errorf("lua 执行器不支持子命令: %s", cmd)
	}
	return luaRunnerScript, nil
}

// luaRunnerScript 是 lua -e 的常量入口。
//
// 职责：读 MYNET_ARGV（JSON 数组）→ 分派到 luci.model.mynet.* →
// 约定输出：stdout = 人读/JSON 结果，非零退出 = 失败且 stderr 带原因。
//
// 为什么映射放在 Lua 而不是 Go 生成函数调用：OpenWrt 侧函数签名/返回形状
// 以 Lua 模块为准（table 结构、node_id 取值），Go 只保证「子命令面」稳定。
// 这样 Lua 模块内部重构不必改 mynetd。
const luaRunnerScript = `
local argv = {}
do
  local raw = os.getenv("MYNET_ARGV") or "[]"
  local okj, decoded = pcall(function()
    -- 极简 JSON 数组解析：由 Go 侧 json.Marshal 产出，元素全是字符串
    local t = {}
    for s in raw:gmatch('"([^"]*)"') do t[#t+1] = s end
    return t
  end)
  if okj and type(decoded) == "table" then argv = decoded end
end

local function out(s) io.stdout:write(tostring(s or "")) end
local function fail(s)
  io.stderr:write(tostring(s or "lua executor failed"))
  os.exit(1)
end

local function try_require(mod)
  local ok, m = pcall(require, mod)
  if not ok then fail("require " .. mod .. " failed: " .. tostring(m)) end
  return m
end

local cmd = argv[1] or ""
local flags = {}
local pos = {}
local i = 2
while i <= #argv do
  local a = argv[i]
  if a == "--auto" or a == "--clear" or a == "--json" or a == "--dry-run" then
    flags[a:sub(3)] = true
  elseif a == "--node-id" or a == "--vpn-type" or a == "--interface" or a == "--config" then
    flags[a:sub(3)] = argv[i+1] or ""
    i = i + 1
  elseif a:sub(1, 2) == "--" then
    flags[a:sub(3)] = argv[i+1] or ""
    i = i + 1
  else
    pos[#pos+1] = a
  end
  i = i + 1
end

local function nid()
  if flags["node-id"] and flags["node-id"] ~= "" then
    return tonumber(flags["node-id"]) or flags["node-id"]
  end
  return nil
end

if cmd == "apply" or cmd == "refresh-routes" then
  local node = try_require("luci.model.mynet.node")
  if flags["clear"] then
    node.clear_routes(nid())
    out("cleared")
    os.exit(0)
  end
  local r = node.apply_routes(nid())
  if r and r.ok then
    out("applied")
    os.exit(0)
  end
  local errs = (r and r.errors) or {}
  fail(table.concat(errs, "; "))
elseif cmd == "service" then
  -- E-1：进程生命周期归 procd。不再 Lua spawn/kill gnb。
  -- 启停走 /etc/init.d/mynet（由 mynet install 写入的 procd 单元）。
  local action = pos[1] or ""
  local util = try_require("luci.model.mynet.util")
  if action == "start" or action == "stop" or action == "reload" or action == "restart" then
    if action == "restart" then action = "restart"
    elseif action == "reload" then action = "reload" end
    local so, code = util.exec_status("/etc/init.d/mynet " .. action .. " 2>&1")
    so = tostring(so or "")
    if code == 0 then
      out("service " .. action)
      os.exit(0)
    end
    fail(so ~= "" and so or ("init mynet " .. action .. " failed code=" .. tostring(code)))
  else
    fail("unknown service action: " .. tostring(action))
  end
elseif cmd == "metrics" then
  local sys = try_require("luci.model.mynet.system")
  local util = try_require("luci.model.mynet.util")
  local m = sys.collect_metrics(nid())
  out(util.json_encode(m) or "{}")
  os.exit(0)
elseif cmd == "detector" then
  -- detector 产出 system.json（L6 素材，mynet apply 要读）。
  -- 与 apply 同属「必须落到 Go 执行器产物」的能力，这里转调 mynet detector，
  -- 不在 Lua 重写探测解析（AGENTS.md §1.2：重活在执行器）。
  local util = try_require("luci.model.mynet.util")
  local bin = util.mynet_bin()
  if not bin then fail("mynet binary not found (expect /usr/sbin/mynet)") end
  local home = os.getenv("MYNET_HOME") or util.MYNET_HOME or "/etc/mynet"
  local so, code = util.exec_status("MYNET_HOME='" .. home .. "' '" .. bin .. "' detector --json 2>&1")
  so = tostring(so or "")
  if code == 0 then
    out(so)
    os.exit(0)
  end
  fail(so ~= "" and so or ("detector failed code=" .. tostring(code)))
elseif cmd == "refresh-config" or cmd == "refresh-routes" or cmd == "refresh-address" then
  -- 节点面 bundle 已由 mynetd 拉好；这里转调 mynet（L6）读本地 bundle，
  -- 不走 Lua 的用户 JWT（会过期，且节点面本就不该依赖用户会话）。
  local util = try_require("luci.model.mynet.util")
  local node = try_require("luci.model.mynet.node")
  local bin = util.mynet_bin()
  if not bin then fail("mynet binary not found (expect /usr/sbin/mynet)") end
  local home = os.getenv("MYNET_HOME") or util.MYNET_HOME or "/etc/mynet"
  local so, code = util.exec_status("MYNET_HOME='" .. home .. "' '" .. bin .. "' " .. cmd .. " --auto 2>&1")
  so = tostring(so or "")
  if code ~= 0 then
    fail(so ~= "" and so or (cmd .. " failed code=" .. tostring(code)))
  end
  -- driver/gnb/route.conf 已更新；同步生成 OS 级 /etc/mynet/conf/route.conf
  -- （route.mynet / proxy 读这份）。此前链路断在这里：188 进了 driver 却不进 conf。
  if cmd == "refresh-config" and node.generate_route_conf then
    local _, rerr = node.generate_route_conf(nid())
    if rerr then
      so = so .. "\nroute.conf: " .. tostring(rerr)
    end
  end
  out(so)
  os.exit(0)
elseif cmd == "nodes" then
  -- 节点列表：本地 route/gnb 视图（对齐 LuCI Node List）
  local node = try_require("luci.model.mynet.node")
  local util = try_require("luci.model.mynet.util")
  local list = node.get_nodes and node.get_nodes() or nil
  if list then
    out(util.json_encode(list) or "[]")
    os.exit(0)
  end
  -- 兜底：从 route.conf 抽节点
  local cfg = try_require("luci.model.mynet.config")
  local util2 = try_require("luci.model.mynet.util")
  local nid = cfg.get_node_id()
  out(util2.json_encode({ node_id = nid }) or "{}")
  os.exit(0)
elseif cmd == "status" or cmd == "config" then
  local node = try_require("luci.model.mynet.node")
  local util = try_require("luci.model.mynet.util")
  local cfg = try_require("luci.model.mynet.config")
  local running = node.gnb_is_running and node.gnb_is_running(nil)
  out(util.json_encode({
    node_id = cfg.get_node_id(),
    running = running and true or false,
    vpn_type = cfg.get_vpn_type and cfg.get_vpn_type() or "gnb",
    interface = cfg.get_vpn_interface and cfg.get_vpn_interface() or "",
  }) or "{}")
  os.exit(0)
else
  fail("unhandled cmd: " .. tostring(cmd))
end
`
