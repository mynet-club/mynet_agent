package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdTree 配置树诊断/收敛（C4 / spec/paths.md §3）。
//
//	mynet tree doctor [--json]     只读诊断：列出各树、标活主体/残留
//	mynet tree consolidate --dry-run  演示如何把残留改名 *.legacy-YYYYMMDD（不执行）
//	mynet tree consolidate           执行改名（仍不删）
//
// 目标：每机只有一棵 Store Root。残留只改名隔离，**不删除**。
func cmdTree(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet tree doctor [--json] | consolidate [--dry-run]")
		return 2
	}
	switch args[0] {
	case "doctor":
		return treeDoctor(args[1:])
	case "consolidate":
		return treeConsolidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 tree 子命令: %s\n", args[0])
		return 2
	}
}

type treeDoctorReport struct {
	Home       string     `json:"home"`
	HomeSource string     `json:"home_source"`
	Subject    any        `json:"subject,omitempty"`
	Trees      []treeInfo `json:"trees"`
	Warnings   []string   `json:"warnings,omitempty"`
}

type treeInfo struct {
	Path      string   `json:"path"`
	Kind      string   `json:"kind"` // store-root | conf-tree | residual
	Note      string   `json:"note,omitempty"`
	HasMode   bool     `json:"has_mode"`
	HasDriver bool     `json:"has_driver"`
	NodeDirs  []string `json:"node_dirs,omitempty"`
}

func treeDoctor(args []string) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}

	home := utils.GetMyNetHome()
	src := utils.HomeSource()
	rep := treeDoctorReport{
		Home:       home,
		HomeSource: src,
	}

	// 活主体（进程锚）
	subj, _ := utils.ResolveSubject(utils.ResolveOpts{})
	if subj != nil {
		rep.Subject = subj
	}

	// 候选树
	for _, t := range discoverTreeRoots(home) {
		info := treeInfo{
			Path:      t.Path,
			Kind:      t.Kind,
			HasMode:   fileExists(filepath.Join(t.Path, "mode.json")),
			HasDriver: isDir(filepath.Join(t.Path, "driver")),
			NodeDirs:  listNodeDirs(t.Path),
		}
		// 与活主体比对：只把「树根」标成 store-root，conf 子目录保持 conf-tree
		if subj != nil && subj.ConfDir != "" {
			cleanTree := filepath.Clean(t.Path)
			cleanConf := filepath.Clean(subj.ConfDir)
			if cleanConf == cleanTree || strings.HasPrefix(cleanConf, cleanTree+string(filepath.Separator)) {
				if t.Kind != "conf-tree" {
					info.Kind = "store-root"
					info.Note = "活主体所在"
				} else {
					info.Note = "活主体 conf 目录"
				}
			}
		}
		if src != "env" && info.Kind == "store-root" {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("MYNET_HOME 未设（home 来自 %s）—— 装服务前请 export MYNET_HOME=%s", src, t.Path))
		}
		rep.Trees = append(rep.Trees, info)
	}

	// 多树警告
	roots := 0
	for _, t := range rep.Trees {
		if t.Kind == "store-root" || t.HasMode {
			roots++
		}
	}
	if roots > 1 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("发现 %d 个像 Store Root 的树 —— 应收敛到一棵；见 mynet tree consolidate", roots))
	}
	if subj != nil && len(subj.ToolsMissing) > 0 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("主体工具不完整 missing=%v（R2/R5）", subj.ToolsMissing))
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printTreeHuman(rep)
	}
	if len(rep.Warnings) > 0 {
		return 1
	}
	return 0
}

