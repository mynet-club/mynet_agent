//go:build !windows

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGnb 用一个假 gnb + 假进程表，复现「重叠的 Stop/Start 在同一 confdir 留下两个 gnb」。
//
// 为什么要假：真 gnb 会双 fork 自我 daemon 化、写自己的 pid 文件、抢真实 tun ——
// 单测里跑不起来。这里只保真**关键行为**：
//   - Spawn 后**不是立刻**写 pid 文件（真 gnb 要 fork 两次才写）→ 这就是竞态窗口：
//     先到的 Spawn 还没写 pid，后到的 Stop/IsProcessAlive 就看不见它；
//   - 同 confdir 已经有人持有时，新起的**照样活着**（真 gnb 抢不到 tun 只是变 tunless，
//     不退出）—— 这正是「一持 tun、一 tunless」两个进程的来源；
//   - 进程表用 `kill -0` 判活、用 `ps -Ao pid=,args=` 列命令行（与实现同口径）。
type fakeGnb struct {
	mu      sync.Mutex
	nextPID int
	alive   map[int]bool
	cmdline map[int]string
	spawns  int
	// spawnDelay 模拟 daemon 化的时间差（越接近真实，竞态越容易现形）。
	spawnDelay time.Duration
	// concurrentSpawns 记录「同一时刻有几次 Spawn 在途」，用来证明锁真的串行化了。
	inSpawn    int
	maxInSpawn int
	// busyboxPS 为真时让 `ps` 报「不认 -A」（OpenWrt busybox 的实测形态：`.182` 上
	// `ps: unrecognized option: A`），逼出 pidof + /proc 的回落路径。
	busyboxPS bool
}

func newFakeGnb() *fakeGnb {
	return &fakeGnb{nextPID: 1000, alive: map[int]bool{}, cmdline: map[int]string{}, spawnDelay: 30 * time.Millisecond}
}

func (f *fakeGnb) exec(name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case name == "systemctl":
		// LoadState=not-found ⇒ systemdManages()=false ⇒ 走 spawn 路径（正是生产 .180 的形态）
		return []byte("not-found"), nil
	case name == "ps":
		if f.busyboxPS {
			return nil, fmt.Errorf("ps: unrecognized option: A")
		}
		var pids []int
		for pid := range f.alive {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		var b strings.Builder
		for _, pid := range pids {
			fmt.Fprintf(&b, "%d %s\n", pid, f.cmdline[pid])
		}
		return []byte(b.String()), nil
	case name == "pidof":
		// busybox 回落路径：pidof <exe> → 名字匹配；命令行首段 basename 即进程名
		name0 := ""
		if len(args) > 0 {
			name0 = args[0]
		}
		var pids []int
		for pid := range f.alive {
			if filepath.Base(strings.Fields(f.cmdline[pid])[0]) == name0 {
				pids = append(pids, pid)
			}
		}
		sort.Ints(pids)
		var parts []string
		for _, pid := range pids {
			parts = append(parts, strconv.Itoa(pid))
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("pidof: no matching process")
		}
		return []byte(strings.Join(parts, " ")), nil
	case name == "cat" && len(args) == 1 && strings.HasPrefix(args[0], "/proc/"):
		pid, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(args[0], "/proc/"), "/cmdline"))
		if cmd, ok := f.cmdline[pid]; ok {
			return []byte(strings.ReplaceAll(cmd, " ", "\x00")), nil
		}
		return nil, fmt.Errorf("cat: no such file")
	case name == "kill" && len(args) == 2 && args[0] == "-0":
		pid, _ := strconv.Atoi(args[1])
		if f.alive[pid] {
			return nil, nil
		}
		return nil, fmt.Errorf("no such process")
	case name == "kill" && len(args) == 2 && (args[0] == "-TERM" || args[0] == "-KILL"):
		pid, _ := strconv.Atoi(args[1])
		delete(f.alive, pid)
		delete(f.cmdline, pid)
		return nil, nil
	}
	return nil, nil
}

func (f *fakeGnb) spawn(name string, args ...string) error {
	f.mu.Lock()
	f.inSpawn++
	if f.inSpawn > f.maxInSpawn {
		f.maxInSpawn = f.inSpawn
	}
	f.spawns++
	f.mu.Unlock()
	// 关键：daemon 化期间 pid 文件还没写 —— 别的动作此时看不见这个实例。
	time.Sleep(f.spawnDelay)
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() { f.inSpawn-- }()
	f.nextPID++
	pid := f.nextPID
	f.alive[pid] = true
	f.cmdline[pid] = name + " " + strings.Join(args, " ")
	// 真 gnb 会自己写 <conf>/gnb.pid（后到的会覆盖先到的 → pid 文件可能指向 tunless 那个）
	conf := ""
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			conf = args[i+1]
		}
	}
	if conf != "" {
		_ = os.WriteFile(filepath.Join(conf, "gnb.pid"), []byte(strconv.Itoa(pid)), 0o600)
	}
	return nil
}

func (f *fakeGnb) liveCount(conf string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for pid := range f.alive {
		if strings.Contains(f.cmdline[pid], conf) {
			n++
		}
	}
	return n
}

