package mynet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

func TestCLIHomeDefaultAndOverride(t *testing.T) {
	for _, override := range []string{"", filepath.Join(t.TempDir(), "not-created")} {
		t.Run(override, func(t *testing.T) {
			t.Setenv("MYNET_HOME", override)
			want := utils.PlatformDefaultHome()
			if override != "" {
				want = override
			}
			if got := confDir(); got != filepath.Join(want, "conf") {
				t.Fatalf("confDir=%q, want %q", got, filepath.Join(want, "conf"))
			}
			if got := defaultProbe().home; got != want {
				t.Fatalf("status home=%q, want %q", got, want)
			}
			// 未知子命令应进入 dispatch（2），而不是被旧的环境门禁挡住（1）。
			captureStderrFn(t, func() {
				if got := Run([]string{"unknown-command"}); got != 2 {
					t.Fatalf("dispatch rc=%d, want 2", got)
				}
			})
			if override != "" {
				if _, err := os.Stat(override); !os.IsNotExist(err) {
					t.Fatalf("read-only resolution created home: %v", err)
				}
			}
		})
	}
}

func TestCLIRejectsRelativeHomeBeforeDispatch(t *testing.T) {
	t.Setenv("MYNET_HOME", "relative-home")
	captureStderrFn(t, func() {
		if got := Run([]string{"unknown-command"}); got != 1 {
			t.Fatalf("relative home rc=%d, want 1", got)
		}
	})
}
