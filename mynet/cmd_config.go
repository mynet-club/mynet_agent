package mynet

// mynet config export | import —— 节点身份与配置的备份 / 恢复。
//
// 导出物：tar.gz，内含 conf/、var/ 里**身份相关**的内容，
// **不含**日志、pid、缓存 —— 备份是「这台节点是谁」，不是「它跑成什么样」。
//
//	mynet config export [-o FILE]
//	mynet config import FILE [--dry-run]
//
// 恢复前自动把现有 conf/var 打成 pre-restore-*.bak.tar.gz，
// 导入失败/半截不会把原配置弄丢。

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// backupManifest 导出包元数据。版本用来做兼容判断。
type backupManifest struct {
	Format   int               `json:"format"`
	App      string            `json:"app"`
	Version  string            `json:"version"`
	Exported time.Time         `json:"exported_at"`
	Hostname string            `json:"hostname"`
	Home     string            `json:"home"`
	Files    []string          `json:"files"`
	Extra    map[string]string `json:"extra,omitempty"`
}

const backupFormat = 1

// 相对 MYNET_HOME 的导出白名单（配置树 v2）。
// 真相源：conf/**（含 local/online/keys/plugins）+ mode.json。
// var/token 可选带；var/system.json 便于恢复后少跑一次 detector。
// driver/** 是 Apply 投影，**不进包**（导入后 mynet apply --active 重生）。
var backupPaths = []string{
	"mode.json",
	"conf",
	"var/token",
	"var/system.json",
}

// 任何路径段命中这些就跳过（防御：conf 下若有人塞了日志）
var backupSkip = []string{
	"var/logs", "var/pid", "pkg/cache", "state", "backups",
}

func cmdConfig(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet config export | import | use | status | tenants | keys | plugins | pull | sync | parity")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "配置同步语义（U2）:")
		fmt.Fprintln(os.Stderr, "  pull   从后端下载 bundle + 投影 driver/**（不装 OS 路由）")
		fmt.Fprintln(os.Stderr, "  sync   pull + apply --active --auto（下载+投影+OS 路由，推荐）")
		fmt.Fprintln(os.Stderr, "  refresh-*  只渲染**本地** bundle，不访问网络（要拉新先 pull）")
		return 2
	}
	switch args[0] {
	case "export":
		return cmdConfigExport(args[1:])
	case "import", "restore":
		return cmdConfigImport(args[1:])
	case "backups":
		return cmdConfigBackups(args[1:])
	case "parity":
		return configFaceParity()
	case "use":
		// 人用文本；带 --json 时给 localapi
		for _, a := range args[1:] {
			if a == "--json" {
				return configFaceUse(filterFlag(args[1:], "--json"))
			}
		}
		return cmdConfigUse(args[1:])
	case "status":
		return configFaceStatus()
	case "adopt":
		// A5：把旧 Lua 配置采纳进 Go 配置档（幂等、只读旧文件、fail-closed）
		dry := false
		for _, a := range args[1:] {
			if a == "--dry-run" {
				dry = true
			}
		}
		return configAdopt(dry)
	case "tenants":
		return configFaceTenants()
	case "keys":
		return configFaceKeys(args[1:])
	case "plugins":
		return configFacePlugins()
	case "identity":
		return configFaceIdentity()
	case "runtime":
		return configFaceRuntime()
	case "pull":
		for _, arg := range args[1:] {
			if arg == "--download-only" {
				return configFacePullOnly(true)
			}
		}
		return configFacePull()
	case "sync":
		// U2：pull（下载+投影）再装 OS 路由 —— 一条龙
		if rc := configFacePull(); rc != 0 {
			return rc
		}
		fmt.Println("→ 继续 apply --auto（OS 路由/防火墙）")
		rc := cmdApply([]string{"--auto"})
		// N6：parity 必查 —— critical 不一致（zip/crypto/passcode）高声告警
		if items := currentParity(); len(items) > 0 {
			critical := 0
			for _, it := range items {
				if it.Critical && !it.Match {
					critical++
					fmt.Fprintf(os.Stderr, "⚠ parity critical 不一致: %s local=%s remote=%s\n",
						it.Key, it.Local, it.Remote)
				}
			}
			if critical > 0 {
				fmt.Fprintf(os.Stderr, "⚠ %d 个 critical 对等项不一致 —— 大包/TCP 可能静默丢（见 troubleshoot §5.3）\n", critical)
			}
		}
		return rc
	case "cloud":
		return configFaceCloud()
	default:
		fmt.Fprintf(os.Stderr, "mynet config %s: 未知子命令\n", args[0])
		return 1
	}
}

