package material

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// utcdConfig configures the local utcd owned by this agent. When the binary is
// empty the agent only installs material and leaves the data plane to the
// operator (the pre-A1 behaviour).
type utcdConfig struct {
	Binary   string `json:"utcd_binary"`
	Config   string `json:"utcd_config"`
	Template string `json:"utcd_template"`
	Log      string `json:"utcd_log"`
}

// supervisor owns exactly one utcd process started from the configured binary.
// It never touches a process whose executable differs, and it restarts the
// process in place (SIGTERM -> wait -> SIGKILL) only when the trust face
// changed.
type supervisor struct {
	cfg  utcdConfig
	logf func(string, ...any)
	cmd  *exec.Cmd
}

func newSupervisor(cfg utcdConfig, logf func(string, ...any)) (*supervisor, error) {
	if cfg.Binary == "" {
		return nil, nil
	}
	if cfg.Config == "" {
		return nil, errors.New(LogPrefix + "utcd_binary requires utcd_config")
	}
	return &supervisor{cfg: cfg, logf: logf}, nil
}

// render writes the utcd configuration from the template, substituting the
// material paths. Without a template the configured file is left untouched.
func (s *supervisor) render(paths map[string]string) error {
	if s == nil || s.cfg.Template == "" {
		return nil
	}
	raw, err := os.ReadFile(s.cfg.Template)
	if err != nil {
		return err
	}
	out := string(raw)
	for k, v := range paths {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	if bytesEqualFile(s.cfg.Config, []byte(out)) {
		return nil
	}
	return writeAtomic(s.cfg.Config, []byte(out), 0600)
}

func bytesEqualFile(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	return err == nil && string(got) == string(want)
}

func (s *supervisor) pids() []int {
	target, err := filepath.Abs(s.cfg.Binary)
	if err != nil {
		return nil
	}
	var pids []int
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
			if err != nil {
				continue
			}
			if exe == target || exe == target+" (deleted)" {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	base := filepath.Base(target)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || filepath.Base(fields[1]) != base {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func alive(pid int) bool {
	if _, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return true
	}
	return syscall.Kill(pid, 0) == nil
}

// stop terminates every matching utcd and waits up to 10s before SIGKILL.
func (s *supervisor) stop() {
	pids := s.pids()
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_, _ = s.cmd.Process.Wait()
	}
	s.cmd = nil
}

// ensure starts utcd when no instance is running. A running instance is left
// alone: pools and leases are hot-applied by utcd itself.
func (s *supervisor) ensure() error {
	if s == nil {
		return nil
	}
	if len(s.pids()) > 0 {
		return nil
	}
	return s.start()
}

func (s *supervisor) restart() error {
	if s == nil {
		return nil
	}
	s.stop()
	return s.start()
}

func (s *supervisor) start() error {
	argv := []string{s.cfg.Binary, "-c", s.cfg.Config}
	cmd := exec.Command(argv[0], argv[1:]...)
	if s.cfg.Log != "" {
		f, err := os.OpenFile(s.cfg.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("utcd start: %w", err)
	}
	s.cmd = cmd
	time.Sleep(500 * time.Millisecond)
	if len(s.pids()) == 0 {
		return errors.New("utcd exited immediately after start")
	}
	s.logf("utcd started pid=%d", cmd.Process.Pid)
	go func(c *exec.Cmd) {
		if err := c.Wait(); err != nil {
			s.logf("utcd exited: %v", err)
		}
	}(cmd)
	return nil
}
