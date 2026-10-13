package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Install 落盘安装：下载 → sha256 校验 → 解包 → 备份 → 原子替换 → 自检 → 失败回滚。
//
// 此前 NewService 返回的 Install 是个 `return fmt.Errorf("…未注入")` 的桩，
// 于是「升级」这件事从未真正发生过（R4-3 因此撤勾）。

const (
	downloadTimeout = 5 * time.Minute
	maxArtifactSize = 512 << 20 // 产物上限 512MB，防异常响应撑爆磁盘
	backupSuffix    = ".bak"
)

// binaryNamesFor 产物里会被安装的可执行文件（按组件取套件名）。
// webui.html 不在此列——它由 mynetd 的 embed 构建烘进去，不单独落盘替换。
//
// gnb 是**四件套同目录配套**（AGENTS §1.1 / paths.md R2）：缺件不得捡异构建，
// 所以 gnb 按整套装（suiteComplete），缺一个就拒装。
var (
	agentBinaryNames = []string{"mynet", "mynetd", "mynet_gateway"}
	gnbBinaryNames   = []string{"gnb", "gnb_ctl", "gnb_es", "gnb_crypto"}
	wgBinaryNames    = []string{"wg", "wg-quick"}
)

func binaryNamesFor(c Component) []string {
	switch c {
	case ComponentGNB:
		return gnbBinaryNames
	case ComponentWireGuard:
		return wgBinaryNames
	default:
		return agentBinaryNames
	}
}

// suiteComplete 该组件是否必须整套齐全才许装。
func suiteComplete(c Component) bool { return c == ComponentGNB }

// defaultInstall 用默认选项安装。
func defaultInstall(a Artifact) error {
	return InstallWith(a, InstallOptions{})
}

// InstallOptions 安装的可调项（测试注入用；零值即生产默认）。
type InstallOptions struct {
	// BinDir 二进制安装目录；空则取当前可执行文件所在目录。
	BinDir string
	// HTTPDoer 下载用；nil 则用带超时的默认 client。
	HTTPDoer HTTPDoer
	// HealthCheck 替换后自检；返回非 nil 即触发回滚。
	// 空则用默认实现（对装好的 mynet 跑 version）。
	HealthCheck func(binDir string) error
	// RemoveBackup 装完自检通过后删掉 .bak。
	// ⚠️ 语义是「删除」而非「保留」：bool 零值是 false，用 KeepBackup 命名会让
	// 默认行为（删备份）与字面意思（保留）相反 —— 首次写就踩了这个坑。
	RemoveBackup bool
}

// InstallWith 按给定选项安装一个产物。
//
// 顺序刻意如此：**先备份、再替换、自检失败即回滚**。任何一步失败都不允许留下
// 「装了一半」的状态——半装的节点比装不上的节点更难查。
func InstallWith(a Artifact, opts InstallOptions) error {
	if a.URL == "" {
		return fmt.Errorf("install: 产物 URL 为空")
	}
	if a.Checksum == "" {
		// 不静默降级：装一个无法校验的二进制等于放弃完整性保护。
		// ctl 上传时必算 sha256（ReleaseService.php:34），缺了说明索引/数据有问题。
		return fmt.Errorf("install: 产物缺少 sha256 校验和，拒绝安装（%s）", a.URL)
	}

	names := binaryNamesFor(a.Component)
	binDir := opts.BinDir
	if binDir == "" {
		if a.Component == ComponentGNB {
			// D4-E：driver/ 安装层退役，gnb 四件套与 agent 同装 <prefix>/bin。
			binDir = utils.PrefixBinDir()
		} else {
			var err error
			binDir, err = currentBinDir()
			if err != nil {
				return fmt.Errorf("install: 推断安装目录失败: %w", err)
			}
		}
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("install: 创建安装目录 %s: %w", binDir, err)
	}

	work, err := os.MkdirTemp("", "mynet-upgrade-")
	if err != nil {
		return fmt.Errorf("install: 建临时目录: %w", err)
	}
	defer os.RemoveAll(work)

	archive := filepath.Join(work, filepath.Base(a.URL))
	if err := download(a.URL, archive, opts.HTTPDoer); err != nil {
		return err
	}

	got, err := sha256File(archive)
	if err != nil {
		return fmt.Errorf("install: 计算校验和: %w", err)
	}
	want := normalizeChecksum(a.Checksum)
	if got != want {
		// 校验失败必须在解包**之前**拒绝——绝不执行未通过校验的内容。
		return fmt.Errorf("install: sha256 不匹配\n  期望 %s\n  实际 %s", want, got)
	}

	unpacked := filepath.Join(work, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return fmt.Errorf("install: 建解包目录: %w", err)
	}
	if err := unpack(archive, unpacked); err != nil {
		return fmt.Errorf("install: 解包: %w", err)
	}

	srcDir, found, err := locateBinaries(unpacked, names)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("install: 产物里没有 %s 中的任何一个", strings.Join(names, "/"))
	}
	if suiteComplete(a.Component) {
		var missing []string
		have := map[string]bool{}
		for _, n := range found {
			have[n] = true
		}
		for _, n := range names {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			// R2：gnb_ctl/gnb_es 必须与 gnb 同套件；缺件装半套 = tools_missing
			return fmt.Errorf("install: %s 套件缺件 %s（整套 %s 缺一不装）",
				a.Component, strings.Join(missing, "/"), strings.Join(names, "/"))
		}
	}

	// ── 备份 → 替换 → 自检 → 失败回滚 ──────────────────────────
	backups, err := backupBinaries(binDir, found)
	if err != nil {
		return fmt.Errorf("install: 备份旧二进制: %w", err)
	}

	if err := replaceBinaries(srcDir, binDir, found); err != nil {
		restoreErr := restoreBinaries(binDir, backups, names)
		if restoreErr != nil {
			return fmt.Errorf("install: 替换失败（%w），且回滚也失败（%v）—— 需人工介入", err, restoreErr)
		}
		return fmt.Errorf("install: 替换失败已回滚: %w", err)
	}

	check := opts.HealthCheck
	if check == nil {
		if a.Component == ComponentGNB {
			check = gnbHealthCheck
		} else {
			check = defaultHealthCheck
		}
	}
	if err := check(binDir); err != nil {
		restoreErr := restoreBinaries(binDir, backups, names)
		if restoreErr != nil {
			return fmt.Errorf("install: 自检失败（%w），且回滚也失败（%v）—— 需人工介入", err, restoreErr)
		}
		return fmt.Errorf("install: 自检失败已回滚: %w", err)
	}

	if opts.RemoveBackup {
		removeBackups(backups)
	}
	return nil
}

