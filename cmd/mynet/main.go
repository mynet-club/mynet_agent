package main

import (
	"os"

	mynet "github.com/mynet-club/mynet_agent-V2/mynet"
)

func main() {
	os.Exit(mynet.Run(os.Args[1:]))
}
