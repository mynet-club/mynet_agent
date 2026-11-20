//go:build linux

package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const endpointHelperEnv = "MYNET_ENDPOINT_TEST_PARENT"

// Run must supply only --config CONFIG, so dispatch before testing parses flags.
// Tests are deliberately serial: only their children inherit this scoped env.
func TestMain(m *testing.M) {
	if os.Getenv(endpointHelperEnv) == strconv.Itoa(os.Getppid()) {
		os.Exit(endpointHelperMain())
	}
	os.Exit(m.Run())
}

type endpointHelperConfig struct {
	ExitFirst bool
	ExitCode  int
	GateTerm  bool
}

type endpointEvent struct {
	Kind string
	PID  int
	PPID int
	Args []string
	At   int64
}

func endpointHelperMain() int {
	// A watchdog bounds even a broken supervisor's orphaned helper lifetime.
	go func() {
		<-time.After(30 * time.Second)
		os.Exit(98)
	}()
	if len(os.Args) != 3 || os.Args[1] != "--config" {
		return 97
	}
	root := filepath.Dir(os.Args[2])
	writeEvent := func(kind string) {
		f, err := os.OpenFile(filepath.Join(root, "events"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(96)
		}
		e := endpointEvent{kind, os.Getpid(), os.Getppid(), os.Args[1:], time.Now().UnixNano()}
		raw, err := json.Marshal(e)
		if err == nil {
			_, err = f.Write(append(raw, '\n'))
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			os.Exit(96)
		}
	}
	var cfg endpointHelperConfig
	raw, err := os.ReadFile(os.Args[2])
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		return 95
	}
	lock, err := os.OpenFile(filepath.Join(root, "alive.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 94
	}
	defer lock.Close()
	// The kernel releases this lock only when this process closes/exits. A
	// second process cannot hide overlap by overwriting a shared PID file.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		writeEvent("overlap")
		return 93
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	writeEvent("start")
	fmt.Fprintln(os.Stdout, "endpoint-helper-stdout")
	fmt.Fprintln(os.Stderr, "endpoint-helper-stderr")
	if cfg.ExitFirst {
		first, err := os.OpenFile(filepath.Join(root, "first"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_ = first.Close()
			writeEvent("exiting")
			// Keep the lock held while exiting, to expose premature restarts.
			<-time.After(150 * time.Millisecond)
			writeEvent("exit")
			return cfg.ExitCode
		}
		if !os.IsExist(err) {
			return 92
		}
	}
	select {
	case <-term:
		writeEvent("term")
	case <-time.After(25 * time.Second):
		return 91
	}
	if cfg.GateTerm {
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
				break
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				return 90
			}
		}
	}
	writeEvent("exit")
	return 0
}

type endpointFixture struct {
	t      *testing.T
	root   string
	config string
	binary string
	owned  map[int]*os.Process
}

func newEndpointFixture(t *testing.T, cfg endpointHelperConfig) *endpointFixture {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &endpointFixture{t, root, filepath.Join(root, "utcd config with spaces.conf"), binary, make(map[int]*os.Process)}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(endpointHelperEnv, strconv.Itoa(os.Getpid()))
	t.Cleanup(func() {
		for _, p := range f.owned {
			// Also clean up a child if a broken Run returned without waiting.
			// FindProcess retains this child's Linux process handle, not a name.
			_ = p.Kill()
			done := make(chan struct{})
			go func(p *os.Process) { _, _ = p.Wait(); close(done) }(p)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Errorf("cleanup wait for owned PID %d timed out", p.Pid)
			}
			_ = p.Release()
		}
	})
	return f
}

func (f *endpointFixture) events() []endpointEvent {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "events"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var events []endpointEvent
	lines := bytes.Split(raw, []byte{'\n'})
	// Ignore only the last, incomplete write while a child is appending.
	for _, line := range lines[:len(lines)-1] {
		var e endpointEvent
		if err := json.Unmarshal(line, &e); err != nil {
			f.t.Fatalf("invalid helper event %q: %v", line, err)
		}
		if e.Kind == "overlap" {
			f.t.Error("supervisor overlapped two live helper processes")
		}
		if e.Kind == "start" && f.owned[e.PID] == nil {
			p, err := os.FindProcess(e.PID)
			if err != nil {
				f.t.Fatal(err)
			}
			f.owned[e.PID] = p
		}
		events = append(events, e)
	}
	return events
}

func (f *endpointFixture) waitEvent(kind string, ordinal int) endpointEvent {
	f.t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		n := 0
		for _, e := range f.events() {
			if e.Kind == kind {
				n++
				if n == ordinal {
					return e
				}
			}
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			f.t.Fatalf("timed out waiting for %s #%d: %+v", kind, ordinal, f.events())
		}
	}
}

type endpointRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // read only after done is closed
}

func (f *endpointFixture) run(alreadyCanceled bool) *endpointRun {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &endpointRun{cancel: cancel, done: make(chan struct{})}
	if alreadyCanceled {
		cancel()
	}
	go func() {
		r.err = Run(ctx, f.binary, f.config, func(string, ...any) {})
		close(r.done)
	}()
	f.t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(15 * time.Second):
			// Only handles belonging to this fixture are ever killed. Never
			// enumerate processes or signal a process name/group.
			for _, p := range f.owned {
				_ = p.Kill()
			}
			select {
			case <-r.done:
			case <-time.After(2 * time.Second):
				f.t.Error("Run still blocked after owned helper cleanup")
			}
		}
	})
	return r
}

