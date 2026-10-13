package main

import (
	"context"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
	"github.com/mynet-club/mynet_agent/shared/logger"
)

// localResultReporter 把 /api/v1/command-result（Lua / 本地执行器回报）转成
// 节点面 Result，经 WS 上报后端（E-4）。
//
// 为什么必须接到 report：handleCommandResult 只负责收 HTTP，真正上行要签名 + 走
// transport。此前 localapi.OnResult 从未装配，Lua 执行结果进了 mynetd 就被丢掉——
// 表现为「命令执行了，但后端上一直停在执行中」。
//
// report 可为 nil（未配 MYNET_GATEWAYS 或节点面密钥未就绪）：此时明确记日志，
// 不静默丢弃——静默丢弃的症状是「本地明明成功，平台永远无回执」。
func localResultReporter(report func(context.Context, commands.Result) error) func(id string, ok bool, message, source string) {
	return func(id string, ok bool, message, source string) {
		if report == nil {
			logger.Warn("command-result 收到但无上报通道（未配 MYNET_GATEWAYS 或节点面密钥未就绪）",
				"id", id, "ok", ok, "source", source)
			return
		}
		res := commands.Result{ID: id, OK: ok}
		if ok {
			res.Stdout = message
		} else {
			res.Error = message
		}
		// source 进 stderr：后端的 result 里 stdout/stderr 是结构化回执，
		// 执行器来源（mynet / lua）放这里便于对账，不污染给人看的 message。
		if source != "" {
			res.Stderr = "executor=" + source
		}
		if err := report(context.Background(), res); err != nil {
			logger.Warn("command-result 上报失败", "id", id, "err", err)
			return
		}
		logger.Debug("command-result 已上报", "id", id, "ok", ok, "source", source)
	}
}
