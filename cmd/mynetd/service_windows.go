//go:build windows

package main

// Windows 服务宿主（mynetd）—— 用 SCM 托管，替代计划任务。
//
// 为什么不用计划任务：服务可依赖、可恢复、`sc query` 可见状态；
// 计划任务在 SSH Job 退出后行为不定（183 实测进程会被收走）。
//
// 环境变量：服务进程不读用户环境。启动时从 `%MYNET_HOME%\mynetd.env`
// 注入 MYNET_HOME / MYNET_GATEWAYS / MYNET_BIN（缺省 HOME 为
// `C:\ProgramData\MyNet`，见 utils.PlatformDefaultHome）。
// gnb 不单独注册服务（gnb.exe 不是 SCM 程序）——由 mynetd 的
// auto_restore_tunnel 在服务启动后拉起（tunnel_restore.go）。

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
	"golang.org/x/sys/windows/svc"
)

// tryRunAsWindowsService 若以 Windows 服务方式启动则接管并返回 true。
func tryRunAsWindowsService() bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false
	}
	loadServiceEnv()
	svc.Run("mynetd", &winService{})

	return true
}

// winService 实现 svc.Handler：StartPending → Running → StopPending。
type winService struct{}

func (m *winService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgent(ctx)
	}()

	s <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				s <- svc.Status{State: svc.StopPending}
				return false, 0
			case svc.Interrogate:
				s <- c.CurrentStatus
			}
		case <-done:
			// runAgent 自己退出（配置错等）——如实报非零
			return false, 1
		}
	}
}

// loadServiceEnv 读 mynetd.env 注入进程环境（服务没有用户 env）。
func loadServiceEnv() {
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		// 与 utils.PlatformDefaultHome 一致（home 是 ProgramData，不是程序目录）
		home = utils.PlatformDefaultHome()
		_ = os.Setenv("MYNET_HOME", home)
	}
	for _, p := range []string{
		filepath.Join(home, "mynetd.env"),
		filepath.Join(filepath.Dir(exeDir()), "mynetd.env"),
	} {
		if applyEnvFile(p) {
			return
		}
	}
}

func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return ""
}

// applyEnvFile 把 KEY=VALUE 行设进当前进程环境（已有的不覆盖）。
func applyEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
	return true
}
