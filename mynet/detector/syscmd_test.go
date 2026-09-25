package detector

import (
	"os"
	"path/filepath"
	"testing"
)

// daemon 的 PATH 里没有 /sbin 时，route/ifconfig 仍要找得到。
// 这是 apply、node-status、detector 三处栽过的同一坑。
func TestLookPathSysFindsSbin(t *testing.T) {
	old := os.Getenv("PATH")
	os.Setenv("PATH", "/usr/bin:/bin")
	defer os.Setenv("PATH", old)

	p := lookPathSys("route")
	if p == "" {
		if _, err := os.Stat("/sbin/route"); err != nil {
			t.Skip("本机没有 /sbin/route")
		}
		t.Fatal("lookPathSys(route) 应扫到 /sbin/route")
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("got %q want absolute path", p)
	}
}

func TestSysExecResolvesBeforeRun(t *testing.T) {
	old := os.Getenv("PATH")
	os.Setenv("PATH", "/usr/bin:/bin")
	defer os.Setenv("PATH", old)

	cmd := sysExec("route", "-n", "get", "default")
	if cmd.Path == "" || cmd.Path == "route" {
		if _, err := os.Stat("/sbin/route"); err != nil {
			t.Skip("本机没有 /sbin/route")
		}
		t.Fatalf("sysExec 未解析绝对路径: Path=%q", cmd.Path)
	}
}
