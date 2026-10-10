package mynet

// 服务单元/init 的**名字与落盘名**在这里（跨平台共享的纯契约，无模板数据）；
// 各自的**模板数据**按平台编译，不互相夹带：
//   - units_systemd.go（`linux && !openwrt`）：mynetd.service / mynet_gnb.service
//   - units_openwrt.go（`linux && openwrt`）：openwrt_mynetd.init / openwrt_mynet.init
//
// ⚠️ 为什么模板放包里 embed 而不是 deploy/：`//go:embed` 不能越过包目录，而 install
// 必须在**分发出去的二进制**里自带单元 —— 否则新机器只拿到 mynet 二进制、装不出服务。
// deploy/systemd/ 只保留不归 mynet install 管的单元（如 mynet_gateway.service）。

// unitFileName 落到 /etc/systemd/system/ 下的文件名。
func unitFileName(name string) string {
	if name == "gnb" {
		name = "mynet_gnb"
	}

	return name + ".service"
}

// SystemdUnitNames mynet install 会写的两个单元（顺序即启动顺序）。
var SystemdUnitNames = []string{"mynetd", "mynet_gnb"}

// OpenWrtInitNames procd init 脚本名（落到 /etc/init.d/）。
var OpenWrtInitNames = []string{"mynetd", "mynet"}

func containsLine(body, prefix string) bool {
	for _, ln := range splitLines(body) {
		if len(ln) >= len(prefix) && ln[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
