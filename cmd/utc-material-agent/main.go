// utc-material-agent pulls short-lived UTC relay material from utc-materiald,
// installs it atomically and restarts the local utcd.
//
// All fetch/install/restart logic lives in mynet/utc/material so that the
// `mynet utc material` subcommand runs the exact same code. This program is a
// thin flag/process wrapper around it.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mynet-club/mynet_agent/mynet/utc/material"
)

func main() {
	path := flag.String("c", "", "config path")
	once := flag.Bool("once", false, "fetch once and exit")
	flag.Parse()
	if *path == "" {
		log.Fatalf("%s-c <config> is required", material.LogPrefix)
	}
	cfg, err := material.LoadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	a, err := material.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		// The daemon keeps its historical one-shot behaviour: a failed fetch is
		// reported but does not change the exit status.
		if err := a.RunOnce(ctx); err != nil {
			log.Printf("%sinitial sync failed: %v", material.LogPrefix, err)
		}
		return
	}
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("%s%v", material.LogPrefix, err)
	}
}
