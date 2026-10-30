package main

import (
	"context"
	"time"
)

// retryNodeFace 节点面装配重试：mynetd 先于 mynet node claim 启动时，
// 身份稍后才出现 —— 一次性放弃会让 transport 永久死（2026-10-30 L3 实测）。
// attempt 成功即返回；ctx 取消即放弃。
func retryNodeFace(ctx context.Context, every time.Duration, attempt func() bool, logf func(string, ...any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if attempt() {
			if logf != nil {
				logf("节点面身份已就绪，transport 已接入")
			}
			return
		}
	}
}
