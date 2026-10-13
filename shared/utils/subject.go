package utils

// 主体定位器（Subject Locator）—— 解决「脚本/执行器找不到真正的主主体」。
//
// 规则 R1–R6 见 knowledge-base/spec/paths.md §4。
// 证据：knowledge-base/evidence/20260930-subject/INDEX.md
//
//	R1 进程锚优先：cmdline -c 与 exe 是唯一真相
//	R2 gnb_ctl 必须与 gnb 同目录，缺则 tools_missing，禁止捡异构建
//	R3 多 home 列 candidates[] + chosen/why
//	R4 --systemd 时 map 需探测（conf 目录 vs /tmp/gnb.<port>.map）
//	R5 工具缺失显式，不静默
//	R6 Windows 认 .exe

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Subject 一个 gnb 运行主体的完整定位结果。
type Subject struct {
	NodeID    string `json:"node_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
	GnbBin    string `json:"gnb_bin"`
	ConfDir   string `json:"conf_dir"`
	MapPath   string `json:"map_path,omitempty"`
	GnbCtl    string `json:"gnb_ctl,omitempty"`
	GnbEs     string `json:"gnb_es,omitempty"`
	MyNetHome string `json:"mynet_home,omitempty"`
	// Source: process | fallback
	Source string `json:"source"`
	Why    string `json:"why"`
	// Systemd cmdline 带 --systemd（map 约定可能变）
	Systemd bool `json:"systemd,omitempty"`
	// Candidates 其它 conf 树/进程（R3）
	Candidates []Candidate `json:"candidates,omitempty"`
	// ToolsMissing 缺失工具名（gnb_ctl 等）。非空 = 套件不完整（R2/R5）
	ToolsMissing []string `json:"tools_missing,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Candidate 候选主体（残留树 / 别的进程）。
type Candidate struct {
	PID     int    `json:"pid,omitempty"`
	GnbBin  string `json:"gnb_bin,omitempty"`
	ConfDir string `json:"conf_dir,omitempty"`
	Note    string `json:"note,omitempty"`
}

// GnbProc 扫到的 gnb 进程。
type GnbProc struct {
	PID     int
	Exe     string
	ConfDir string
	Systemd bool
	Index   bool // -P index 角色，不是节点主体
	RawCmd  string
}

// ResolveOpts 解析输入。
type ResolveOpts struct {
	PID     int
	NodeID  string
	ConfDir string
	Home    string // 覆盖 MYNET_HOME
}

// ── 进程扫描 ─────────────────────────────────────────────────

// ScanGnbProcs 列出本机 gnb 节点相关进程。
// 可注入（测试打桩）：scanGnbProcsFunc。
var scanGnbProcsFunc = ScanGnbProcs

// ScanGnbProcs 列出本机 gnb 节点相关进程。
func ScanGnbProcs() ([]GnbProc, error) {
	if runtime.GOOS == "windows" {
		return scanGnbProcsWindows()
	}
	return scanGnbProcsUnix()
}

func scanGnbProcsUnix() ([]GnbProc, error) {
	out, err := exec.Command("ps", "-ax", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps 扫 gnb 进程: %w", err)
	}
	var procs []GnbProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		cmd := fields[1]
		if !looksLikeGnbCmd(cmd) {
			continue
		}
		p := GnbProc{PID: pid, RawCmd: cmd}
		p.Exe, p.ConfDir, p.Systemd, p.Index = parseGnbCmd(cmd)
		if link, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			p.Exe = link
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func looksLikeGnbCmd(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	base = strings.TrimSuffix(base, ".exe")
	if base != "gnb" {
		return false
	}
	// 排除取证/包装脚本
	if strings.Contains(cmd, " SUBJECT SNAPSHOT") || strings.Contains(cmd, "grep") {
		return false
	}
	return true
}

// parseGnbCmd 抽 exe / -c conf / --systemd / index。
func parseGnbCmd(cmd string) (exe, confDir string, systemd, index bool) {
	fields := strings.Fields(cmd)
	if len(fields) > 0 {
		exe = fields[0]
	}
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "-c":
			if i+1 < len(fields) {
				confDir = fields[i+1]
			}
		case "--systemd":
			systemd = true
		case "-P":
			index = true
		}
	}
	return
}

