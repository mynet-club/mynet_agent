package mynet

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc"
)

func cmdUTCAuthority(args []string) int {
	if len(args) == 0 || (args[0] != "verify" && args[0] != "install-floor") {
		fmt.Fprintln(os.Stderr, "usage: mynet utc authority verify --trust FILE --plan FILE\n       mynet utc authority install-floor --trust FILE --scope S --key ID --revision N")
		return 2
	}
	flags := flag.NewFlagSet("utc authority "+args[0], flag.ContinueOnError)
	trust := flags.String("trust", "", "installed private trust configuration")
	planFile := flags.String("plan", "", "pair plan JSON")
	scope := flags.String("scope", "", "trusted UTC scope")
	key := flags.String("key", "", "installed signing key ID")
	revision := flags.Uint64("revision", 0, "independently obtained current revocation revision")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *trust == "" || flags.NArg() != 0 {
		return 2
	}
	authority, err := utc.LoadStaticAuthority(*trust)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if args[0] == "install-floor" {
		if *scope == "" || *key == "" || *revision == 0 || *planFile != "" {
			return 2
		}
		if err := authority.InstallRevocationFloor(ctx, *scope, *key, *revision); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(`{"state":"REVOCATION_FLOOR_INSTALLED"}`)
		return 0
	}
	if *planFile == "" || *scope != "" || *key != "" || *revision != 0 {
		return 2
	}
	file, err := os.Open(*planFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 131073))
	if err != nil || len(raw) > 131072 {
		fmt.Fprintln(os.Stderr, "UTC plan unreadable or too large")
		return 1
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan utc.Plan
	if err := decoder.Decode(&plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fmt.Fprintln(os.Stderr, "UTC plan has trailing data or exceeds capacity")
		return 1
	}
	if err := authority.Validate(ctx, plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		State      string `json:"state"`
		Scope      string `json:"scope"`
		PairID     uint64 `json:"pair_id"`
		Generation uint64 `json:"generation"`
		Manifest   string `json:"manifest"`
	}{"AUTHORIZED", plan.Scope, plan.PairID, plan.Generation, plan.Manifest})
	return 0
}
