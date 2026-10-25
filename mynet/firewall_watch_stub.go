//go:build !linux || openwrt

package mynet

import "fmt"

func cmdFirewallWatch(args []string) int {
	fmt.Println("firewalld event hook is only available on generic Linux")
	return 2
}
