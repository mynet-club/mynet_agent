//go:build linux && !openwrt

package mynet

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func cmdFirewallWatch(args []string) int {
	if len(args) != 0 {
		return 2
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchSender("org.fedoraproject.FirewallD1"), dbus.WithMatchInterface("org.fedoraproject.FirewallD1"), dbus.WithMatchMember("Reloaded")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Subscribe before initial replay, so reload racing service startup is not missed.
	replay := func() error { return LoadNftArtifacts(utils.GetMyNetHome()) }
	if err := replay(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for {
		select {
		case <-conn.Context().Done():
			return 1
		case signal := <-signals:
			if signal == nil {
				return 1
			}
			if signal.Name == "org.fedoraproject.FirewallD1.Reloaded" {
				if err := replay(); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
			}
		}
	}
}