func (r *endpointRun) wait(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-r.done:
		// The API permits either graceful nil or the context cancellation.
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Run cancellation returned %v", r.err)
		}
	case <-time.After(timeout):
		t.Fatal("Run did not return within cancellation deadline")
	}
}

func assertEndpointReaped(t *testing.T, pid int) {
	t.Helper()
	var status syscall.WaitStatus
	got, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if !errors.Is(err, syscall.ECHILD) {
		t.Errorf("PID %d not reaped by Run: Wait4 = %d, %v", pid, got, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("PID %d still exists after Run's Wait: %v", pid, err)
	}
}

func (f *endpointFixture) assertStarts(want int) {
	f.t.Helper()
	count := 0
	for _, e := range f.events() {
		if e.Kind == "start" {
			count++
		}
	}
	if count != want {
		f.t.Fatalf("spawn count = %d, want %d", count, want)
	}
}

func TestRunExactArgvAndCancellationWaitsForSIGTERMExit(t *testing.T) {
	f := newEndpointFixture(t, endpointHelperConfig{GateTerm: true})
	output, err := os.Create(filepath.Join(f.root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	previousStderr := os.Stderr
	os.Stderr = output
	t.Cleanup(func() { os.Stderr = previousStderr; _ = output.Close() })
	r := f.run(false)
	start := f.waitEvent("start", 1)
	if !reflect.DeepEqual(start.Args, []string{"--config", f.config}) || start.PPID != os.Getpid() {
		t.Fatalf("not exact exec-owned utcd: %+v", start)
	}
	r.cancel()
	term := f.waitEvent("term", 1)
	if term.PID != start.PID {
		t.Fatalf("SIGTERM delivered to wrong child: %+v", term)
	}
	select {
	case <-r.done:
		t.Fatal("Run returned before its child exited")
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(f.root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	r.wait(t, 3*time.Second)
	assertEndpointReaped(t, start.PID)
	f.assertStarts(1)
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"endpoint-helper-stdout", "endpoint-helper-stderr"} {
		if !strings.Contains(string(raw), marker) {
			t.Errorf("child output not forwarded to os.Stderr: missing %s in %q", marker, raw)
		}
	}
}

func TestRunRestartsAfterWaitWithoutOverlap(t *testing.T) {
	for _, code := range []int{0, 23} {
		t.Run(fmt.Sprintf("exit_%d", code), func(t *testing.T) {
			f := newEndpointFixture(t, endpointHelperConfig{ExitFirst: true, ExitCode: code})
			r := f.run(false)
			first := f.waitEvent("start", 1)
			exit := f.waitEvent("exit", 1)
			second := f.waitEvent("start", 2)
			if first.PID == second.PID || second.PPID != os.Getpid() {
				t.Fatalf("restart did not create a new owned child: %+v / %+v", first, second)
			}
			if elapsed := time.Duration(second.At - exit.At); elapsed < 2*time.Second {
				t.Errorf("restart skipped the post-Wait 2s backoff: %v", elapsed)
			}
			assertEndpointReaped(t, first.PID)
			r.cancel()
			r.wait(t, 3*time.Second)
			assertEndpointReaped(t, second.PID)
			f.assertStarts(2)
		})
	}
}

func TestRunCancelDuringBackoffDoesNotSpawnAgain(t *testing.T) {
	f := newEndpointFixture(t, endpointHelperConfig{ExitFirst: true, ExitCode: 23})
	r := f.run(false)
	first := f.waitEvent("start", 1)
	f.waitEvent("exit", 1)
	// ESRCH (unlike a helper's exit marker) proves Wait reaped the child, so
	// cancellation is specifically in the post-Wait backoff, not in exec.Wait.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if errors.Is(syscall.Kill(first.PID, 0), syscall.ESRCH) {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("first child was not reaped before the backoff deadline")
		}
	}
	r.cancel()
	r.wait(t, 750*time.Millisecond)
	<-time.After(2200 * time.Millisecond)
	f.assertStarts(1)
	assertEndpointReaped(t, first.PID)
}

func TestRunAlreadyCanceledDoesNotSpawn(t *testing.T) {
	f := newEndpointFixture(t, endpointHelperConfig{})
	r := f.run(true)
	r.wait(t, time.Second)
	<-time.After(2200 * time.Millisecond)
	if events := f.events(); len(events) != 0 {
		t.Fatalf("already-canceled Run spawned a helper: %+v", events)
	}
}

func TestRunCancellationDoesNotSignalUnrelatedSameExecutable(t *testing.T) {
	managed := newEndpointFixture(t, endpointHelperConfig{})
	unrelated := newEndpointFixture(t, endpointHelperConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, unrelated.binary, "--config", unrelated.config)
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel() // CommandContext kills only this cmd.Process.
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("unrelated helper cleanup timed out")
		}
	})
	other := unrelated.waitEvent("start", 1)
	if other.PID != cmd.Process.Pid {
		t.Fatal("unrelated helper PID mismatch")
	}
	r := managed.run(false)
	child := managed.waitEvent("start", 1)
	r.cancel()
	r.wait(t, 3*time.Second)
	assertEndpointReaped(t, child.PID)
	select {
	case err := <-done:
		// Put the result back so cleanup remains bounded without a false error.
		done <- err
		t.Fatalf("canceling managed child stopped unrelated helper: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated same executable no longer alive: %v", err)
	}
	for _, e := range unrelated.events() {
		if e.Kind == "term" || e.Kind == "exit" {
			t.Fatalf("unrelated process received managed cancellation: %+v", e)
		}
	}
}