func filterFlag(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

// cmdConfigUse mynet config use local | online --tenant T
func cmdConfigUse(args []string) int {
	store := &config.Store{}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet config use local | online --tenant T [--node ID]")
		return 2
	}
	switch args[0] {
	case "local":
		m, err := store.UseLocal()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("✓ mode=%s source=%s\n", m.Mode, m.Active.Source)
		return 0
	case "online":
		tenant, node := "", ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--tenant" && i+1 < len(args) {
				i++
				tenant = args[i]
			}
			if args[i] == "--node" && i+1 < len(args) {
				i++
				node = args[i]
			}
		}
		if tenant == "" {
			fmt.Fprintln(os.Stderr, "online 需要 --tenant")
			return 2
		}
		// node 默认取 profile
		if node == "" {
			if p, err := store.LoadOnlineProfile(tenant); err == nil {
				node = p.NodeID
			}
		}
		m, err := store.UseOnline(tenant, node)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("✓ mode=%s tenant=%s node=%s\n", m.Mode, m.Active.TenantID, m.Active.NodeID)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "用法: mynet config use local | online --tenant T\n")
		return 2
	}
}

// backupsDir 备份统一放 $MYNET_HOME/backups/，不再散落 cwd。
func backupsDir() string {
	return filepath.Join(utils.GetMyNetHome(), "backups")
}

func cmdConfigExport(args []string) int {
	out := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			i++
			out = args[i]
		}
	}
	home := utils.GetMyNetHome()
	if out == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "node"
		}
		_ = os.MkdirAll(backupsDir(), 0o700)
		out = filepath.Join(backupsDir(),
			fmt.Sprintf("mynet-config-%s-%s.tar.gz", host, time.Now().Format("20060102-150405")))
	}

	mf := backupManifest{
		Format:   backupFormat,
		App:      "mynet_agent",
		Version:  utils.AppVersion,
		Exported: time.Now().UTC(),
		Home:     home,
	}
	mf.Hostname, _ = os.Hostname()

	if err := writeBackup(out, home, &mf); err != nil {
		fmt.Fprintf(os.Stderr, "export 失败: %v\n", err)
		return 1
	}
	fmt.Printf("✓ 已导出 %s（%d 个文件）\n", out, len(mf.Files))
	fmt.Println("  含 conf/ 与 var/token；不含日志。妥善保存——里面有节点身份。")
	return 0
}

// cmdConfigBackups 列出备份包（时间 / 大小 / 文件数），供 UI 选择恢复，不再盲盒。
func cmdConfigBackups(args []string) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}
	type item struct {
		File    string `json:"file"`
		Name    string `json:"name"`
		ModTime string `json:"mod_time"`
		Size    int64  `json:"size"`
		Files   int    `json:"files,omitempty"`
	}
	files := listBackupFiles()
	out := make([]item, 0, len(files))
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		it := item{
			File:    f,
			Name:    filepath.Base(f),
			ModTime: st.ModTime().Format(time.RFC3339),
			Size:    st.Size(),
		}
		if mf, fl, err := readBackup(f); err == nil {
			it.Files = len(mf.Files)
			_ = fl
		}
		out = append(out, it)
	}
	if asJSON {
		return printJSON(out)
	}
	if len(out) == 0 {
		fmt.Println("(没有备份)")
		return 0
	}
	for _, it := range out {
		fmt.Printf("%s\t%s\t%dB\t%d files\n", it.ModTime, it.Name, it.Size, it.Files)
	}
	return 0
}

// listBackupFiles 按时间倒序列出可用备份包。
func listBackupFiles() []string {
	var files []string
	for _, dir := range []string{backupsDir(), utils.GetMyNetHome()} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") {
				continue
			}
			if strings.HasPrefix(name, "pre-restore-") {
				continue // 恢复前自动快照，不当作可选备份
			}
			p := filepath.Join(dir, name)
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			files = append(files, p)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		si, _ := os.Stat(files[i])
		sj, _ := os.Stat(files[j])
		if si == nil || sj == nil {
			return files[i] > files[j]
		}
		return si.ModTime().After(sj.ModTime())
	})
	return files
}

