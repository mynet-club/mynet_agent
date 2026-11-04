package mynet

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

// The launcher replaces itself with GNB: service managers retain the same PID,
// cgroup, signal delivery and restart ownership. It never calls driver.Start.
func cmdUTC(args []string) int {
	if len(args) > 0 && args[0] == "authority" {
		return cmdUTCAuthority(args[1:])
	}
	if len(args) > 0 && (args[0] == "publish" || args[0] == "publish-peer" || args[0] == "history" || args[0] == "verify") {
		return cmdUTCProjection(args)
	}
	if len(args) == 0 || args[0] != "driver-run" {
		fmt.Fprintln(os.Stderr, "usage: mynet utc authority verify|install-floor [options]\n       mynet utc driver-run --binary PATH -c DIR [--systemd]\n       mynet utc publish -c DIR --binary PATH --file projection.json\n       mynet utc verify -c DIR --file projection.json --boot ID --pid PID (local APPLIED only)")
		return 2
	}
	flags := flag.NewFlagSet("utc driver-run", flag.ContinueOnError)
	binary := flags.String("binary", "", "GNB executable")
	conf := flags.String("c", "", "GNB configuration directory")
	systemd := flags.Bool("systemd", false, "GNB Linux foreground mode")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *binary == "" || *conf == "" || flags.NArg() != 0 {
		return 2
	}
	d := driver.NewGNBDriver(0)
	d.Binary = *binary
	d.ConfDir = *conf
	launchArgs, err := d.UTCLaunchArgs()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *systemd {
		launchArgs = append(launchArgs, "--systemd")
	}
	resolved, err := exec.LookPath(*binary)
	if err == nil {
		err = replaceUTCProcess(resolved, append([]string{resolved}, launchArgs...), os.Environ())
	}
	fmt.Fprintln(os.Stderr, "UTC driver launch:", err)
	return 1
}

func cmdUTCProjection(args []string) int {
	flags := flag.NewFlagSet("utc "+args[0], flag.ContinueOnError)
	conf := flags.String("c", "", "GNB configuration directory")
	binary := flags.String("binary", "", "GNB executable")
	input := flags.String("file", "", "trusted local projection JSON")
	boot := flags.String("boot", "", "independently observed current GNB boot ID")
	pid := flags.Uint64("pid", 0, "independently observed current GNB PID")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *conf == "" || flags.NArg() != 0 || args[0] != "history" && *input == "" {
		return 2
	}
	d := driver.NewGNBDriver(0)
	d.ConfDir = *conf
	d.Binary = *binary
	if args[0] == "history" {
		projection, err := d.ReadUTCProjection()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			State      string                `json:"state"`
			Projection *driver.UTCProjection `json:"projection"`
		}{"WATERMARK", projection})
		return 0
	}
	file, err := os.Open(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 131073))
	if err != nil || len(raw) > 131072 {
		fmt.Fprintln(os.Stderr, "UTC projection input unreadable or too large")
		return 1
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var projection driver.UTCProjection
	if err = decoder.Decode(&projection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		fmt.Fprintln(os.Stderr, "UTC projection has trailing data")
		return 1
	}
	if args[0] == "publish-peer" {
		if len(projection.Peers) != 1 || projection.ConfigGen != 0 {
			fmt.Fprintln(os.Stderr, "publish-peer needs one peer and config_gen=0")
			return 2
		}
		projection, err = d.PublishUTCPeer(projection.Scope, projection.Instance, projection.LocalID, projection.Peers[0])
	} else if args[0] == "publish" {
		err = d.ApplyUTCProjection(projection)
	} else {
		err = d.VerifyUTCProjection(projection, *boot, *pid, time.Now())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	state := "PUBLISHED"
	if args[0] == "verify" {
		state = "APPLIED"
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		State      string               `json:"state"`
		ConfigGen  uint64               `json:"config_gen"`
		Projection driver.UTCProjection `json:"projection"`
	}{state, projection.ConfigGen, projection})
	return 0
}
