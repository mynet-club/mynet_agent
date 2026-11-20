package endpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// Run supervises only the utcd child it starts, leaving configuration and materials to utcd.
func Run(ctx context.Context, binary, config string, logf func(string, ...any)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if binary == "" || config == "" {
		return errors.New("utcd binary and config must not be empty")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, binary, "--config", config)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		cmd.Cancel = func() error {
			if runtime.GOOS == "windows" {
				return cmd.Process.Kill()
			}
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		cmd.WaitDelay = 10 * time.Second
		if err := cmd.Start(); err != nil {
			logf("utcd start error: %v", err)
		} else {
			logf("utcd started pid=%d", cmd.Process.Pid)
			err := cmd.Wait()
			logf("utcd exited pid=%d err=%v", cmd.Process.Pid, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