func cmdConfigImport(args []string) int {
	dry := false
	file := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		default:
			if file == "" && !strings.HasPrefix(args[i], "-") {
				file = args[i]
			}
		}
	}
	if file == "" {
		// 未指定文件：取 backups/ 下最新一份；没有就明确说没有，别甩 usage。
		list := listBackupFiles()
		if len(list) == 0 {
			fmt.Fprintln(os.Stderr, "没有找到备份包。先执行: mynet config export")
			fmt.Fprintf(os.Stderr, "  期望目录: %s\n", backupsDir())
			return 2
		}
		file = list[0]
		fmt.Fprintf(os.Stderr, "使用最新备份: %s\n", file)
		if len(list) > 1 {
			fmt.Fprintf(os.Stderr, "  （共 %d 份，指定文件名可恢复更早的）\n", len(list))
		}
	}

	home := utils.GetMyNetHome()
	mf, files, err := readBackup(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "import 失败: %v\n", err)
		return 1
	}
	if mf.Format != backupFormat {
		fmt.Fprintf(os.Stderr, "备份格式 %d 不支持（当前 %d）\n", mf.Format, backupFormat)
		return 1
	}
	fmt.Printf("备份：%s @ %s · %s · %d 文件\n", mf.App, mf.Version, mf.Exported.Format(time.RFC3339), len(mf.Files))

	if dry {
		for _, f := range files {
			fmt.Println("  ", f)
		}
		fmt.Println("--dry-run：未写入")
		return 0
	}

	// 先备份现状再覆盖 —— 导错包还能退回来
	if err := backupBeforeRestore(home); err != nil {
		fmt.Fprintf(os.Stderr, "无法备份现状，拒绝导入: %v\n", err)
		return 1
	}
	if err := restoreBackup(home, file); err != nil {
		fmt.Fprintf(os.Stderr, "导入失败: %v\n", err)
		return 1
	}
	fmt.Printf("✓ 已导入到 %s（真相源 conf/mode.json）\n", home)
	// 投影重放：driver/** 不信包里副本，一律 Apply 重生
	if err := ApplyFromActive(false); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 导入成功但 Apply 失败: %v\n", err)
		fmt.Println("  请检查 intent/bundle 后手动 mynet apply --active")
		return 1
	}
	fmt.Println("✓ driver 投影已重生")
	// 对等配置提醒：导入/恢复的包必须与对端 zip、crypto 等一致
	if local, _, err := LoadNodeConfKV(); err == nil {
		if items := CheckGNBParity(local, fetchRemoteParityOptions()); items != nil {
			var bad []string
			for _, it := range items {
				if !it.Match && it.Critical {
					bad = append(bad, it.Key+" local="+it.Local+" remote="+it.Remote)
				}
			}
			if len(bad) > 0 {
				fmt.Fprintln(os.Stderr, "⚠ 对等配置不一致（可能导致大包/TCP 不通）:")
				for _, x := range bad {
					fmt.Fprintln(os.Stderr, "  -", x)
				}
				fmt.Fprintln(os.Stderr, "  参考: mynet config parity")
			} else {
				fmt.Println("✓ 对等配置（zip/crypto 等）一致")
			}
		}
	}
	fmt.Println("  下一步：重启服务（systemctl restart mynetd / launchctl kickstart …）")
	return 0
}

// writeBackup 打 tar.gz。mf.Files 填进包内相对路径。
func writeBackup(out, home string, mf *backupManifest) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// manifest 最先写，读的时候好确认
	// 先收集文件清单再写 manifest（Files 字段要全）
	var entries []string
	for _, rel := range backupPaths {
		root := filepath.Join(home, rel)
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			relInHome, err := filepath.Rel(home, p)
			if err != nil {
				return nil
			}
			relInHome = filepath.ToSlash(relInHome)
			if skipPath(relInHome) {
				return nil
			}
			entries = append(entries, relInHome)
			return nil
		})
	}
	mf.Files = entries

	mb, _ := json.MarshalIndent(mf, "", "  ")
	if err := writeTarBytes(tw, "manifest.json", mb); err != nil {
		return err
	}
	for _, rel := range entries {
		if err := writeTarFile(tw, home, rel); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	return nil
}

