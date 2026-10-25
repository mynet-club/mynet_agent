package mynet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func hookDNSInterruptedFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-interrupted.json")
}
func hookDNSReadyFile() string { return filepath.Join(varDir(), "proxy", "dns-hook-ready.pid") }
func hookDNSInterrupted() bool { _, err := os.Stat(hookDNSInterruptedFile()); return err == nil }
func clearHookDNSInterruption() error {
	err := os.Remove(hookDNSInterruptedFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func recordHookDNSInterruption() error {
	raw, _ := json.Marshal(map[string]string{"reason": "system_dns_changed", "at": time.Now().UTC().Format(time.RFC3339)})
	p := hookDNSInterruptedFile()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// 只读核对当前配置，不把备份中的 applied 当成当前事实。
func hookDNSOwned(b *DNSBackup, ops systemDNSOps, effective func() ([]string, error)) (bool, error) {
	if b == nil || !b.Applied || b.Target != "127.0.0.1" {
		return false, nil
	}
	if raw := b.Extra["resolver_file"]; raw != "" {
		var state resolverFileBackup
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return false, err
		}
		info, err := os.Lstat(state.Path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		data, err := os.ReadFile(state.Path)
		if err != nil {
			return false, err
		}
		return info.Mode()&os.ModeSymlink == 0 && bytes.Equal(data, state.Applied), nil
	}
	services, err := ops.list()
	if err != nil {
		return false, err
	}
	if len(services) == 0 {
		return false, fmt.Errorf("no network services available")
	}
	for _, svc := range services {
		current, _, err := ops.read(svc)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(current, strings.Split(b.Target, ",")) {
			return false, nil
		}
	}
	if effective != nil {
		current, err := effective()
		if err != nil {
			return false, err
		}
		if len(current) > 0 && !reflect.DeepEqual(current, []string{b.Target}) {
			return false, nil
		}
	}
	return true, nil
}

func monitorHookDNS(ctxDone <-chan struct{}, events <-chan struct{}, check func() (bool, error), interrupt func() error, stop func()) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-events:
		case <-tick.C:
		}
		owned, err := check()
		if err != nil {
			hookLogf("DNS ownership check: %v", err)
			continue
		}
		if owned {
			continue
		}
		if err := interrupt(); err != nil {
			hookLogf("DNS interruption record: %v", err)
			continue
		}
		hookLogf("system DNS changed externally; acceleration interrupted, automatic takeover suspended")
		stop()
		return
	}
}

func hookDNSMonitorReady() bool {
	raw, err := os.ReadFile(hookDNSReadyFile())
	if err != nil || strings.TrimSpace(string(raw)) != fmt.Sprint(os.Getpid()) {
		return false
	}
	info, err := os.Stat(hookDNSReadyFile())
	return err == nil && time.Since(info.ModTime()) >= time.Second
}

func hookDNSResumeAllowed() error {
	if hookDNSInterrupted() {
		return errors.New("系统 DNS 已被外部更改；请明确重新开启加速后再接管")
	}
	return nil
}

func hookDNSObservationFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-ownership.json")
}
func saveHookDNSObservation(owned bool, err error) {
	state := "owned"
	if err != nil {
		state = "unknown"
	} else if !owned {
		state = "overridden"
	}
	raw, _ := json.Marshal(struct {
		PID   int
		State string
		At    time.Time
	}{os.Getpid(), state, time.Now()})
	p := hookDNSObservationFile()
	if os.WriteFile(p+".tmp", raw, 0600) == nil {
		_ = os.Rename(p+".tmp", p)
	}
}
func hookDNSObservation(pid int) string {
	raw, err := os.ReadFile(hookDNSObservationFile())
	if err != nil {
		return ""
	}
	var state struct {
		PID   int
		State string
		At    time.Time
	}
	if json.Unmarshal(raw, &state) != nil || state.PID != pid {
		return ""
	}
	if time.Since(state.At) > 15*time.Second {
		return "unknown"
	}
	return state.State
}