func (f *fakeGnb) livePids(conf string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for pid := range f.alive {
		if strings.Contains(f.cmdline[pid], conf) {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

func (f *fakeGnb) driver(conf string) *GNBDriver {
	return &GNBDriver{
		NodeID:          1,
		ConfDir:         conf,
		Binary:          "gnb",
		Exec:            f.exec,
		Spawn:           f.spawn,
		StartProbes:     40,
		StartProbeDelay: 5 * time.Millisecond,
	}
}

// 复现/回归：**并发**的 Reload（等价于 bundle_changed 事件链在启动瞬间连跑）不得在
// 同一 confdir 留下多于一个 gnb。锁之前这里会剩 2–3 个。
func TestConcurrentReloadLeavesExactlyOneGnb(t *testing.T) {
	conf := t.TempDir()
	f := newFakeGnb()
	d := f.driver(conf)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Reload() }()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	if n := f.liveCount(conf); n != 1 {
		t.Fatalf("并发 Reload 后同 confdir 应恰 1 个 gnb，实际 %d（pid=%v）", n, f.livePids(conf))
	}
	if f.maxInSpawn > 1 {
		t.Fatalf("生命周期动作未被串行化：同时有 %d 次 Spawn 在途", f.maxInSpawn)
	}
}

// 收敛：启动前把「命令行指向本 confdir 的其它 gnb」收掉（OpenWrt init 那条 converge 的
// Go 侧等价物，跨平台生效）。
func TestStartConvergesStaleSameConfdir(t *testing.T) {
	conf := t.TempDir()
	f := newFakeGnb()
	// 预置一个「孤儿」：活着、命令行指向本 confdir，但不在 pid 文件里
	f.mu.Lock()
	f.alive[4242] = true
	f.cmdline[4242] = "gnb -c " + conf
	f.mu.Unlock()

	d := f.driver(conf)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if n := f.liveCount(conf); n != 1 {
		t.Fatalf("Start 后应收敛为 1 个，实际 %d（pid=%v）", n, f.livePids(conf))
	}
	f.mu.Lock()
	orphanAlive := f.alive[4242]
	f.mu.Unlock()
	if orphanAlive {
		t.Fatal("孤儿 4242 应已被收敛掉")
	}
}

// Stop 也要收残留：只认 pid 文件会漏掉「pid 文件指错 / 被覆盖」的那个。
func TestStopReapsLeftoverSameConfdir(t *testing.T) {
	conf := t.TempDir()
	f := newFakeGnb()
	d := f.driver(conf)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 再塞一个孤儿（模拟 pid 文件被后到者覆盖后的残留）
	f.mu.Lock()
	f.alive[7777] = true
	f.cmdline[7777] = "gnb -c " + conf
	f.mu.Unlock()

	if err := d.Stop(); err != nil && f.liveCount(conf) > 0 {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := f.liveCount(conf); n != 0 {
		t.Fatalf("Stop 后同 confdir 应 0 个，实际 %d（pid=%v）", n, f.livePids(conf))
	}
}

// 同机多 confdir 互不干扰（多 zone 共存是设计支持的）。
func TestConvergeIgnoresOtherConfdir(t *testing.T) {
	confA, confB := t.TempDir(), t.TempDir()
	f := newFakeGnb()
	d := f.driver(confA)

	f.mu.Lock()
	f.alive[5555] = true
	f.cmdline[5555] = "gnb -c " + confB // 另一个节点
	f.mu.Unlock()

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	f.mu.Lock()
	otherAlive := f.alive[5555]
	f.mu.Unlock()
	if !otherAlive {
		t.Fatal("另一 confdir 的 gnb 不该被收敛（多节点共存）")
	}
	if n := f.liveCount(confA); n != 1 {
		t.Fatalf("本 confdir 应 1 个，实际 %d", n)
	}
}

// busybox（OpenWrt）的 ps 不认 -A/-o → 必须回落到 pidof + /proc 口径，否则收敛在
// OpenWrt 上静默失效（2026-10-11 `.182` 实测：`ps: unrecognized option: A`）。
func TestConvergeFallsBackWhenPsUnsupported(t *testing.T) {
	conf := t.TempDir()
	f := newFakeGnb()
	f.busyboxPS = true
	f.mu.Lock()
	f.alive[4242] = true
	f.cmdline[4242] = "gnb -c " + conf // 本 confdir 的孤儿
	f.alive[1] = true
	f.cmdline[1] = "gnb -c /some/other/conf" // 别的节点
	f.mu.Unlock()

	d := f.driver(conf)
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	f.mu.Lock()
	orphan, other := f.alive[4242], f.alive[1]
	f.mu.Unlock()
	if orphan {
		t.Fatal("ps 不可用时仍须收敛同 confdir 孤儿（pidof+/proc 回落没生效）")
	}
	if !other {
		t.Fatal("别的 confdir 不该被收")
	}
	if n := f.liveCount(conf); n != 1 {
		t.Fatalf("应恰 1 个，实际 %d", n)
	}
}
