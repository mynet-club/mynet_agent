package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mynet-club/mynet_agent/mynet/utc/endpoint"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func startUTCEndpoint(ctx context.Context) func() {
	noop := func() {}
	path := os.Getenv("MYNET_UTC_CONFIG")
	explicit := path != ""
	if !explicit {
		path = filepath.Join(utils.GetMyNetHome(), "conf", "utc", "utcd.conf")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			logger.Error("UTC endpoint 配置不可读，未启动", "path", path, "err", err)
		}
		return noop
	}
	if !info.Mode().IsRegular() {
		logger.Error("UTC endpoint 配置不是普通文件，未启动", "path", path)
		return noop
	}
	file, err := os.Open(path)
	if err != nil {
		logger.Error("UTC endpoint 配置不可读，未启动", "path", path, "err", err)
		return noop
	}
	if err := file.Close(); err != nil {
		logger.Error("UTC endpoint 配置关闭失败，未启动", "path", path, "err", err)
		return noop
	}
	name := "utcd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(utils.PrefixBinDir(), name)
	childCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := endpoint.Run(childCtx, binary, path, func(format string, args ...any) {
			logger.Info("UTC endpoint: " + fmt.Sprintf(format, args...))
		}); err != nil && childCtx.Err() == nil {
			logger.Error("UTC endpoint 退出", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