func skipPath(rel string) bool {
	for _, s := range backupSkip {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	// 不导出备份自身，防递归
	base := filepath.Base(rel)
	if strings.HasPrefix(base, "pre-restore-") || strings.HasPrefix(base, "mynet-config-") {
		return true
	}
	return false
}

// readBackup 只读 manifest 与清单（导入前预检）。
func readBackup(file string) (backupManifest, []string, error) {
	var mf backupManifest
	zr, err := openTarGz(file)
	if err != nil {
		return mf, nil, err
	}
	defer zr.close()
	var files []string
	for {
		hdr, err := zr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return mf, nil, err
		}
		if hdr.Name == "manifest.json" {
			b, err := io.ReadAll(zr)
			if err != nil {
				return mf, nil, err
			}
			if err := json.Unmarshal(b, &mf); err != nil {
				return mf, nil, fmt.Errorf("manifest 解析: %w", err)
			}
			continue
		}
		if hdr.Typeflag == tar.TypeReg {
			files = append(files, hdr.Name)
		}
	}
	return mf, files, nil
}

// restoreBackup 把包内文件落到 home。只接受白名单前缀，防路径穿越。
func restoreBackup(home, file string) error {
	zr, err := openTarGz(file)
	if err != nil {
		return err
	}
	defer zr.close()
	for {
		hdr, err := zr.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(hdr.Name)
		if name == "manifest.json" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if strings.Contains(name, "..") {
			return fmt.Errorf("包内非法路径: %s", name)
		}
		if !allowedRestorePath(name) {
			return fmt.Errorf("拒绝写入包外路径: %s", name)
		}
		dst := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode)
		if mode == 0 {
			mode = 0o600
		}
		// 身份文件一律 0600，不沿用包里可能过宽的权限
		if strings.Contains(name, "token") || strings.Contains(name, "key") || strings.HasSuffix(name, ".conf") {
			mode = 0o600
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, zr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
}

func allowedRestorePath(rel string) bool {
	// 真相源白名单（配置树 v2）
	if rel == "mode.json" || rel == "var/system.json" {
		return true
	}
	if strings.HasPrefix(rel, "conf/") || rel == "conf" {
		return true // local/online/keys/plugins 都在 conf 下
	}
	if strings.HasPrefix(rel, "var/token/") || rel == "var/token" {
		return true
	}
	return false
}

// backupBeforeRestore 把现有 conf + var/token 打成 pre-restore-*.tar.gz。
func backupBeforeRestore(home string) error {
	// 没东西可保护就不写空包
	has := false
	for _, rel := range backupPaths {
		if _, err := os.Stat(filepath.Join(home, rel)); err == nil {
			has = true
			break
		}
	}
	if !has {
		return nil
	}
	name := filepath.Join(home, fmt.Sprintf("pre-restore-%s.tar.gz", time.Now().Format("20060102-150405")))
	mf := backupManifest{
		Format:   backupFormat,
		App:      "mynet_agent",
		Version:  utils.AppVersion,
		Exported: time.Now().UTC(),
		Home:     home,
		Extra:    map[string]string{"kind": "pre-restore"},
	}
	mf.Hostname, _ = os.Hostname()
	return writeBackup(name, home, &mf)
}

// ── tar.gz 薄封装 ────────────────────────────────────────────

type tarGzReader struct {
	f  *os.File
	gz *gzip.Reader
	tr *tar.Reader
}

func openTarGz(path string) (*tarGzReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("不是 gzip: %w", err)
	}
	return &tarGzReader{f: f, gz: gz, tr: tar.NewReader(gz)}, nil
}

func (z *tarGzReader) next() (*tar.Header, error) { return z.tr.Next() }
func (z *tarGzReader) Read(p []byte) (int, error) { return z.tr.Read(p) }
func (z *tarGzReader) close() {
	z.gz.Close()
	z.f.Close()
}

func writeTarBytes(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func writeTarFile(tw *tar.Writer, home, rel string) error {
	p := filepath.Join(home, filepath.FromSlash(rel))
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = rel
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(tw, in)
	return err
}
