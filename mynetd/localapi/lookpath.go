package localapi

import (
	"os"
	"os/exec"
	"path/filepath"
)

// lookPathAny 先走 PATH，失败后扫系统目录。
// mynetd 起来时 PATH 常是残的（/sbin 不在里面）。
func lookPathAny(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin", "/usr/local/bin"} {
		cand := filepath.Join(d, name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return ""
}
