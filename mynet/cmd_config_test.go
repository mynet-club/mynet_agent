package mynet

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 备份导出/导入：身份保留、日志不进包、导入可回滚。

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigExportImportRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"conf/mynet.conf":    "NODE_KEY=abc\n",
		"var/token/session":  "tok-123",
		"var/system.json":    `{"os":"linux"}`,
		"var/logs/huge.log":  strings.Repeat("x", 100),
		"var/pid/mynetd.pid": "1",
		"pkg/cache/junk.bin": "nope",
	})

	out := filepath.Join(t.TempDir(), "bak.tar.gz")
	// 直接调 writeBackup（cmdConfigExport 依赖 cwd）
	mf := backupManifest{Format: backupFormat, App: "mynet_agent", Version: "vtest"}
	if err := writeBackup(out, src, &mf); err != nil {
		t.Fatalf("export: %v", err)
	}

	// 读回清单
	mf2, files, err := readBackup(out)
	if err != nil {
		t.Fatal(err)
	}
	if mf2.Format != backupFormat {
		t.Fatalf("format=%d", mf2.Format)
	}
	joined := strings.Join(files, "\n")
	if !strings.Contains(joined, "conf/mynet.conf") {
		t.Fatalf("缺 conf: %v", files)
	}
	if !strings.Contains(joined, "var/token/session") {
		t.Fatalf("缺 token: %v", files)
	}
	if strings.Contains(joined, "huge.log") || strings.Contains(joined, "pid") || strings.Contains(joined, "cache") {
		t.Fatalf("日志/pid/cache 不该进包: %v", files)
	}

	// 导到空 home
	dst := t.TempDir()
	if err := restoreBackup(dst, out); err != nil {
		t.Fatalf("import: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "conf/mynet.conf"))
	if err != nil || string(b) != "NODE_KEY=abc\n" {
		t.Fatalf("恢复的 conf 不对: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(dst, "var/token/session"))
	if err != nil || string(b) != "tok-123" {
		t.Fatalf("恢复的 token 不对: %q %v", b, err)
	}
	// 权限：身份文件 0600
	st, _ := os.Stat(filepath.Join(dst, "var/token/session"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("token 权限 %v，应为 0600", st.Mode().Perm())
	}
}

func TestConfigImportMakesPreRestoreBackup(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"conf/mynet.conf": "NEW\n"})
	out := filepath.Join(t.TempDir(), "bak.tar.gz")
	mf := backupManifest{Format: backupFormat, App: "mynet_agent"}
	if err := writeBackup(out, src, &mf); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	writeTree(t, dst, map[string]string{"conf/mynet.conf": "OLD\n"})
	if err := backupBeforeRestore(dst); err != nil {
		t.Fatalf("pre-restore 失败: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dst, "pre-restore-*.tar.gz"))
	if len(matches) != 1 {
		t.Fatalf("应有 1 个 pre-restore 包，得到 %v", matches)
	}
	// 原配置还在（备份不删）
	old, _ := os.ReadFile(filepath.Join(dst, "conf/mynet.conf"))
	if string(old) != "OLD\n" {
		t.Fatalf("备份不应改动原配置: %q", old)
	}

	if err := restoreBackup(dst, out); err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(filepath.Join(dst, "conf/mynet.conf"))
	if string(now) != "NEW\n" {
		t.Fatalf("导入后应为 NEW: %q", now)
	}
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	// 手造一个含 ../ 的包
	p := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	_ = tw.WriteHeader(&tar.Header{Name: "conf/../../etc/evil", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	f.Close()

	dst := t.TempDir()
	if err := restoreBackup(dst, p); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
}

func TestRestoreRejectsOutOfWhitelist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("x")
	_ = tw.WriteHeader(&tar.Header{Name: "var/logs/x.log", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	f.Close()

	if err := restoreBackup(t.TempDir(), p); err == nil {
		t.Fatal("白名单外路径应被拒绝")
	}
}
