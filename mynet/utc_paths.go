package mynet

import (
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Installed UTC coordination files. These are operator-managed local state and
// are never resolved from a command request.
func defaultUTCInstallPath() string {
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "install.json")
}

func defaultUTCTrustPath() string {
	return filepath.Join(utils.GetMyNetHome(), "conf", "utc", "trust.json")
}
