package mynet

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdSubject 主体定位器 CLI —— knowledge-base/spec/paths.md §4。
//
//	mynet subject [--pid N | --node-id ID | --conf DIR] [--json]
//
// 进程锚优先（R1）；gnb_ctl 必须与 gnb 同目录（R2）；
// 工具缺失显式（R5）。脚本请读 --json 或 $MYNET_HOME/var/subject.json，
// **不要**自己猜 conf 树。
func cmdSubject(args []string) int {
	opts := utils.ResolveOpts{}
	asJSON := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pid":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "subject: --pid 需要参数")
				return 2
			}
			fmt.Sscanf(args[i+1], "%d", &opts.PID)
			i++
		case "--node-id":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "subject: --node-id 需要参数")
				return 2
			}
			opts.NodeID = args[i+1]
			i++
		case "--conf":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "subject: --conf 需要参数")
				return 2
			}
			opts.ConfDir = args[i+1]
			i++
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Print(`mynet subject — 定位 gnb 主体（bin / conf / map / ctl）

用法: mynet subject [--pid N | --node-id ID | --conf DIR] [--json]

规则（spec/paths.md §4）:
  R1 进程锚优先：cmdline -c 与 exe 是唯一真相
  R2 gnb_ctl 必须与 gnb 同目录，缺则 tools_missing，禁止捡异构建
  R3 多 home 列出 candidates，不猜
  R4 --systemd 时探测 map（conf 目录 vs /tmp/gnb.<port>.map）
  R5 工具缺失显式报错

脚本用法:
  mynet subject --json | jq -r .conf_dir
  mynet subject --json | jq -r .gnb_ctl   # 为空表示套件不完整，停手
`)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "subject: 未知参数 %s\n", args[i])
			return 2
		}
	}

	s, err := utils.ResolveSubject(opts)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s)
	} else {
		printSubjectHuman(s)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if len(s.ToolsMissing) > 0 {
		fmt.Fprintf(os.Stderr, "✗ 工具不完整 missing=%v\n", s.ToolsMissing)
		return 1
	}
	return 0
}

func printSubjectHuman(s *utils.Subject) {
	if s == nil {
		fmt.Println("（无结果）")
		return
	}
	fmt.Printf("主体（source=%s）\n", s.Source)
	fmt.Printf("  why      : %s\n", s.Why)
	fmt.Printf("  node_id  : %s\n", s.NodeID)
	fmt.Printf("  pid      : %d\n", s.PID)
	fmt.Printf("  gnb_bin  : %s\n", s.GnbBin)
	fmt.Printf("  conf_dir : %s\n", s.ConfDir)
	fmt.Printf("  map_path : %s\n", s.MapPath)
	fmt.Printf("  gnb_ctl  : %s\n", s.GnbCtl)
	fmt.Printf("  gnb_es   : %s\n", s.GnbEs)
	fmt.Printf("  home     : %s\n", s.MyNetHome)
	if s.Systemd {
		fmt.Printf("  systemd  : yes\n")
	}
	if len(s.ToolsMissing) > 0 {
		fmt.Printf("  tools_missing: %v\n", s.ToolsMissing)
	}
	for _, w := range s.Warnings {
		fmt.Printf("  ⚠ %s\n", w)
	}
	if len(s.Candidates) > 0 {
		fmt.Printf("候选（%d）:\n", len(s.Candidates))
		for _, c := range s.Candidates {
			fmt.Printf("  - pid=%d conf=%s note=%s\n", c.PID, c.ConfDir, c.Note)
		}
	}
}