func printTreeHuman(rep treeDoctorReport) {
	fmt.Printf("配置树诊断  home=%s (source=%s)\n", rep.Home, rep.HomeSource)
	if s, ok := rep.Subject.(map[string]any); ok {
		fmt.Printf("活主体: conf=%v source=%v\n", s["conf_dir"], s["source"])
	}
	fmt.Printf("候选树（%d）:\n", len(rep.Trees))
	for _, t := range rep.Trees {
		mark := " "
		switch t.Kind {
		case "store-root":
			mark = "★"
		case "residual":
			mark = "·"
		}
		fmt.Printf("  %s %-14s %s", mark, t.Kind, t.Path)
		if t.Note != "" {
			fmt.Printf("  (%s)", t.Note)
		}
		fmt.Println()
		if len(t.NodeDirs) > 0 {
			fmt.Printf("      nodes: %s\n", strings.Join(t.NodeDirs, ", "))
		}
	}
	for _, w := range rep.Warnings {
		fmt.Printf("  ⚠ %s\n", w)
	}
	if len(rep.Warnings) == 0 {
		fmt.Println("✓ 无多树/无 env 警告")
	}
}

// treeConsolidate 残留树改名隔离（不删）。
func treeConsolidate(args []string) int {
	dry := false
	for _, a := range args {
		if a == "--dry-run" {
			dry = true
		}
	}
	if _, code := requireHome(); code != 0 {
		return code
	}
	home := utils.GetMyNetHome()
	subj, _ := utils.ResolveSubject(utils.ResolveOpts{})

	keep := home
	if subj != nil && subj.ConfDir != "" {
		// 保留活主体所在的树根
		keep = storeRootOf(subj.ConfDir, home)
	}

	renamed := 0
	for _, t := range discoverTreeRoots(home) {
		if filepath.Clean(t.Path) == filepath.Clean(keep) {
			continue
		}
		// 只动明显的残留：有 driver/ 或 mode.json 或多 node 目录
		hasMode := fileExists(filepath.Join(t.Path, "mode.json"))
		hasDriver := isDir(filepath.Join(t.Path, "driver"))
		nodeDirs := listNodeDirs(t.Path)
		if !hasMode && !hasDriver && len(nodeDirs) == 0 {
			continue
		}
		if t.Kind == "conf-tree" {
			continue // 节点 conf 目录由收敛脚本整体处理，不单个改名
		}
		target := t.Path + ".legacy-" + timestamp()
		fmt.Printf("  %s %s → %s\n", map[bool]string{true: "PLAN", false: "RENAME"}[dry], t.Path, target)
		if !dry {
			if err := os.Rename(t.Path, target); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ 改名失败: %v\n", err)
				continue
			}
		}
		renamed++
	}
	fmt.Printf("共 %d 个残留树（%s）\n", renamed, map[bool]string{true: "dry-run 未执行", false: "已改名"}[dry])
	return 0
}

func storeRootOf(confDir, fallback string) string {
	// 向上找含 mode.json 或 driver/ 的根
	p := filepath.Clean(confDir)
	for i := 0; i < 6; i++ {
		if fileExists(filepath.Join(p, "mode.json")) || isDir(filepath.Join(p, "driver")) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return fallback
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func timestamp() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

type foundTree struct {
	Path string
	Kind string
}

func discoverTreeRoots(home string) []foundTree {
	var out []foundTree
	seen := map[string]bool{}

	add := func(p, kind string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if seen[p] || !isDir(p) {
			return
		}
		seen[p] = true
		out = append(out, foundTree{Path: p, Kind: kind})
	}

	add(home, "store-root")

	// 常见位（evidence/20260930-subject）
	for _, p := range []string{
		"/var/lib/mynet",
		"/opt/mynet",
		"/usr/local/opt/mynet",
		"/etc/mynet",
		"/usr/local/mynet",
		`C:\mynet`,
		`C:\Program Files\mynet`,
		`C:\mynet-test`,
	} {
		if filepath.Clean(p) != filepath.Clean(home) {
			add(p, "residual")
		}
	}

	// home 下 driver/gnb/conf 的兄弟节点目录也算候选
	conf := filepath.Join(home, "driver", "gnb", "conf")
	if entries, err := os.ReadDir(conf); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(conf, e.Name()), "conf-tree")
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func listNodeDirs(root string) []string {
	var out []string
	// driver/gnb/conf/*
	entries, err := os.ReadDir(filepath.Join(root, "driver", "gnb", "conf"))
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
