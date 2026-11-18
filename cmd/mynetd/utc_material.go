package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/mynet/utc/material"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// startUTCMaterial runs the utcd material agent inside mynetd. It renders the
// utcd configuration from the delivered material and owns utcd's lifecycle, so
// leases, trust and token pools no longer require operator scripts or manual
// restarts. It starts only when conf/utc/material.json exists.
func startUTCMaterial(ctx context.Context) {
	path := os.Getenv("MYNET_UTC_MATERIAL_CONFIG")
	if path == "" {
		path = filepath.Join(utils.GetMyNetHome(), "conf", "utc", "material.json")
	}
	if _, err := os.Lstat(path); err != nil {
		if !os.IsNotExist(err) {
			logger.Error("UTC 材料配置不可读，材料代理未启动", "path", path, "err", err)
		}
		return
	}
	cfg, err := material.LoadConfig(path)
	if err != nil {
		logger.Error("UTC 材料配置无效，材料代理未启动（fail-closed）", "path", path, "err", err)
		return
	}
	agent, err := material.New(cfg)
	if err != nil {
		logger.Error("UTC 材料代理初始化失败", "path", path, "err", err)
		return
	}
	agent.SetLogger(func(format string, args ...any) {
		logger.Info("UTC Material: " + fmt.Sprintf(format, args...))
	})
	logger.Info("UTC 材料代理启动", "url", cfg.URL, "dir", cfg.Dir,
		"utcd", cfg.UTCD.Binary != "")
	go func() {
		if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("UTC 材料代理退出", "err", err)
		}
	}()
}
