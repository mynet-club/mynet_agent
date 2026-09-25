package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mynet-club/mynet_agent-V2/shared"
)

// ApplyPlan 是 L6 写入计划：只含系统网络语义，不含驱动原生语法。
// 跨驱动互通（A gnb → C 双驱动 → B wg）就是这里的几条 Route，驱动层互不知晓对方。
type ApplyPlan struct {
	Routes     []RouteOp    `json:"routes,omitempty"`
	Firewall   []FirewallOp `json:"firewall,omitempty"`
	Interfaces []IfaceOp    `json:"interfaces,omitempty"`
	ExtraExec  []ExecOp     `json:"extra_exec,omitempty"` // 兜底；优先结构化字段
	// LocalAddresses 本机 overlay 地址（如 10.0.0.15/32）。
	// 用途只有一个：bundle 里的 Device 是**逻辑名**（gnb_tun），而 darwin 的 gnb
	// 会拿到内核分配的 utunN（gnb_drv_darwin.c:249-252 把 ifname 覆盖成
	// UTUN_OPT_IFNAME）。执行期用这里的 IP 反查真实接口，否则路由装到
	// 一个不存在的 gnb_tun 上 —— 症状是「隧道起了、IP 有了、ping 全丢」。
	LocalAddresses []string `json:"local_addresses,omitempty"`
}

type RouteOp struct {
	Destination string `json:"destination"` // 10.8.0.0/24
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device"` // gnb0 / wg0 —— 对 apply 无差别
	Metric      int    `json:"metric,omitempty"`
	Action      string `json:"action"` // add | replace | del
}

