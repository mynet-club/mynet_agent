package mynet

import (
	"fmt"
	"os"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/upgrade"
)

// cmdUpdate — R2-5 / U4：agent 自更新（ComponentMynet）。
// 驱动升级走 `mynet driver install|upgrade`（D-9，scope 互补）。
//
//		mynet update [--dry-run] [--check]
//
//	  - --dry-run 只打印升级计划（目标版本/校验和/备份/回滚步骤），不下载不替换
//	  - --check   只比较版本，有新版本退出码 10（脚本可判断）
//
// 真升级顺序（InstallWith）：下载 → sha256 校验 → 解包 → 备份 → 原子替换 → 自检 → 失败回滚。
func cmdUpdate(args []string) int {
	dryRun := false
	checkOnly := false
	for _, a := range args {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--check":
			checkOnly = true
		default:
			fmt.Fprintf(os.Stderr, "用法: mynet update [--dry-run] [--check]\n")
			return 2
		}
	}

	index := "https://ctl.mynet.club"
	if v := os.Getenv("MYNET_APPS_INDEX"); v != "" {
		index = v
	}
	svc := upgrade.NewService(index)

	all, err := svc.Fetch(index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: 拉索引失败: %v\n", err)
		return 1
	}
	best := pickLatestMynet(all)
	if best == nil {
		fmt.Fprintf(os.Stderr, "update: 索引中无 mynet 组件\n")
		return 1
	}

	cur := Version
	cmp := upgrade.CompareVersions(best.Version, cur)

	if checkOnly {
		if cmp > 0 {
			fmt.Printf("update available: %s -> %s\n", cur, best.Version)
			return 10
		}
		fmt.Printf("up to date: %s\n", cur)
		return 0
	}

	if dryRun {
		printUpdatePlan(cur, *best, cmp)
		return 0
	}

	if cmp <= 0 {
		fmt.Printf("已是最新 %s（索引 %s）\n", cur, best.Version)
		return 0
	}

	fmt.Printf("latest mynet %s %s\n", best.Version, best.Platform)
	if err := svc.Install(*best); err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		return 1
	}
	return 0
}

// pickLatestMynet 按语义版本取最新的 mynet 组件。
// 版本比较必须按语义而非字典序：字典序下 "0.10.0" < "0.9.0"，
// 会把更旧的版本当成「最新」——症状是升级后版本号变小，且不报错。
func pickLatestMynet(all []upgrade.Artifact) *upgrade.Artifact {
	var best *upgrade.Artifact
	for i := range all {
		if all[i].Component != upgrade.ComponentMynet {
			continue
		}
		if best == nil || upgrade.CompareVersions(all[i].Version, best.Version) > 0 {
			best = &all[i]
		}
	}
	return best
}

// printUpdatePlan — U4 dry-run：把「校验和 + 回滚」计划摊开，不执行。
func printUpdatePlan(cur string, a upgrade.Artifact, cmp int) {
	fmt.Println("update plan（--dry-run，未下载未替换）")
	fmt.Printf("  当前版本: %s\n", cur)
	fmt.Printf("  目标版本: %s  (%s)\n", a.Version, a.Platform)
	switch {
	case cmp > 0:
		fmt.Println("  动作:     升级")
	case cmp == 0:
		fmt.Println("  动作:     无（版本相同）")
	default:
		fmt.Println("  动作:     无（索引版本更旧，拒绝降级）")
	}
	fmt.Printf("  产物 URL: %s\n", a.URL)
	if a.Checksum != "" {
		fmt.Printf("  校验和:   sha256 %s\n", normalizeSum(a.Checksum))
		fmt.Println("  校验步骤: 下载 → 算 sha256 → 与上值比对；不符即拒，不解包不替换")
	} else {
		fmt.Println("  校验和:   缺失 —— Install 会拒绝安装（完整性保护不降级）")
	}
	fmt.Println("  安装步骤: 解包 → 备份现有二进制为 *.bak → 原子替换 → 自检 (mynet version)")
	fmt.Println("  回滚步骤: 自检失败 → 用 *.bak 还原原二进制 → 报错退出")
	fmt.Println("  成功之后: 自检通过才删 *.bak（默认保留，除非 RemoveBackup）")
}

func normalizeSum(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
