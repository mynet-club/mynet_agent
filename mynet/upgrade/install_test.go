package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTgz 造一个与 pack.sh 产物同构的归档：
//
//	mynet_agent_<...>/{mynet,mynetd,mynet_gateway}
func makeTgz(t *testing.T, topDir string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	write := func(name string, body string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range files {
		write(topDir+"/"+name, body, 0o755)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newZipWriter / writeZipEntry：造 zip 产物（windows 的扩展名是 zip）
func newZipWriter(w io.Writer) *zip.Writer { return zip.NewWriter(w) }

func writeZipEntry(t *testing.T, zw *zip.Writer, name, body string) {
	t.Helper()
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serveArtifact 把归档挂到 httptest，返回 artifact（带正确 sha256）。
func serveArtifact(t *testing.T, body []byte) (Artifact, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return Artifact{
		Component: ComponentMynet,
		Version:   "v0.2.0",
		URL:       srv.URL + "/mynet_agent_linux_amd64_v0.2.0.tgz",
		Checksum:  sha256Hex(body),
		Platform:  "linux/amd64",
	}, srv
}

// seedBinDir 造一个「已装旧版」的目录。
func seedBinDir(t *testing.T, contents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(b)
}

func TestInstallHappyPath(t *testing.T) {
	body := makeTgz(t, "mynet_agent_linux_amd64_v0.2.0", map[string]string{
		"mynet": "NEW-MYNET", "mynetd": "NEW-MYNETD", "mynet_gateway": "NEW-GW",
	})
	art, srv := serveArtifact(t, body)
	dir := seedBinDir(t, map[string]string{"mynet": "OLD", "mynetd": "OLD", "mynet_gateway": "OLD"})

	var checkedDir string
	err := InstallWith(art, InstallOptions{
		BinDir:   dir,
		HTTPDoer: srv.Client(),
		HealthCheck: func(binDir string) error {
			checkedDir = binDir
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	for name, want := range map[string]string{
		"mynet": "NEW-MYNET", "mynetd": "NEW-MYNETD", "mynet_gateway": "NEW-GW",
	} {
		if got := readFile(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if checkedDir != dir {
		t.Errorf("自检目录 = %q, want %q", checkedDir, dir)
	}
	// 备份保留（KeepBackup 默认 true）
	if got := readFile(t, filepath.Join(dir, "mynet"+backupSuffix)); got != "OLD" {
		t.Errorf("应保留旧版备份，实际 %q", got)
	}
	// 不留临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

// 校验和不匹配必须在**解包之前**拒绝，且一个文件都不许动。
func TestInstallRejectsChecksumMismatch(t *testing.T) {
	body := makeTgz(t, "top", map[string]string{"mynet": "NEW"})
	art, srv := serveArtifact(t, body)
	art.Checksum = strings.Repeat("0", 64) // 故意错
	dir := seedBinDir(t, map[string]string{"mynet": "OLD"})

	err := InstallWith(art, InstallOptions{
		BinDir:      dir,
		HTTPDoer:    srv.Client(),
		HealthCheck: func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("校验和不匹配应报错")
	}
	if !strings.Contains(err.Error(), "sha256 不匹配") {
		t.Errorf("错误应说明校验失败，实际: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "mynet")); got != "OLD" {
		t.Errorf("校验失败后原文件被改动了: %q", got)
	}
}

// 缺校验和必须拒绝——装一个无法校验的二进制等于放弃完整性保护。
func TestInstallRejectsMissingChecksum(t *testing.T) {
	body := makeTgz(t, "top", map[string]string{"mynet": "NEW"})
	art, srv := serveArtifact(t, body)
	art.Checksum = ""
	dir := seedBinDir(t, map[string]string{"mynet": "OLD"})

	err := InstallWith(art, InstallOptions{BinDir: dir, HTTPDoer: srv.Client()})
	if err == nil {
		t.Fatal("缺校验和应报错")
	}
	if !strings.Contains(err.Error(), "缺少 sha256") {
		t.Errorf("错误应说明缺校验和，实际: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "mynet")); got != "OLD" {
		t.Errorf("拒绝后原文件被改动了: %q", got)
	}
}

// 自检失败必须**回滚**——半装的节点比装不上的节点更难查。
func TestInstallRollsBackOnHealthCheckFailure(t *testing.T) {
	body := makeTgz(t, "top", map[string]string{
		"mynet": "NEW", "mynetd": "NEWD", "mynet_gateway": "NEWG",
	})
	art, srv := serveArtifact(t, body)
	dir := seedBinDir(t, map[string]string{"mynet": "OLD", "mynetd": "OLDD", "mynet_gateway": "OLDG"})

	err := InstallWith(art, InstallOptions{
		BinDir:   dir,
		HTTPDoer: srv.Client(),
		HealthCheck: func(string) error {
			return fmt.Errorf("模拟自检失败")
		},
	})
	if err == nil {
		t.Fatal("自检失败应报错")
	}
	if !strings.Contains(err.Error(), "已回滚") {
		t.Errorf("错误应说明已回滚，实际: %v", err)
	}

	// 三个文件都必须回到旧内容
	for name, want := range map[string]string{
		"mynet": "OLD", "mynetd": "OLDD", "mynet_gateway": "OLDG",
	} {
		if got := readFile(t, filepath.Join(dir, name)); got != want {
			t.Errorf("回滚后 %s = %q, want %q", name, got, want)
		}
	}
}

// 首次安装（没有旧二进制）：自检失败时回滚应把新文件删掉，而不是留下半装的。
func TestInstallRollbackOnFreshInstallRemovesNewFiles(t *testing.T) {
	body := makeTgz(t, "top", map[string]string{"mynet": "NEW", "mynetd": "NEWD"})
	art, srv := serveArtifact(t, body)
	dir := t.TempDir() // 空目录

	err := InstallWith(art, InstallOptions{
		BinDir:      dir,
		HTTPDoer:    srv.Client(),
		HealthCheck: func(string) error { return fmt.Errorf("boom") },
	})
	if err == nil {
		t.Fatal("应报错")
	}
	for _, name := range []string{"mynet", "mynetd"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Errorf("回滚后 %s 仍存在 —— 首次安装失败不该留下文件", name)
		}
	}
}

// 归档里的目录穿越必须被拒。
func TestInstallRejectsPathTraversalInArchive(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	evil := "../../escaped"
	_ = tw.WriteHeader(&tar.Header{Name: evil, Mode: 0o755, Size: 4, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("EVIL"))
	_ = tw.Close()
	_ = gz.Close()

	art, srv := serveArtifact(t, buf.Bytes())
	dir := t.TempDir()

	err := InstallWith(art, InstallOptions{
		BinDir: dir, HTTPDoer: srv.Client(),
		HealthCheck: func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("目录穿越应被拒")
	}
	if !strings.Contains(err.Error(), "逃出解包目录") {
		t.Errorf("错误应指出逃逸，实际: %v", err)
	}
}

// 归档里没有已知二进制 → 明确报错，而不是「装成功但什么都没装」。
func TestInstallRejectsArchiveWithoutBinaries(t *testing.T) {
	body := makeTgz(t, "top", map[string]string{"README.md": "nothing here"})
	art, srv := serveArtifact(t, body)
	dir := t.TempDir()

	err := InstallWith(art, InstallOptions{
		BinDir: dir, HTTPDoer: srv.Client(),
		HealthCheck: func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("没有二进制应报错")
	}
	if !strings.Contains(err.Error(), "没有") {
		t.Errorf("错误应说明缺二进制，实际: %v", err)
	}
}

// 二进制散在不同目录 → 拒绝。混装会造成 mynet 与 mynetd 版本不一致
// （共享内存布局/协议可能不兼容）。
func TestInstallRejectsBinariesInDifferentDirs(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"a/mynet", "b/mynetd"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	_ = gz.Close()

	art, srv := serveArtifact(t, buf.Bytes())
	dir := t.TempDir()

	err := InstallWith(art, InstallOptions{
		BinDir: dir, HTTPDoer: srv.Client(),
		HealthCheck: func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("散在不同目录应被拒")
	}
	if !strings.Contains(err.Error(), "同一目录") {
		t.Errorf("错误应指出目录不一致，实际: %v", err)
	}
}

// 下载失败（404）必须报错且不动现场。
func TestInstallSurfacesDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	art := Artifact{Component: ComponentMynet, URL: srv.URL + "/x.tgz", Checksum: strings.Repeat("a", 64)}
	dir := seedBinDir(t, map[string]string{"mynet": "OLD"})

	err := InstallWith(art, InstallOptions{BinDir: dir, HTTPDoer: srv.Client()})
	if err == nil {
		t.Fatal("404 应报错")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("错误应带状态码，实际: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "mynet")); got != "OLD" {
		t.Errorf("下载失败后原文件被改动了: %q", got)
	}
}

// zip 也要支持（windows 产物的扩展名是 zip）。
func TestInstallSupportsZip(t *testing.T) {
	var buf bytes.Buffer
	zw := newZipWriter(&buf)
	writeZipEntry(t, zw, "top/mynet", "NEW-ZIP")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	art, srv := serveArtifact(t, buf.Bytes())
	art.URL = srv.URL + "/mynet_agent_windows_amd64_v0.2.0.zip"
	dir := seedBinDir(t, map[string]string{"mynet": "OLD"})

	if err := InstallWith(art, InstallOptions{
		BinDir: dir, HTTPDoer: srv.Client(),
		HealthCheck: func(string) error { return nil },
	}); err != nil {
		t.Fatalf("zip 安装: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "mynet")); got != "NEW-ZIP" {
		t.Errorf("mynet = %q, want NEW-ZIP", got)
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	for _, evil := range []string{"../x", "../../etc/passwd", "a/../../x", "/../x"} {
		if got, err := safeJoin(base, evil); err == nil {
			t.Errorf("safeJoin(%q) 应被拒，却得到 %q", evil, got)
		}
	}
	for _, ok := range []string{"mynet", "top/mynet", "./top/mynet", "a/b/c"} {
		if _, err := safeJoin(base, ok); err != nil {
			t.Errorf("safeJoin(%q) 不应被拒: %v", ok, err)
		}
	}
}

// gnb 套件：四件整套装、缺件拒装（R2 同套件配套，缺件不捡异构建）。
func TestInstallGNBSuite(t *testing.T) {
	suite := map[string]string{
		"gnb":     "#!/bin/sh\necho GNB version Dev 1.6.8",
		"gnb_ctl": "ctl", "gnb_es": "es", "gnb_crypto": "crypto",
	}
	a, _ := serveArtifact(t, makeTgz(t, "gnb_linux_amd64", suite))
	a.Component = ComponentGNB
	binDir := t.TempDir()
	// 四件齐全 → 装成
	if err := InstallWith(a, InstallOptions{BinDir: binDir, RemoveBackup: true}); err != nil {
		t.Fatal(err)
	}
	for _, n := range gnbBinaryNames {
		if _, err := os.Stat(filepath.Join(binDir, n)); err != nil {
			t.Fatalf("缺 %s: %v", n, err)
		}
	}

	// 缺 gnb_ctl → 拒装（半套 = tools_missing）
	partial := map[string]string{"gnb": suite["gnb"], "gnb_es": "es", "gnb_crypto": "crypto"}
	a2, _ := serveArtifact(t, makeTgz(t, "gnb_linux_amd64", partial))
	a2.Component = ComponentGNB
	err := InstallWith(a2, InstallOptions{BinDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "gnb_ctl") {
		t.Fatalf("缺件必须点名 gnb_ctl 拒装: %v", err)
	}

	// agent 件里不该认出 gnb 名单（防组件串味）
	a3, _ := serveArtifact(t, makeTgz(t, "mynet_agent", map[string]string{"mynet": "x"}))
	a3.Component = ComponentGNB
	if err := InstallWith(a3, InstallOptions{BinDir: t.TempDir()}); err == nil {
		t.Fatal("agent 产物不得按 gnb 套件装")
	}
}
