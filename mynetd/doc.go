// Package mynetd 是 MyNet 的**纯通信层**（常驻）。
//
// 只做（AGENTS.md §1.2）：
//
//	心跳组装 · WS + 身份密钥 · 取指令/结果上报 · 读 system.json · 命令分派
//
// 不做系统能力：探测解析、OS 路由/防火墙写入、驱动配置生成、进程生命周期、
// 驱动状态解析、主机指标采集。这些全在执行器 mynet（见 mynet/doc.go）。
//
// 构建开关（D-11）：
//
//	go build -tags embed ./cmd/mynetd   // 桌面：embed WebUI
//	go build ./cmd/mynetd               // OpenWrt：无 embed（LuCI 托管）
//
// 不再需要 thin/noembed 标签裁业务——mynetd 本身就没有重活。
package mynetd

// DefaultLocalAPI 端口 8096（8192 = IANA spytechphone + mynet_utc 占用）
const DefaultLocalAPI = "127.0.0.1:8096"