type FirewallOp struct {
	Table   string `json:"table,omitempty"`
	Chain   string `json:"chain,omitempty"`
	Action  string `json:"action"` // ensure-masq | open-port | …
	Device  string `json:"device,omitempty"`
	Port    int    `json:"port,omitempty"`
	Proto   string `json:"proto,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type IfaceOp struct {
	Name   string `json:"name"`
	Action string `json:"action"` // up | down
	MTU    int    `json:"mtu,omitempty"`
}

type ExecOp struct {
	// GUARD-OK 逃生舱：仅当结构化字段无法表达时使用；优先改计划结构
	Argv []string `json:"argv"`
}

func readApplyPlan(path string) (*ApplyPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p ApplyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// routeCmd 是一条待执行的路由命令。optional 表示失败可忽略 ——
// BSD 没有 `ip route replace`，replace 只能「先删后加」，而 delete
// 在路由本来就不存在时会失败，那不是错误。
type routeCmd struct {
	argv     []string
	optional bool
}

// executeApply 按 system.json 能力选择工具执行。唯一写入入口（F-5）。
func executeApply(snap *shared.SystemSnapshot, plan *ApplyPlan, dry bool) int {
	fail := 0
	for _, r := range plan.Routes {
		r.Device = resolveDevice(r.Device, plan.LocalAddresses)
		for _, c := range routeCmds(snap, r) {
			if err := runOp(dry, c.argv); err != nil {
				if c.optional {
					continue
				}
				fmt.Fprintf(os.Stderr, "route %s %s: %v\n", r.Action, r.Destination, err)
				fail = 1
			}
		}
	}
	for _, f := range plan.Firewall {
		argv := firewallArgv(snap, f)
		if len(argv) == 0 {
			fmt.Fprintf(os.Stderr, "firewall %s: 当前 system.json 无可用工具\n", f.Action)
			fail = 1
			continue
		}
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "firewall %s: %v\n", f.Action, err)
			fail = 1
		}
	}
	for _, i := range plan.Interfaces {
		argv := ifaceArgv(snap, i)
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "iface %s %s: %v\n", i.Action, i.Name, err)
			fail = 1
		}
	}
	for _, e := range plan.ExtraExec {
		if err := runOp(dry, e.Argv); err != nil {
			fmt.Fprintf(os.Stderr, "exec %v: %v\n", e.Argv, err)
			fail = 1
		}
	}
	return fail
}

// preferBSDNetTools 当前环境是否该用 route/ifconfig 而不是 ip。
//
// 判据是 system.json 的 capabilities，**不是 runtime.GOOS**：macOS 装了
// iproute2mac 时 `ip` 可用，那条路更统一；没装才是 BSD 语义。Linux 一般有 ip，
// 极老的发行版退到 net-tools 的 route，参数形状与 darwin 的 route 同族。
func preferBSDNetTools(snap *shared.SystemSnapshot) bool {
	if snap == nil {
		// 测试与未知快照：保持既有 `ip route` 形状，避免静默换方言
		return false
	}
	return !snap.Capabilities["ip"]
}

// resolveDevice 把 bundle 里的**逻辑设备名**解析成此刻真实存在的接口。
//
// 情况一（darwin/gnb）：配置写 ifname gnb_tun，但 gnb_drv_darwin.c:249-252
// 在 tun_alloc 之后用 UTUN_OPT_IFNAME 把 ifname 覆盖成 utunN ——
// 系统里根本没有叫 gnb_tun 的接口。
// 情况二：接口还在、名字也对，直接用。
// 解析不出来时**原样返回**，让后面的命令去失败 —— 静默换成别的接口
// 会把路由装到错误的网卡上，比失败更糟。
func resolveDevice(name string, localAddrs []string) string {
	if name == "" {
		return name
	}
	if _, err := net.InterfaceByName(name); err == nil {
		return name
	}
	for _, cidr := range localAddrs {
		ip := parseAddr(cidr)
		if ip == nil {
			continue
		}
		if iface := ifaceWithIP(ip); iface != "" {
			return iface
		}
	}
	return name
}

func parseAddr(s string) net.IP {
	if ip, _, err := net.ParseCIDR(s); err == nil {
		return ip
	}
	return net.ParseIP(s)
}

func ifaceWithIP(ip net.IP) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(ip) {
				return iface.Name
			}
		}
	}
	return ""
}

// isHostDest 目的地是主机路由（/32、/128 或裸 IP）还是网段。
func isHostDest(dest string) bool {
	if _, ipnet, err := net.ParseCIDR(dest); err == nil {
		ones, bits := ipnet.Mask.Size()
		return ones == bits
	}
	return net.ParseIP(dest) != nil
}

// hostDestIP 主机路由在 BSD `route` 下要传裸 IP，不带 /32。
func hostDestIP(dest string) string {
	if ip, _, err := net.ParseCIDR(dest); err == nil {
		return ip.String()
	}
	return dest
}

func routeCmds(snap *shared.SystemSnapshot, r RouteOp) []routeCmd {
	if preferBSDNetTools(snap) {
		return bsdRouteCmds(r)
	}
	verb := map[string]string{"add": "add", "replace": "replace", "del": "del"}[r.Action]
	if verb == "" {
		verb = "replace"
	}
	argv := []string{"ip", "route", verb, r.Destination}
	if r.Gateway != "" {
		argv = append(argv, "via", r.Gateway)
	}
	if r.Device != "" {
		argv = append(argv, "dev", r.Device)
	}
	if r.Metric > 0 {
		argv = append(argv, "metric", fmt.Sprintf("%d", r.Metric))
	}
	return []routeCmd{{argv: argv}}
}

// bsdRouteCmds darwin/net-tools 的 route 方言。
//
// macOS 的 route 没有 replace，只能 delete（忽略「不存在」）+ add。
// metric 在这条方言下没有对应物，直接丢掉而不是拼一个无效参数。
func bsdRouteCmds(r RouteOp) []routeCmd {
	kind := "-net"
	dest := r.Destination
	if isHostDest(dest) {
		kind = "-host"
		dest = hostDestIP(dest)
	}
	build := func(verb string) []string {
		argv := []string{"route", "-n", verb, kind, dest}
		// delete 只要目的地；带上 -interface 在接口已换名（utun 重分配）时
		// 反而删不掉旧路由。
		if verb == "delete" {
			return argv
		}
		if r.Gateway != "" {
			argv = append(argv, r.Gateway)
		} else if r.Device != "" {
			argv = append(argv, "-interface", r.Device)
		}
		return argv
	}
	switch r.Action {
	case "del":
		return []routeCmd{{argv: build("delete")}}
	case "add":
		return []routeCmd{{argv: build("add")}}
	default: // replace
		return []routeCmd{
			{argv: build("delete"), optional: true},
			{argv: build("add")},
		}
	}
}

func firewallArgv(snap *shared.SystemSnapshot, f FirewallOp) []string {
	switch f.Action {
	case "ensure-masq":
		if snap.Capabilities["nft"] {
			return []string{"nft", "add", "rule", "ip", "nat", "POSTROUTING",
				"oifname", f.Device, "masquerade"}
		}
		if snap.Capabilities["iptables"] {
			return []string{"iptables", "-t", "nat", "-A", "POSTROUTING",
				"-o", f.Device, "-j", "MASQUERADE"}
		}
		return nil
	case "open-port":
		if snap.Capabilities["iptables"] {
			argv := []string{"iptables", "-A", "INPUT", "-p", f.Proto, "--dport", fmt.Sprintf("%d", f.Port), "-j", "ACCEPT"}
			if f.Comment != "" {
				argv = append(argv, "-m", "comment", "--comment", f.Comment)
			}
			return argv
		}
		return nil
	default:
		return nil
	}
}

func ifaceArgv(snap *shared.SystemSnapshot, i IfaceOp) []string {
	verb := map[string]string{"up": "up", "down": "down"}[i.Action]
	if preferBSDNetTools(snap) {
		return []string{"ifconfig", i.Name, verb}
	}
	return []string{"ip", "link", "set", "dev", i.Name, verb}
}

// resolveBin 把命令名解析成可执行路径。
//
// ⚠️ daemon 环境的 PATH 往往是残的：systemd 的默认 PATH 不含 /sbin，
// 我们手工起 mynetd 时也容易漏掉 /sbin —— 而 `route`/`ifconfig` 就在
// /sbin 里。症状是 apply 在终端能跑、在向导里报
// `exec: "route": executable file not found in $PATH`。
// 所以 LookPath 失败后再扫几个系统目录，而不是直接丢给 exec。
func resolveBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/usr/bin", "/bin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name
}

func runOp(dry bool, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	if dry {
		fmt.Printf("DRY-RUN: %v\n", argv)
		return nil
	}
	argv = append([]string{resolveBin(argv[0])}, argv[1:]...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
