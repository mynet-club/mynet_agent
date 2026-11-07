package mynet

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc"
)

// cmdUTCCoordinate is the persistent local coordination entry. It runs in the
// executor, not in mynetd: no new daemon, no remote arbitrary binary/trust
// flags, and the management channel never depends on the pair being changed.
func cmdUTCCoordinate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mynet utc coordinate run --install FILE --trust FILE --plan FILE\n"+
			"       mynet utc coordinate resume --install FILE --trust FILE --scope S --pair N\n"+
			"       mynet utc coordinate status --install FILE --scope S --pair N")
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("utc coordinate "+action, flag.ContinueOnError)
	install := flags.String("install", "", "installed coordinator execution descriptor")
	trust := flags.String("trust", "", "installed static trust configuration")
	planFile := flags.String("plan", "", "pair plan JSON")
	planJSON := flags.String("plan-json", "", "pair plan JSON content (written by the trusted caller)")
	scope := flags.String("scope", "", "UTC scope")
	pair := flags.Uint64("pair", 0, "UTC pair id")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return 2
	}
	// Trust roots and execution descriptors are local installation state. When
	// flags are omitted they resolve to the installed UTC directory; a remote
	// request can never substitute its own binary or trust root.
	if *install == "" {
		*install = defaultUTCInstallPath()
	}
	if *trust == "" {
		*trust = defaultUTCTrustPath()
	}
	if action != "run" && action != "resume" && action != "status" {
		return 2
	}
	store, nodes, err := utc.LoadCoordinatorInstall(*install)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch action {
	case "status":
		if *planFile != "" || *planJSON != "" || *scope == "" || *pair == 0 {
			return 2
		}
		record, err := utc.CoordinateStatus(store, *scope, *pair)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if record == nil {
			fmt.Fprintln(os.Stderr, "no durable operation for this pair")
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(record)
		return 0
	case "run", "resume":
		if action == "run" && ((*planFile == "" && *planJSON == "") ||
			(*planFile != "" && *planJSON != "") || *scope != "" || *pair != 0) {
			return 2
		}
		if action == "resume" && (*planFile != "" || *planJSON != "" || *scope == "" || *pair == 0) {
			return 2
		}
		authority, err := utc.LoadStaticAuthority(*trust)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		var record utc.Record
		if action == "run" {
			var plan utc.Plan
			if *planJSON != "" {
				plan, err = utc.ParsePlan([]byte(*planJSON))
			} else {
				plan, err = utc.LoadPlan(*planFile)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			record, err = utc.RunPlan(ctx, store, nodes, authority, plan)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		} else {
			record, err = utc.ResumePair(ctx, store, nodes, authority, *scope, *pair)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(record)
		if err != nil {
			return 1
		}
		if record.Phase != "EFFECTIVE" && record.Phase != "BLOCKED" {
			return 1
		}
		return 0
	default:
		return 2
	}
}
