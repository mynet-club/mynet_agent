// Package mynet 是执行器（偶发、用完即抛）。
//
// 职责（AGENTS.md §1.2）：detector · metrics · apply · service · driver · setup/install/…
// 不含：mynetd 通信层的职责（心跳/WS/指令分派）。
//
// 子命令面见 commands.go。
package mynet

// Version 由构建注入
var Version = "dev"
