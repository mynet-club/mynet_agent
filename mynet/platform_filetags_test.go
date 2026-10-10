package mynet

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// H1 验收 fixture：平台安装器文件**编译期互斥**，且每平台恰好一个入口提供者。
//
// 用 `go list` 按 GOOS/GOARCH/tags 取非测试 GoFiles 集合（本机可跑，不需要目标机）。
// 只看安装器相关文件 —— 包内其它按 GOOS 分叉的文件（apply_*/metrics_*/dns_*）不在本条断言内。

var installerEntryFiles = []string{
	"cmd_install_systemd.go",     // linux && !openwrt
	"cmd_install_openwrt.go",     // linux && openwrt
	"cmd_install_launchd.go",     // darwin
	"cmd_install_win.go",         // windows
	"cmd_install_unsupported.go", // !linux && !darwin && !windows
}

func goListGoFiles(t *testing.T, goos, goarch, tags string) map[string]bool {
	t.Helper()

	args := []string{"list", "-f", `{{join .GoFiles "\n"}}`}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, ".")
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list (%s/%s tags=%q) 失败: %v\n%s", goos, goarch, tags, err, ee.Stderr)
		}
		t.Fatalf("go list 无法执行（需要 go 工具链）: %v", err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			set[line] = true
		}
	}

	return set
}

func TestInstallerFilesAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		goarch      string
		tags        string
		mustInclude []string
		mustExclude []string
	}{
		{
			name: "linux-systemd", goos: "linux", goarch: "amd64", tags: "",
			mustInclude: []string{"cmd_install_systemd.go"},
			mustExclude: []string{"cmd_install_openwrt.go", "cmd_install_launchd.go", "cmd_install_win.go", "cmd_install_unsupported.go"},
		},
		{
			name: "linux-openwrt", goos: "linux", goarch: "mipsle", tags: "openwrt",
			mustInclude: []string{"cmd_install_openwrt.go"},
			mustExclude: []string{"cmd_install_systemd.go", "cmd_install_launchd.go", "cmd_install_win.go", "cmd_install_unsupported.go"},
		},
		{
			name: "darwin", goos: "darwin", goarch: "arm64", tags: "",
			mustInclude: []string{"cmd_install_launchd.go"},
			mustExclude: []string{"cmd_install_systemd.go", "cmd_install_openwrt.go", "cmd_install_openwrt_only.go", "cmd_install_win.go", "cmd_install_unsupported.go"},
		},
		{
			name: "windows", goos: "windows", goarch: "amd64", tags: "",
			mustInclude: []string{"cmd_install_win.go"},
			mustExclude: []string{"cmd_install_systemd.go", "cmd_install_openwrt.go", "cmd_install_openwrt_only.go", "cmd_install_launchd.go", "cmd_install_unsupported.go"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := goListGoFiles(t, c.goos, c.goarch, c.tags)

			// 共享入口必须在每个平台都在
			if !files["cmd_install.go"] {
				t.Fatalf("共享入口 cmd_install.go 缺于 %s/%s tags=%q", c.goos, c.goarch, c.tags)
			}
			for _, f := range c.mustInclude {
				if !files[f] {
					t.Errorf("%s/%s tags=%q 应含 %s 而未含", c.goos, c.goarch, c.tags, f)
				}
			}
			for _, f := range c.mustExclude {
				if files[f] {
					t.Errorf("%s/%s tags=%q 不该含 %s（平台文件未互斥）", c.goos, c.goarch, c.tags, f)
				}
			}

			// 恰好一个入口提供者被编译（platformInstall/platformUninstall 不重复定义）
			n := 0
			for _, f := range installerEntryFiles {
				if files[f] {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s/%s tags=%q 编译进 %d 个平台入口文件，应恰好 1 个", c.goos, c.goarch, c.tags, n)
			}
		})
	}
}

// TestOpenWrtBuildUsesOpenWrtTag 锁住构建接线：OpenWrt 产物必须以 `-tags openwrt` 构建，
// 否则选不到 procd 后端（platformInstall 未定义/落到其他后端）。这是「标签引了但没接线」的防线。
func TestOpenWrtBuildUsesOpenWrtTag(t *testing.T) {
	src, err := os.ReadFile("../.github/workflows/build_openwrt.yml")
	if err != nil {
		t.Fatalf("读 build_openwrt.yml 失败: %v", err)
	}
	if !strings.Contains(string(src), "-tags openwrt") {
		t.Fatal("build_openwrt.yml 必须用 `-tags openwrt` 构建 OpenWrt 产物")
	}
}

// TestBuildWorkflowDoesNotFakeOpenWrt 锁住"桌面/服务器工作流不得伪装 OpenWrt"：
// build.yml 是 linux/darwin/windows 的发行工作流；OpenWrt 交付归 build_openwrt.yml 的
// `-tags openwrt` ipk。曾用 `openwrt: true` 把 linux 包做成"瘦 mynetd"，收严后那类包在
// OpenWrt 上会被拒装 —— 这里防止它回来。
func TestBuildWorkflowDoesNotFakeOpenWrt(t *testing.T) {
	src, err := os.ReadFile("../.github/workflows/build.yml")
	if err != nil {
		t.Fatalf("读 build.yml 失败: %v", err)
	}
	s := string(src)
	if strings.Contains(s, "openwrt: true") {
		t.Fatal("build.yml 不应再有 `openwrt: true` —— linux 包不得伪装 OpenWrt（归 build_openwrt.yml 的 ipk）")
	}
	if !strings.Contains(s, "-tags embed") {
		t.Fatal("build.yml 的 mynetd 必须 `-tags embed`（与 make dist 的桌面/服务器产物一致）")
	}
}

// TestDistMatrixHasOpenWrtVariant 锁住 dist 矩阵里的 openwrt 归档变体
// （platform=openwrt、GOOS=linux、带 -tags openwrt），产物名 mynet_agent_openwrt_<arch>_<ver>。
func TestDistMatrixHasOpenWrtVariant(t *testing.T) {
	src, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatalf("读 Makefile 失败: %v", err)
	}
	if !strings.Contains(string(src), "openwrt:linux:mipsle:mipsel:openwrt") {
		t.Fatal("Makefile DIST_MATRIX 必须含 openwrt 变体（openwrt:linux:mipsle:mipsel:openwrt）")
	}
}
