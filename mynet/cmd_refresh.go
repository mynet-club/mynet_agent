package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// cmdRefresh — R2-3：refresh-{routes,address,config,keys}
//
//	mynet refresh-routes --auto --node-id N
//	mynet refresh-address --auto [--vpn-type wireguard]
//	mynet refresh-config --auto
//	mynet refresh-keys --auto
//
// 输入：$MYNET_HOME/var/bundle.json（BundleEnvelope v2）或 stdin。
func cmdRefresh(args []string) int {
	what := strings.TrimPrefix(args0(args), "refresh-")
	rest := args
	auto := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--auto" {
			auto = true
			rest = append(args[:i], args[i+1:]...)
			break
		}
	}
	_ = auto
	_ = rest

	env, err := loadBundleEnvelope()
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh-%s: 读 bundle 失败: %v\n", what, err)
		return 1
	}

	switch what {
	case "routes":
		return refreshRoutes(env)
	case "address":
		return refreshAddress(env)
	case "config":
		return refreshConfig(env)
	case "keys":
		return refreshKeys(env)
	default:
		fmt.Fprintf(os.Stderr, "未知 refresh 域: %s\n", what)
		return 2
	}
}

func args0(args []string) string {
	// mynet refresh-routes … → Run 收到的是 ["refresh-routes", …]
	if len(args) > 0 && strings.HasPrefix(args[0], "refresh-") {
		return args[0]
	}
	if v := os.Getenv("MYNET_REFRESH"); v != "" {
		return "refresh-" + v
	}
	return "refresh-routes"
}

func loadBundleEnvelope() (*domain.BundleEnvelope, error) {
	p := filepath.Join(varDir(), "bundle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return domain.DecodeBundleEnvelope(raw)
}

func varDir() string {
	if v := os.Getenv("MYNET_HOME"); v != "" {
		return v + "/var"
	}
	return sharedVarDir()
}

func sharedVarDir() string {
	return filepath.Dir(shared.DefaultSnapshotPath())
}

func refreshRoutes(env *domain.BundleEnvelope) int {
	// L6：NetworkIntent.Routes → apply 计划
	var plan ApplyPlan
	for _, r := range env.Network.Routes {
		plan.Routes = append(plan.Routes, RouteOp{
			Action:      "replace",
			Destination: r.Prefix,
			Gateway:     r.Via,
			Device:      r.Dev,
			Metric:      r.Metric,
		})
	}
	return printPlan(plan)
}

func refreshAddress(env *domain.BundleEnvelope) int {
	for _, a := range env.Network.Addresses {
		fmt.Println("addr", a)
	}
	return 0
}

func refreshConfig(env *domain.BundleEnvelope) int {
	f := driver.NewFactory()
	switch env.Driver {
	case domain.DriverTypeWireGuard:
		d := driver.NewWireGuardDriver(env.Node.Interface, 0)
		d.ConfPath = filepath.Join(varDir(), "wg.conf")
		if err := d.WriteIntent(env.WireGuard); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", d.ConfPath)
	case domain.DriverTypeGNB:
		d := driver.NewGNBDriver(0)
		d.ConfDir = filepath.Join(varDir(), "gnb")
		if err := d.WriteIntent(env.Gnb); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("wrote", d.ConfDir)
	default:
		_ = f
		fmt.Fprintf(os.Stderr, "unknown driver %s\n", env.Driver)
		return 1
	}
	return 0
}

func refreshKeys(env *domain.BundleEnvelope) int {
	// 密钥落盘归 WriteConfig；此处只报告意图中的公钥指纹
	if env.Gnb != nil {
		fmt.Println("gnb identity node", env.Gnb.Identity.NodeID, "alg", env.Gnb.Identity.KeyAlgorithm)
	}
	if env.WireGuard != nil {
		fmt.Println("wireguard listen", env.WireGuard.Interface.ListenPort, "peers", len(env.WireGuard.Peers))
	}
	return 0
}

func printPlan(p ApplyPlan) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
	return 0
}
