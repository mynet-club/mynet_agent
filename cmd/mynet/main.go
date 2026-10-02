package main

import (
	"os"

	mynet "github.com/mynet-club/mynet_agent/mynet"
)

func main() {
	os.Exit(mynet.Run(os.Args[1:]))
}