// currentBinDir 取当前可执行文件所在目录。
func currentBinDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

func download(url, dest string, hc HTTPDoer) error {
	if hc == nil {
		hc = &http.Client{Timeout: downloadTimeout}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("install: 构造下载请求: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("install: 下载 %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("install: 下载 %s 返回 %d", url, resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("install: 建临时文件: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(f, io.LimitReader(resp.Body, maxArtifactSize+1))
	if err != nil {
		return fmt.Errorf("install: 写入 %s: %w", dest, err)
	}
	if n > maxArtifactSize {
		return fmt.Errorf("install: 产物超过 %d 字节上限", maxArtifactSize)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unpack 按扩展名解包，统一铺到 destDir。
//
// 归档内可能有顶层目录（pack.sh 保留了与产物同名的顶层目录），
// 故解包后由 locateBinaries 递归找二进制，不假设固定层级。
func unpack(archive, destDir string) error {
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return unpackZip(archive, destDir)
	case strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar.gz"):
		return unpackTarGz(archive, destDir)
	default:
		return fmt.Errorf("不支持的归档格式: %s（只认 .tgz/.tar.gz/.zip）", filepath.Base(archive))
	}
}

func unpackTarGz(archive, destDir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// 防目录穿越：归档项不得逃出 destDir
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, maxArtifactSize)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func unpackZip(archive, destDir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, zf := range zr.File {
		target, err := safeJoin(destDir, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode()&0o777)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(rc, maxArtifactSize)); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// safeJoin 把归档项名安全地拼到 base 下，拒绝逃逸。
//
// **显式拒绝 `..` 分量**而不是靠 filepath.Clean 归一化掉：
// Clean("/../../escaped") 会得到 "/escaped"，穿越被「中和」成 base 内的路径 ——
// 逃不出去，但畸形/敌意归档会被静默接受，出问题时无从察觉。合法归档不含 `..`。
func safeJoin(base, name string) (string, error) {
	slash := filepath.ToSlash(name)
	for _, part := range strings.Split(slash, "/") {
		if part == ".." {
			return "", fmt.Errorf("归档项试图逃出解包目录（含 .. 分量）: %s", name)
		}
	}

	clean := strings.TrimPrefix(filepath.Clean(slash), "/")
	target := filepath.Join(base, clean)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("归档项试图逃出解包目录: %s", name)
	}
	return target, nil
}

// locateBinaries 在解包结果里递归找可执行文件，返回它们所在的**同一个**目录。
//
// 要求同目录：产物是「一整套二进制」，混装不同目录的版本会造成
// mynet 与 mynetd 版本不一致（共享内存布局/协议可能不兼容）。
func locateBinaries(root string, names []string) (string, []string, error) {
	byName := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		for _, want := range names {
			if base == want {
				if prev, dup := byName[want]; dup {
					return fmt.Errorf("产物里有多个 %s：%s 与 %s", want, prev, path)
				}
				byName[want] = path
			}
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	if len(byName) == 0 {
		return "", nil, nil
	}

	dir := filepath.Dir(byName[firstKey(byName)])
	for name, path := range byName {
		if filepath.Dir(path) != dir {
			return "", nil, fmt.Errorf("产物里 %s 与其他二进制不在同一目录（%s vs %s）",
				name, filepath.Dir(path), dir)
		}
	}

	found := make([]string, 0, len(byName))
	for _, n := range names {
		if _, ok := byName[n]; ok {
			found = append(found, n)
		}
	}
	return dir, found, nil
}

func firstKey(m map[string]string) string {
	best := ""
	for k := range m {
		if best == "" || k < best {
			best = k
		}
	}
	return best
}

// backupBinaries 把待替换的现有二进制改名为 <name>.bak。
// 返回 name → 备份路径（只含**确实存在**的旧文件）。
func backupBinaries(binDir string, names []string) (map[string]string, error) {
	backups := map[string]string{}
	for _, name := range names {
		cur := filepath.Join(binDir, name)
		if _, err := os.Stat(cur); err != nil {
			if os.IsNotExist(err) {
				continue // 首次安装，没有旧文件可备份
			}
			return nil, err
		}
		bak := cur + backupSuffix
		_ = os.Remove(bak)
		if err := os.Rename(cur, bak); err != nil {
			// 已改名的要回滚回去，不留半备份状态
			_ = restoreBinaries(binDir, backups, names)
			return nil, fmt.Errorf("备份 %s: %w", name, err)
		}
		backups[name] = bak
	}
	return backups, nil
}

// replaceBinaries 把新二进制原子地放到 binDir。
//
// 先写同目录临时文件再 rename：跨文件系统 rename 会失败，且 rename 是原子的，
// 避免出现「写了一半的可执行文件」。
func replaceBinaries(srcDir, binDir string, names []string) error {
	for _, name := range names {
		src := filepath.Join(srcDir, name)
		tmp := filepath.Join(binDir, "."+name+".new")

		in, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("打开新 %s: %w", name, err)
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			in.Close()
			return fmt.Errorf("写入 %s: %w", tmp, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			in.Close()
			os.Remove(tmp)
			return fmt.Errorf("复制 %s: %w", name, err)
		}
		out.Close()
		in.Close()

		if err := os.Chmod(tmp, 0o755); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("设置 %s 权限: %w", name, err)
		}
		if err := os.Rename(tmp, filepath.Join(binDir, name)); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("落位 %s: %w", name, err)
		}
	}
	return nil
}

// restoreBinaries 从备份恢复。缺失的备份项按「原本就不存在」处理（删掉新文件）。
func restoreBinaries(binDir string, backups map[string]string, names []string) error {
	var firstErr error
	for _, name := range names {
		bak, ok := backups[name]
		if !ok {
			// 原本没有这个二进制，回滚就是把它删掉
			if err := os.Remove(filepath.Join(binDir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Rename(bak, filepath.Join(binDir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func removeBackups(backups map[string]string) {
	for _, bak := range backups {
		_ = os.Remove(bak)
	}
}

// runVersionProbe 跑 `mynet version` 并取 stdout。带超时——自检不能挂死升级流程。
func runVersionProbe(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// defaultHealthCheck 自检：装好的 mynet 必须能跑起来并报出版本。
//
// 只检查 mynet 而非全部：它是三者里唯一「跑一次就退出」的，适合做探针；
// mynetd/mynet_gateway 是常驻进程，起停应由服务管理器负责，不在这里做。
func defaultHealthCheck(binDir string) error {
	bin := filepath.Join(binDir, "mynet")
	if _, err := os.Stat(bin); err != nil {
		if os.IsNotExist(err) {
			return nil // 本次没装 mynet，无从自检
		}
		return err
	}
	out, err := runVersionProbe(bin)
	if err != nil {
		return fmt.Errorf("自检失败（%s version）: %w", bin, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("自检失败：%s version 无输出", bin)
	}
	return nil
}

// gnbHealthCheck gnb 套件自检：gnb --version 能跑且带版本串。
// 只探 gnb 本体（四件套同目录已由完整性检查保证），不碰 gnb_ctl ——
// 它输出的是 ctl block，不适合当探针。
func gnbHealthCheck(binDir string) error {
	bin := filepath.Join(binDir, "gnb")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("自检失败（%s --version）: %w", bin, err)
	}
	if !strings.Contains(string(out), "GNB") {
		return fmt.Errorf("自检失败：%s --version 输出不含 GNB 版本串：%q", bin, out)
	}
	return nil
}
