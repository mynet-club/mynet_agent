//go:build ignore

// Run after prepare-gui.sh: go run -tags embed scripts/check-embedded-ui.go
// Probes the compiled filesystem over HTTP; no installed daemon is contacted.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/mynetd/localapi"
)

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check() error {
	s := localapi.NewServer(localapi.Config{Listen: "127.0.0.1:0"})
	if err := s.Start(); err != nil {
		return err
	}
	defer s.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path string) ([]byte, error) {
		resp, err := client.Get("http://" + s.Addr() + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("%s: empty body", path)
		}
		fmt.Printf("%s: HTTP 200 (%d bytes)\n", path, len(body))
		return body, nil
	}
	if _, err := get("/ui/"); err != nil {
		return err
	}
	index, err := get("/gui/")
	if err != nil {
		return err
	}
	refs := regexp.MustCompile(`(?:src|href)="(/gui/assets/[^"\s]+)"`).FindAllSubmatch(index, -1)
	if len(refs) == 0 {
		return fmt.Errorf("GUI index contains no embedded entry assets")
	}
	for _, ref := range refs {
		body, err := get(string(ref[1]))
		if err != nil {
			return err
		}
		if strings.HasPrefix(strings.TrimSpace(string(body)), "<!DOCTYPE") {
			return fmt.Errorf("%s: SPA fallback instead of asset", ref[1])
		}
	}
	return nil
}
