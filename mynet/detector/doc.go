// Package detector 是系统能力探测的**唯一**实现（F-1）。
//
// # 收敛说明
//
// 旧 mynet_agent 有两套重复的「读」实现：
//
//	internal/detector/*          接口化 + 缓存 + 工厂（约 3000 行）
//	internal/tools/{firewall,routing}  面向 CLI 展示的另一套解析（约 1990 行）
//
// 两套全是 Detect/Get/parse，无一 Apply/Write（实测 2026-09-23）。
// 收敛决策：**保留 detector 接口与实现**，tools/{firewall,routing} **不搬**。
// tools 独有的平台覆盖（OpenWrt zone 细节、Darwin 路由）按需并入本包
// （见 routing_darwin.go / firewall_openwrt.go），其余展示格式化逻辑不搬。
//
// 写入能力不在此包——归 mynet apply（F-2，唯一写入者）。
package detector
