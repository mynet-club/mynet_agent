package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemdUnitsAreEmbeddedAndWellFormed 钉住内嵌单元的最小完备性。
//
// 单元一旦被改得缺了 ExecStart / [Service]，`mynet install` 会装出一个
// **装得上但起不来**的服务 —— 而 install 还会报成功。那种故障只在启动那一刻
// 才暴露，最难查。所以这里在写盘之前就拦。
func TestSystemdUnitsAreEmbeddedAndWellFormed(t *testing.T) {
	if err := assertUnitsEmbedded(); err != nil {
		t.Fatalf("内嵌单元不完备: %v", err)
	}
	for _, name := range SystemdUnitNames {
		body, ok := systemdUnit(name)
		if !ok {
			t.Fatalf("单元 %s 未嵌入", name)
		}
		if !strings.Contains(body, "[Install]") {
			t.Errorf("单元 %s 缺 [Install] 段 —— systemctl enable 会失败", name)
		}
		if !strings.Contains(body, "WantedBy=multi-user.target") {
			t.Errorf("单元 %s 缺 WantedBy=multi-user.target", name)
		}
	}

	// gnb 那个必须自己管好自己的存活：数据面挂了没人拉起来，节点就「进程都在、网络没了」
	gnb, _ := systemdUnit("mynet_gnb")
	if !strings.Contains(gnb, "Restart=always") {
		t.Error("mynet_gnb 必须 Restart=always —— 隧道挂了要自动拉起（CPI-64 的教训）")
	}
	// mynetd 是编排者，崩了一样要起来，否则重启后没人装路由（CPI-65）
	md, _ := systemdUnit("mynetd")
	if !strings.Contains(md, "Restart=") {
		t.Error("mynetd 必须配置 Restart=（否则重启后无人重装内核路由，CPI-65）")
	}
}

func TestUnitFileNameMapping(t *testing.T) {
	if got := unitFileName("mynetd"); got != "mynetd.service" {
		t.Fatalf("unitFileName(mynetd) = %q", got)
	}
	// 别名 gnb 要落到 mynet_gnb.service，否则 install/uninstall 找不到对方写的文件
	if got := unitFileName("gnb"); got != "mynet_gnb.service" {
		t.Fatalf("unitFileName(gnb) = %q，应与 mynet_gnb 同名", got)
	}
	if _, ok := systemdUnit("gnb"); !ok {
		t.Fatal("systemdUnit(\"gnb\") 应命中 gnb 的单元（别名）")
	}
	if _, ok := systemdUnit("wireguard"); ok {
		t.Fatal("wireguard 尚无单元，不该命中")
	}
}

// TestWriteSystemdUnitsSubstitutesGnbPath 钉住 gnb 路径替换。
//
// systemd 的 ExecStart **第一个 token 必须是绝对路径**，不能是 ${VAR} ——
// 所以路径必须在安装时写进单元。不替换的话，gnb 不在 /usr/local/bin 时
// 单元装得上、起不来，而错误只在启动那一刻才出现。
func TestWriteSystemdUnitsSubstitutesGnbPath(t *testing.T) {
	dir := t.TempDir()
	custom := "/opt/gnb/bin/gnb"
	if err := writeSystemdUnits(dir, custom); err != nil {
		t.Fatal(err)
	}

	for _, name := range SystemdUnitNames {
		p := filepath.Join(dir, unitFileName(name))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("未写出 %s: %v", p, err)
		}
		body := string(b)
		if name == "mynet_gnb" {
			if !strings.Contains(body, "ExecStart="+custom) {
				t.Errorf("mynet_gnb 的 ExecStart 未替换为 %s:\n%s", custom, body)
			}
			if strings.Contains(body, "/usr/local/bin/gnb") {
				t.Errorf("mynet_gnb 里还残留默认 gnb 路径:\n%s", body)
			}
		} else {
			if strings.Contains(body, custom) {
				t.Errorf("mynetd 单元不该被 gnb 路径替换影响:\n%s", body)
			}
		}
	}
}

