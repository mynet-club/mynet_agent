package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/mynet/utc/hubd"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// startUTCHub runs the resident Hub pair runtime inside mynetd. A long-lived
// renewal loop cannot live behind the 60s executor command timeout, and a
// separate daemon would duplicate lifecycle/identity ownership.
func startUTCHub(ctx context.Context) {
	path := os.Getenv("MYNET_UTC_HUB_CONFIG")
	if path == "" {
		path = filepath.Join(utils.GetMyNetHome(), "conf", "utc", "hub.json")
	}
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			logger.Error("UTC Hub 配置不可读，运行时未启动", "path", path, "err", err)
		}
		return
	}
	config, err := hubd.Load(path)
	if err != nil {
		logger.Error("UTC Hub 配置无效，运行时未启动（fail-closed）", "path", path, "err", err)
		return
	}
	runtime, err := hubd.New(config)
	if err != nil {
		logger.Error("UTC Hub 运行时初始化失败", "path", path, "err", err)
		return
	}
	runtime.Logf = func(format string, args ...any) {
		logger.Info("UTC Hub: " + fmt.Sprintf(format, args...))
	}
	logger.Info("UTC Hub 运行时启动", "pairs", len(config.Pairs),
		"conf_dir", config.ConfDir)
	go func() {
		if err := runtime.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("UTC Hub 运行时退出", "err", err)
		}
	}()
}
