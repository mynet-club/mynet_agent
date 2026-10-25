package mynet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func firewallServiceHook(home string) string {
	// systemd interprets percent specifiers even inside quotes.
	path := strings.ReplaceAll(FirewallIncludePath(home), "%", "%%")
	return fmt.Sprintf("# mynet external firewall reload hook\n[Service]\nExecStartPost=/bin/sh %q\nExecReload=/bin/sh %q\n", path, path)
}

func ufwReplayHook(home, original string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return fmt.Sprintf(`#!/bin/sh
# mynet external firewall reload hook
if [ -x %s ]; then
  %s "$@" || exit $?
fi
case "$1" in
  start|reload|force-reload|restart) exec sh %s ;;
  *) exit 0 ;;
esac
`, quote(original), quote(original), quote(FirewallIncludePath(home)))
}

func installUfwReplayHook(home, path string) error {
	want := ufwReplayHook(home, path+".mynet-original")
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(body) == want {
		return nil
	}
	if _, err := os.Lstat(path + ".mynet-original"); err == nil {
		return fmt.Errorf("ufw hook changed while original backup exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if body != nil {
		if err := os.Rename(path, path+".mynet-original"); err != nil {
			return err
		}
	}
	if err := writeFirewallArtifact(path, []byte(want), 0755); err != nil {
		if body != nil {
			return errors.Join(err, os.Rename(path+".mynet-original", path))
		}
		return err
	}
	return nil
}

func firewalldWatchUnit(home, bin string) string {
	escape := func(s string) string { return strings.ReplaceAll(s, "%", "%%") }
	return fmt.Sprintf(`[Unit]
Description=Replay MyNet rules after external firewalld reload
After=dbus.service firewalld.service
[Service]
Environment=%q
ExecStart=%q firewall watch
Restart=on-failure
RestartSec=2
[Install]
WantedBy=multi-user.target
`, "MYNET_HOME="+escape(home), escape(bin))
}

func removeUfwReplayHook(home, path string) error {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(body) != ufwReplayHook(home, path+".mynet-original") {
		return nil
	}
	if _, err := os.Lstat(path + ".mynet-original"); err == nil {
		return os.Rename(path+".mynet-original", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Remove(path)
}
