package mynet

import (
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent-V2/mynet/upgrade"
)

// cmdUpdate — R2-5：agent 自更新（ComponentMynet）。
// 驱动升级走 `mynet driver install|upgrade`（D-9，scope 互补）。
func cmdUpdate(args []string) int {
	index := "https://ctl.mynet.club"
	if v := os.Getenv("MYNET_APPS_INDEX"); v != "" {
		index = v
	}
	svc := upgrade.NewService(index)
	// 自更新：Fetch 后挑 ComponentMynet 最新
	all, err := svc.Fetch(index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: 拉索引失败: %v\n", err)
		return 1
	}
	var best *upgrade.Artifact
	for i := range all {
		if all[i].Component == upgrade.ComponentMynet {
			if best == nil || all[i].Version > best.Version {
				best = &all[i]
			}
		}
	}
	if best == nil {
		fmt.Fprintf(os.Stderr, "update: 索引中无 mynet 组件\n")
		return 1
	}
	fmt.Printf("latest mynet %s %s\n", best.Version, best.Platform)
	if err := svc.Install(*best); err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		return 1
	}
	return 0
}