func scanGnbProcsWindows() ([]GnbProc, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'gnb.exe' } | ForEach-Object { "$($_.ProcessId)|$($_.CommandLine)" }`).Output()
	if err != nil {
		return nil, fmt.Errorf("powershell 扫 gnb: %w", err)
	}
	var procs []GnbProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		cmd := parts[1]
		exe, confDir, systemd, index := parseGnbCmd(cmd)
		procs = append(procs, GnbProc{PID: pid, Exe: exe, ConfDir: confDir, Systemd: systemd, Index: index, RawCmd: cmd})
	}
	return procs, nil
}

// ── 主体解析 ─────────────────────────────────────────────────

// ResolveSubject 解析主主体。进程锚优先（R1）。
func ResolveSubject(opts ResolveOpts) (*Subject, error) {
	procs, scanErr := scanGnbProcsFunc()

	if opts.PID > 0 {
		for _, p := range procs {
			if p.PID == opts.PID {
				return finalize(p, procs, opts)
			}
		}
		return nil, fmt.Errorf("subject: pid %d 不是 gnb 进程", opts.PID)
	}

	cands := procs
	if opts.NodeID != "" {
		var hit []GnbProc
		for _, p := range procs {
			if strings.Contains(p.ConfDir, opts.NodeID) {
				hit = append(hit, p)
			}
		}
		cands = hit
	}
	if opts.ConfDir != "" {
		var hit []GnbProc
		want := filepath.Clean(opts.ConfDir)
		for _, p := range procs {
			if filepath.Clean(p.ConfDir) == want {
				hit = append(hit, p)
			}
		}
		cands = hit
	}

	var nodes []GnbProc
	for _, p := range cands {
		if !p.Index {
			nodes = append(nodes, p)
		}
	}

	if len(nodes) == 1 {
		return finalize(nodes[0], procs, opts)
	}
	if len(nodes) > 1 {
		if chosen := pickByMode(nodes, opts); chosen != nil {
			return finalize(*chosen, procs, opts)
		}
		s := &Subject{Source: "process", Why: "多个 gnb 节点进程并存，需 --pid 或 --conf 指定"}
		for _, p := range nodes {
			s.Candidates = append(s.Candidates, Candidate{PID: p.PID, GnbBin: p.Exe, ConfDir: p.ConfDir, Note: "multi"})
		}
		return s, fmt.Errorf("subject: 发现 %d 个 gnb 节点进程，请用 --pid/--conf 指定", len(nodes))
	}

	return fallbackNoProcess(procs, opts, scanErr)
}

func finalize(p GnbProc, all []GnbProc, opts ResolveOpts) (*Subject, error) {
	home := opts.Home
	if home == "" {
		home = GetMyNetHome()
	}
	s := &Subject{
		PID:       p.PID,
		GnbBin:    p.Exe,
		ConfDir:   p.ConfDir,
		Systemd:   p.Systemd,
		MyNetHome: home,
		Source:    "process",
		Why:       fmt.Sprintf("进程锚：pid=%d cmdline -c %s", p.PID, p.ConfDir),
	}
	if abs, err := filepath.Abs(p.Exe); err == nil {
		s.GnbBin = abs
	}
	if s.NodeID == "" {
		s.NodeID = filepath.Base(filepath.Clean(p.ConfDir))
	}

	// R2：ctl/es 必须与 gnb 同目录
	binDir := filepath.Dir(s.GnbBin)
	s.GnbCtl = findSibling(binDir, "gnb_ctl")
	s.GnbEs = findSibling(binDir, "gnb_es")
	if s.GnbCtl == "" {
		s.ToolsMissing = append(s.ToolsMissing, "gnb_ctl")
		s.Warnings = append(s.Warnings,
			fmt.Sprintf("gnb_ctl 不在活主体同目录 %s —— 禁止使用其它树的 ctl（异构建读表会错）", binDir))
	}

	// R4：map 探测
	s.MapPath = resolveMapPath(p)

	// R3：候选
	seen := map[string]bool{filepath.Clean(p.ConfDir): true}
	for _, o := range all {
		if o.PID == p.PID || o.ConfDir == "" {
			continue
		}
		key := filepath.Clean(o.ConfDir)
		if seen[key] {
			continue
		}
		seen[key] = true
		note := "other-process"
		if o.Index {
			note = "index-process"
		}
		s.Candidates = append(s.Candidates, Candidate{PID: o.PID, GnbBin: o.Exe, ConfDir: o.ConfDir, Note: note})
	}
	for _, tree := range discoverConfTrees(home) {
		key := filepath.Clean(tree)
		if seen[key] {
			continue
		}
		seen[key] = true
		s.Candidates = append(s.Candidates, Candidate{ConfDir: tree, Note: "residual-tree"})
	}
	sort.Slice(s.Candidates, func(i, j int) bool { return s.Candidates[i].ConfDir < s.Candidates[j].ConfDir })

	if err := writeSubjectCache(s); err != nil {
		s.Warnings = append(s.Warnings, "写 subject 缓存失败: "+err.Error())
	}
	return s, nil
}

func pickByMode(nodes []GnbProc, opts ResolveOpts) *GnbProc {
	want := modeActiveNodeID(opts.Home)
	if want == "" {
		return nil
	}
	for i := range nodes {
		if strings.Contains(nodes[i].ConfDir, want) {
			return &nodes[i]
		}
	}
	return nil
}

func fallbackNoProcess(all []GnbProc, opts ResolveOpts, scanErr error) (*Subject, error) {
	home := opts.Home
	if home == "" {
		home = GetMyNetHome()
	}
	s := &Subject{
		MyNetHome: home,
		Source:    "fallback",
		Warnings: []string{
			"没有发现运行中的 gnb 节点进程；以下路径是回退推断，不是进程锚（R1 不满足）",
		},
	}
	if scanErr != nil {
		s.Warnings = append(s.Warnings, "进程扫描失败: "+scanErr.Error())
	}
	nodeID := opts.NodeID
	if nodeID == "" {
		nodeID = modeActiveNodeID(home)
	}
	if nodeID == "" && opts.ConfDir != "" {
		nodeID = filepath.Base(filepath.Clean(opts.ConfDir))
	}
	s.NodeID = nodeID

	confDir := opts.ConfDir
	if confDir == "" && nodeID != "" {
		confDir = filepath.Join(home, "driver", "gnb", "conf", nodeID)
	}
	s.ConfDir = confDir
	s.Why = "无进程，按 mode.json / 默认树推断"

	binDir := filepath.Join(home, "driver", "gnb", "bin")
	s.GnbBin = findSibling(binDir, "gnb")
	s.GnbCtl = findSibling(binDir, "gnb_ctl")
	s.GnbEs = findSibling(binDir, "gnb_es")
	if s.GnbBin == "" {
		s.Warnings = append(s.Warnings, "标准位无 gnb 可执行文件")
	}
	if s.GnbCtl == "" {
		s.ToolsMissing = append(s.ToolsMissing, "gnb_ctl")
		s.Warnings = append(s.Warnings, "标准位无 gnb_ctl；不要捡其它树的 ctl")
	}
	if confDir != "" {
		mp := filepath.Join(confDir, "gnb.map")
		if fileExists(mp) {
			s.MapPath = mp
		}
	}
	for _, tree := range discoverConfTrees(home) {
		if filepath.Clean(tree) == filepath.Clean(confDir) {
			continue
		}
		s.Candidates = append(s.Candidates, Candidate{ConfDir: tree, Note: "residual-tree"})
	}
	for _, o := range all {
		s.Candidates = append(s.Candidates, Candidate{PID: o.PID, GnbBin: o.Exe, ConfDir: o.ConfDir, Note: "idle-or-index"})
	}
	if len(s.ToolsMissing) > 0 {
		return s, fmt.Errorf("subject: 未找到运行中的 gnb，且工具不完整 missing=%v", s.ToolsMissing)
	}
	return s, nil
}

// resolveMapPath R4：--systemd 时 conf 目录 map 可能是残留，也可能被写。
func resolveMapPath(p GnbProc) string {
	if p.ConfDir == "" {
		return ""
	}
	confMap := filepath.Join(p.ConfDir, "gnb.map")
	confStat, confErr := os.Stat(confMap)

	var tmpMap string
	if port := listenPort(filepath.Join(p.ConfDir, "node.conf")); port != "" {
		for _, pat := range []string{
			filepath.Join(os.TempDir(), "gnb."+port+".map"),
			"/tmp/gnb." + port + ".map",
		} {
			if _, err := os.Stat(pat); err == nil {
				tmpMap = pat
				break
			}
		}
	}

	switch {
	case confErr == nil && tmpMap == "":
		return confMap
	case confErr != nil && tmpMap != "":
		return tmpMap
	case confErr == nil && tmpMap != "":
		if st, err := os.Stat(tmpMap); err == nil {
			if p.Systemd || st.ModTime().After(confStat.ModTime()) {
				return tmpMap
			}
		}
		return confMap
	}
	return ""
}

func listenPort(nodeConf string) string {
	b, err := os.ReadFile(nodeConf)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return ""
		}
		if i := strings.LastIndex(f[1], ":"); i >= 0 {
			return f[1][i+1:]
		}
		return f[1]
	}
	return ""
}

// discoverConfTrees 扫常见 home 下的 conf/<nodeid> 残留树（R3）。
//
// D4-E 起新唯一存放位是 conf/<form>/<nodeid>/；旧 driver/gnb/conf 等只作
// **残留探测**（未迁移节点 / 迁移前排障），所以两边都扫。
func discoverConfTrees(home string) []string {
	var out []string
	roots := []string{
		filepath.Join(home, "conf", "local"),
		filepath.Join(home, "conf", "online"),
		filepath.Join(home, "driver", "gnb", "conf"),
		"/var/lib/mynet/driver/gnb/conf",
		"/opt/mynet/driver/gnb/conf",
		"/usr/local/opt/mynet/driver/gnb/conf",
		"/etc/mynet/driver/gnb/conf",
		"/opt/mynet/var/gnb", // 188 实测形态
	}
	for _, r := range roots {
		entries, err := os.ReadDir(r)
		if err != nil {
			continue
		}
		// 根本身就是 conf 目录（含 node.conf）
		if fileExists(filepath.Join(r, "node.conf")) || fileExists(filepath.Join(r, "gnb.map")) {
			out = append(out, r)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// conf/<form> 下的节点目录；conf/online 下还有一层 <nodeid>（无租户层）
			p := filepath.Join(r, e.Name())
			if fileExists(filepath.Join(p, "node.conf")) || fileExists(filepath.Join(p, "gnb.map")) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

func modeActiveNodeID(home string) string {
	if home == "" {
		home = GetMyNetHome()
	}
	// mode.json 2026-10-12 起在 conf/ 下；旧位置只**读**兼容
	// （搬迁由 shared/config 做 —— utils 不能反向依赖 config，故这里不做迁移）。
	b, err := os.ReadFile(filepath.Join(home, "conf", "mode.json"))
	if err != nil {
		b, err = os.ReadFile(filepath.Join(home, "mode.json"))
	}
	if err != nil {
		return ""
	}
	var m struct {
		Active struct {
			NodeID string `json:"node_id"`
		} `json:"active"`
	}
	if json.Unmarshal(b, &m) == nil {
		return m.Active.NodeID
	}
	return ""
}

func findSibling(dir, name string) string {
	for _, n := range []string{name, name + ".exe"} {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// WriteSubjectCache 落 var/subject.json，供脚本读（不猜树）。
func WriteSubjectCache(s *Subject) error {
	return writeSubjectCache(s)
}

func writeSubjectCache(s *Subject) error {
	dir := VarDir()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "subject.json"), b, 0o600)
}
