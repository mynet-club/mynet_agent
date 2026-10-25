//go:build darwin

package main

import (
	"context"
	_ "embed"
	"os/exec"
	"strings"
	"time"
)

//go:embed panel_tab.js
var panelTabScript string

func reusePanelTab(target, key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", panelTabScript, target, key).Output()
	return err == nil && (strings.TrimSpace(string(out)) == "reused" || strings.TrimSpace(string(out)) == "created")
}