func TestWriteEnvFileIs0600AndCarriesAllThreeVars(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "etc", "mynet", "mynetd.env")
	home := "/opt/mynet"
	gw := "wss://gw.example.com/v1/node/connect"
	binDir := "/opt/mynet/bin"

	if err := writeEnvFile(p, home, gw, binDir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// 它指向的 MYNET_HOME 里含节点私钥（conf/mynet.conf），必须 0600
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("env 权限 %v，必须 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	body := string(b)
	for _, want := range []string{"MYNET_HOME=" + home, "MYNET_GATEWAYS=" + gw, "MYNET_BIN=" + binDir + "/mynet"} {
		if !strings.Contains(body, want) {
			t.Errorf("env 缺 %q:\n%s", want, body)
		}
	}
}

// TestInstallRequiresGateways 节点模式必须知道 gateway 在哪 ——
// 不给的话 mynetd 起来是「纯本机模式」（无 WS、无节点身份、不碰驱动），
// 装了等于没装，而 install 还报成功。
func TestInstallRequiresGateways(t *testing.T) {
	if rc := cmdInstall([]string{"--dry-run", "--home", t.TempDir()}); rc != 2 {
		t.Fatalf("缺 --gateways 应退出码 2（用法错），实际 %d", rc)
	}
}

func TestInstallDryRunDoesNotTouchFilesystem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	prefix := filepath.Join(t.TempDir(), "prefix")

	rc := cmdInstall([]string{
		"--dry-run",
		"--prefix", prefix,
		"--home", home,
		"--gateways", "wss://gw.example.com/v1/node/connect",
	})
	if rc != 0 {
		t.Fatalf("dry-run 应退出码 0，实际 %d", rc)
	}
	for _, p := range []string{home, prefix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry-run 不该创建 %s（实际 err=%v）", p, err)
		}
	}
}

func TestUninstallDryRunDoesNotTouchFilesystem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	rc := cmdUninstall([]string{"--dry-run", "--home", home})
	if rc != 0 {
		t.Fatalf("dry-run 应退出码 0，实际 %d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, "conf")); err != nil {
		t.Fatalf("dry-run 不该动已存在的目录: %v", err)
	}
}

// launchd plist 的 XML 转义：路径/URL 里有 & 时不能把 plist 写坏。
func TestXMLesc(t *testing.T) {
	got := xmlEsc(`a&b<c>d"e`)
	want := "a&amp;b&lt;c&gt;d&quot;e"
	if got != want {
		t.Fatalf("xmlEsc=%q want %q", got, want)
	}
}

// plist 必须含 Label、绝对路径的 ProgramArguments、KeepAlive ——
// 缺任何一项都是「装得上起不来」。
func TestWriteLaunchdPlistShape(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"MYNET_HOME":     "/opt/mynet",
		"MYNET_GATEWAYS": "wss://gw.example/1",
		"MYNET_BIN":      "/opt/mynet/bin/mynet",
		"PATH":           "/usr/sbin:/usr/bin:/sbin:/bin",
	}
	err := writeLaunchdPlist("com.mynet.gnb", dir,
		[]string{"/usr/local/bin/gnb", "--systemd", "-c", "/opt/mynet/var/gnb"},
		env, "/var/log/mynet/gnb.log", "/var/log/mynet/gnb.err")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "com.mynet.gnb.plist"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"<string>com.mynet.gnb</string>",
		"<string>/usr/local/bin/gnb</string>",
		"<string>--systemd</string>", // 前台受监管，不自 daemon
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
		"<key>MYNET_HOME</key>",
		"<string>/opt/mynet</string>",
		"&amp;", // URL 里的 & 要转义 —— wait, wss://gw.example/1 无 &
	} {
		if want == "&amp;" {
			continue
		}
		if !strings.Contains(s, want) {
			t.Fatalf("plist 缺 %q:\n%s", want, s)
		}
	}
	// 含 & 的 gateways 必须被转义
	if err := writeLaunchdPlist("com.x", dir, []string{"/bin/true"},
		map[string]string{"MYNET_GATEWAYS": "wss://a?b=1&c=2"}, "/tmp/o", "/tmp/e"); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "com.x.plist"))
	if !strings.Contains(string(b2), "wss://a?b=1&amp;c=2") {
		t.Fatalf("URL 里的 & 未转义:\n%s", b2)
	}
}
