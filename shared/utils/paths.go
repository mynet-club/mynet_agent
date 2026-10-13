package utils

// 跨平台路径 —— 配置树 v2+（conf / plugins / var / state；D4-E 起 driver/ 安装层退役）。
//
//	env 覆盖 > MYNET_HOME > 平台默认
//
// 开发期不留 legacy ~/.mynet。旧扁平 conf/mynet.conf 不在本布局内。
// 节点唯一存放位：<conf>/<form>/<nodeId>/（见下方 D4-E 段）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	envHome       = "MYNET_HOME"
	envConfigHome = "MYNET_CONFIG_HOME"
	envDataHome   = "MYNET_DATA_HOME"
	envLogDir     = "MYNET_LOG_DIR"
)

// GetMyNetHome 安装/数据根目录。
// 优先级: MYNET_HOME > 可执行文件上推（…/bin → …）> 当前目录
//
// ⚠️ 仅供探测/迁移工具使用。**装服务、起进程、写配置**必须走 RequireMyNetHome
// —— 无 env 时静默回退 exe/cwd 会写进错误的树（evidence/20260930-subject）。
func GetMyNetHome() string {
	if mynetHome := os.Getenv(envHome); mynetHome != "" {
		return mynetHome
	}
	if exePath, err := os.Executable(); err == nil {
		if realPath, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = realPath
		}
		exeDir := filepath.Dir(exePath)
		if filepath.Base(exeDir) == "bin" {
			return filepath.Dir(exeDir)
		}
		return exeDir
	}
	return "."
}

// ErrHomeUnset MYNET_HOME 未设置时的错误（装服务/起进程/写配置拒绝）。
var ErrHomeUnset = fmt.Errorf("MYNET_HOME 未设置 —— 禁止静默回退 exe/cwd（会写进错误的配置树）；" +
	"请显式 export MYNET_HOME=... 或用 mynet install 写入服务单元")

// RequireMyNetHome 要求 MYNET_HOME 已设置。
// install / service / apply / claim 等**改变状态**的入口必须走这里。
func RequireMyNetHome() (string, error) {
	h := os.Getenv(envHome)
	if h == "" {
		return "", ErrHomeUnset
	}
	if !filepath.IsAbs(h) {
		return "", fmt.Errorf("MYNET_HOME 必须是绝对路径，得到 %q", h)
	}
	return h, nil
}

// HomeSource 返回 home 的来源：env | exe | cwd（诊断用）。
//
// 判据与 GetMyNetHome 的解析链一一对应（C1-S3 修）：
// env 非空 → "env"；能取到可执行路径 → "exe"（无论 bin 上推还是 exeDir，
// 两者都由 exe 位置推出）；取不到 → "cwd"（GetMyNetHome 回退 "."）。
// 此前是 `if base=="bin" { return "exe" }; return "exe"` —— 两分支同值的
// 退化判据，且 "cwd" 永远打不出来。
func HomeSource() string {
	exePath, exeErr := os.Executable()
	return homeSourceFrom(os.Getenv(envHome), exePath, exeErr)
}

// homeSourceFrom 纯判据（可注入 exe 结果，便于测全三态）。
func homeSourceFrom(envVal string, exePath string, exeErr error) string {
	if envVal != "" {
		return "env"
	}
	if exeErr == nil && exePath != "" {
		return "exe"
	}
	return "cwd"
}

// ── v2 配置树 ─────────────────────────────────────────────────

// confDir 只算路径，**不建目录**（认 MYNET_CONFIG_HOME）。
//
// ⚠️ 与 ConfDir 分开是有意的：**取路径不该有副作用**。`mynet install --dry-run`
// 断言"什么都不碰"，而 ModeFilePath/GatewaysFile 这类纯取值一旦顺手建目录就会破坏它
// （2026-10-12 实测被 TestInstallDryRunDoesNotTouchFilesystem 抓到）。
func confDir() string {
	if v := os.Getenv(envConfigHome); v != "" {
		return v
	}
	return filepath.Join(GetMyNetHome(), "conf")
}

// ConfDir $HOME/conf —— 真相源（意图 + 身份 + 插件配置）。**会建目录**（写入方用）。
func ConfDir() string {
	d := confDir()
	_ = SafeCreateDir(d, 0o700)
	return d
}

// LocalConfDir 本地模式配置根。
func LocalConfDir() string {
	d := filepath.Join(ConfDir(), "local")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// OnlineConfDir 联网配置根（不含租户）。
func OnlineConfDir() string {
	d := filepath.Join(ConfDir(), "online")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// OnlineTenantDir 某租户目录。tenantID 非法时返回错误（防穿越）。
func OnlineTenantDir(tenantID string) (string, error) {
	if err := ValidateDirName(tenantID); err != nil {
		return "", err
	}
	d := filepath.Join(OnlineConfDir(), tenantID)
	_ = SafeCreateDir(d, 0o700)
	return d, nil
}

// LegacyKeysDir 兼容：旧布局 {space}/keys（迁移读回用，不再写入）。
func LegacyKeysDir(space string) string {
	return filepath.Join(space, "keys")
}

// LegacyNodeKeysDir 兼容：旧布局 {space}/nodes/{nodeID}/keys（读回用，不再写入）。
func LegacyNodeKeysDir(space, nodeID string) string {
	return filepath.Join(space, "nodes", nodeID, "keys")
}

// NodeDirHasIdentity 节点目录里有没有身份材料（任一形态）：
// gnb 的 security/*.private、旧 node.private、wg 的 wg.conf（PrivateKey 在内）。
func NodeDirHasIdentity(dir string) bool {
	if fileExists(filepath.Join(dir, "node.private")) {
		return true
	}
	if fileExists(filepath.Join(dir, "wg.conf")) {
		return true
	}
	ents, err := os.ReadDir(filepath.Join(dir, "security"))
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() && len(e.Name()) > 8 && e.Name()[len(e.Name())-8:] == ".private" {
			return true
		}
	}
	return false
}

// siblingLocal 由某配置来源目录推出同级的 `conf/local`：
//
//	<home>/conf/local          → 自身
//	<home>/conf/online/<td>    → <home>/conf/local
//
// 找不到 conf 祖先则返回 ""。不依赖 env，纯路径推导（便于测试）。
func siblingLocal(space string) string {
	if filepath.Base(space) == "local" {
		return space
	}
	for p := space; ; {
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		if filepath.Base(parent) == "conf" {
			return filepath.Join(parent, "local")
		}
		p = parent
	}
}

// ResolveKeysDir 解析节点身份的**读取**位置。
//
// D4-E 起密钥与驱动 conf 同居节点目录 <conf>/<form>/<nodeId>/（唯一存放位，
// gnb=security/ 布局、wg=wg.conf base64）。旧布局只作**读**回退 ——
// 写入一律 NodeRuntimeDir，存量由 `mynet config migrate-layout` 收口。
//
// 顺序：
//  1. <conf>/<form>/<nodeId>/（form 由 space 推）—— 新唯一存放位
//  2. {space}/nodes/{nodeID}/keys、{space}/keys 与同级 conf/local 的同构位
//     （跨档回退的理由不变：密钥是节点身份，不该随 local/online 切档"换目录"）
//  3. <home>/driver/gnb/conf/<id>/security/ —— 旧 gnb 投影（未迁移节点）
//
// 旧落点都没有时返回新存放位（作为默认目标）。
func ResolveKeysDir(space, nodeID string) string {
	form := NodeFormOfSpace(space)
	if nodeID != "" {
		if p, err := NodeRuntimePath(form, nodeID); err == nil && NodeDirHasIdentity(p) {
			return p
		}
	}
	candidates := []string{space}
	if local := siblingLocal(space); local != "" && filepath.Clean(local) != filepath.Clean(space) {
		candidates = append(candidates, local)
	}
	for _, sp := range candidates {
		if nodeID != "" {
			p := LegacyNodeKeysDir(sp, nodeID)
			if fileExists(filepath.Join(p, "node.private")) {
				return p
			}
		}
		if fileExists(filepath.Join(LegacyKeysDir(sp), "node.private")) {
			return LegacyKeysDir(sp)
		}
	}
	// 旧 gnb 投影 security/（nodekeys 认 <id>.private 文件名）
	if nodeID != "" {
		sec := filepath.Join(GetMyNetHome(), "driver", "gnb", "conf", nodeID, "security")
		if fileExists(filepath.Join(sec, nodeID+".private")) {
			return sec
		}
	}
	if nodeID != "" {
		if p, err := NodeRuntimePath(form, nodeID); err == nil {
			return p
		}
	}
	return LegacyKeysDir(space)
}

// PluginsConfDir 插件配置（base 为 LocalConfDir 或 OnlineTenantDir）。
func PluginsConfDir(base string) string {
	d := filepath.Join(base, "plugins")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// PluginsRoot 已安装插件包体。
func PluginsRoot() string {
	d := filepath.Join(GetMyNetHome(), "plugins")
	_ = SafeCreateDir(d, 0o755)
	return d
}

// PluginPackageDir plugins/{name} 包体目录。
func PluginPackageDir(name string) (string, error) {
	if err := ValidateDirName(name); err != nil {
		return "", err
	}
	d := filepath.Join(PluginsRoot(), name)
	return d, nil
}

// ── D4-E 节点唯一存放位（2026-10-13 裁定）────────────────────────
//
// <home>/conf/<form>/<nodeId>/ 为每节点**唯一存放位**：密钥 + 驱动 conf 同居一目录。
//
//	form = local | online —— 是**获取 key 的形式**（本地自产生 / 在线下发），
//	不是平行密钥面；nodeID 全局唯一（tenant 维度收进 mode.json / 节点 meta）。
//
// gnb 协议内布局**不动**，照旧写在该目录内：
// node.conf / route.conf / address.conf / security/<id>.{private,public}（128hex 无换行）/
// ed25519/*.public / gnb.map（gnb 自产、只读）。
// wg：wg.conf（驱动原生，PrivateKey=base64 44）同居该目录。
// 磁盘只存驱动原生密钥格式（gnb=128hex、wg=base64 44），hex↔base64 只留 DB/上报边界。

const (
	NodeFormLocal  = "local"
	NodeFormOnline = "online"
)

// ValidateNodeForm form 只认 local / online。
func ValidateNodeForm(form string) error {
	if form != NodeFormLocal && form != NodeFormOnline {
		return fmt.Errorf("非法 form %q（只认 local|online）", form)
	}
	return nil
}

// NodeRuntimeDir 节点唯一存放位：<conf>/<form>/<nodeID>（密钥 + 驱动 conf 同居）。
// 取路径**会建目录**（写入方用）；纯取值场景用 NodeRuntimePath。
func NodeRuntimeDir(form, nodeID string) (string, error) {
	p, err := NodeRuntimePath(form, nodeID)
	if err != nil {
		return "", err
	}
	_ = SafeCreateDir(p, 0o700)
	return p, nil
}

// NodeRuntimePath 同 NodeRuntimeDir 但**不建目录**（dry-run / 探测用）。
func NodeRuntimePath(form, nodeID string) (string, error) {
	if err := ValidateNodeForm(form); err != nil {
		return "", err
	}
	if err := ValidateDirName(nodeID); err != nil {
		return "", err
	}
	return filepath.Join(confDir(), form, nodeID), nil
}

// ActiveNodeInfo 从 mode.json 读当前 form 与 nodeID（不依赖 shared/config，
// utils 不能反向依赖 config）。未初始化时 form=local、nodeID=""。
// 旧位置（HOME 根）只读兼容；搬迁由 shared/config.MigrateLegacyModeFile 做。
func ActiveNodeInfo() (form, nodeID string) {
	form = NodeFormLocal
	for _, p := range []string{ModeFilePath(), filepath.Join(GetMyNetHome(), "mode.json")} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m struct {
			Mode   string `json:"mode"`
			Active struct {
				NodeID string `json:"node_id"`
			} `json:"active"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Mode == NodeFormOnline {
			form = NodeFormOnline
		}
		return form, m.Active.NodeID
	}
	return form, ""
}

// ActiveNodeRuntimeDir 当前激活节点的唯一存放位（form 由 mode.json 判）。
// 节点未定时落 conf/local/default（与旧 gnbConfDir 的 default 兜底同语义）。
func ActiveNodeRuntimeDir() (string, error) {
	form, nodeID := ActiveNodeInfo()
	if nodeID == "" {
		nodeID = "default"
	}
	return NodeRuntimeDir(form, nodeID)
}

// NodeFormOfSpace 由旧 space 路径（conf/local 或 conf/online/<tenant>）推 form。
// 认不出时返回 local（未初始化约定）。
func NodeFormOfSpace(space string) string {
	for p := filepath.Clean(space); ; {
		base := filepath.Base(p)
		if base == NodeFormOnline {
			return NodeFormOnline
		}
		if base == NodeFormLocal {
			return NodeFormLocal
		}
		parent := filepath.Dir(p)
		if parent == p {
			return NodeFormLocal
		}
		p = parent
	}
}

// StateDir 激活态 / last-good。
func StateDir() string {
	d := filepath.Join(GetMyNetHome(), "state")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// ModeFilePath mode.json —— 在 `conf/` 下（2026-10-12 裁定：conf/ 是唯一可手改面，
// mode.json 属"身份"，从 HOME 根移进来）。
//
// ⚠️ 旧节点在 HOME 根有一份 legacy；`config.Store.LoadMode` 会做一次性迁移（见其实现）。
func ModeFilePath() string {
	return filepath.Join(confDir(), "mode.json")
}

// GatewaysFile 网关列表 —— `conf/gateways.json`（原 mynetd.env 的 MYNET_GATEWAYS 归这里）。
// 内容：JSON 字符串数组，与 MYNET_GATEWAYS 的逗号分隔等价。
func GatewaysFile() string {
	return filepath.Join(confDir(), "gateways.json")
}

// LastGoodDir Apply 成功快照。
func LastGoodDir() string {
	d := filepath.Join(StateDir(), "last-good")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// VarDir 运行态（可丢）。
func VarDir() string {
	if v := os.Getenv(envDataHome); v != "" {
		_ = SafeCreateDir(v, 0o700)
		return v
	}
	d := filepath.Join(GetMyNetHome(), "var")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// PluginVarDir var/plugins/{name}（pid、generated、logs）。
func PluginVarDir(name string) (string, error) {
	if err := ValidateDirName(name); err != nil {
		return "", err
	}
	d := filepath.Join(VarDir(), "plugins", name)
	_ = SafeCreateDir(d, 0o700)
	return d, nil
}

// TokenDir var/token。
func TokenDir() string {
	d := filepath.Join(VarDir(), "token")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// PIDDir var/pid。
func PIDDir() string {
	d := filepath.Join(VarDir(), "pid")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// LogsDir 日志。MYNET_LOG_DIR > $HOME/logs（兼容 unit 配置）。
func LogsDir() string {
	if v := os.Getenv(envLogDir); v != "" {
		_ = SafeCreateDir(v, 0o755)
		return v
	}
	d := filepath.Join(GetMyNetHome(), "logs")
	_ = SafeCreateDir(d, 0o755)
	return d
}

// BackupsDir export / pre-restore 包。
func BackupsDir() string {
	d := filepath.Join(GetMyNetHome(), "backups")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// SystemJSONPath var/system.json。
func SystemJSONPath() string {
	return filepath.Join(VarDir(), "system.json")
}

// ── 兼容别名（调用方少改）────────────────────────────────────
// ConfigDir / DataDir 已删：统一走 ConfDir / VarDir。

// NodesDir 元数据（var/nodes）。
func NodesDir() string {
	d := filepath.Join(VarDir(), "nodes")
	_ = SafeCreateDir(d, 0o700)
	return d
}

func fileExists(p string) bool {
	if fi, err := os.Stat(p); err == nil {
		return !fi.IsDir()
	}
	return false
}

// ValidateDirName 单层目录名：禁路径分隔与点段。
func ValidateDirName(name string) error {
	if name == "" {
		return fmt.Errorf("目录名为空")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("目录名非法: %q", name)
	}
	for _, r := range name {
		switch r {
		case '/', '\\', ':':
			return fmt.Errorf("目录名含非法字符 %q: %q", string(r), name)
		}
	}
	if filepath.Base(name) != name {
		return fmt.Errorf("目录名不是单层: %q", name)
	}
	return nil
}

// SafeCreateDir 创建目录（shared 不做 chown）。
func SafeCreateDir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// PlatformDefaultHome 平台默认 **home（Store Root）** —— 只作文档/诊断，
// 不是 install 的回退值：install 必须显式给 --home 或 MYNET_HOME（见 lifecycle-contract §2.1）。
//
// 注意与 PlatformDefaultPrefix（二进制安装前缀 /opt/mynet、C:\Program Files\MyNet）**不是一回事**：
// home 存的是节点身份/密钥/配置，prefix 放程序文件。历史上把两者混用导致 CLI 与安装器各写一棵树。
func PlatformDefaultHome() string {
	switch runtime.GOOS {
	case "darwin":
		return "/var/lib/mynet"
	case "windows":
		return `C:\ProgramData\MyNet`
	default:
		// OpenWrt 的 home 是 /etc/mynet，由 ipk/安装器显式指定；通用 Linux 用 /var/lib/mynet。
		return "/var/lib/mynet"
	}
}

// ── 安装前缀（D4-E：driver/ 安装目录层拿掉，二进制统一 <prefix>/bin）──

// PlatformDefaultPrefix 程序安装前缀的平台默认。装机显式 --prefix / MYNET_PREFIX 为权威，
// 这里只是默认值（与 mynet/prefix_*.go 的 defaultPrefix 同一判据）。
func PlatformDefaultPrefix() string {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/local/opt/mynet"
	case "windows":
		return `C:\Program Files\MyNet`
	default:
		if isOpenWrtSystem() {
			return "/usr" // OpenWrt：二进制由 ipk 装 /usr/sbin，系统里没有 /opt/mynet
		}
		return "/opt/mynet"
	}
}

// isOpenWrtSystem 运行期判定 OpenWrt（与 mynet.isOpenWrt 同判据）。
func isOpenWrtSystem() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	if _, err := os.Stat("/sbin/procd"); err == nil {
		if _, err := os.Stat("/etc/systemd/system"); err != nil {
			return true
		}
	}
	return false
}

// PrefixDir 本机程序前缀：MYNET_PREFIX > exe 反推（<prefix>/bin/exe → <prefix>）> 平台默认。
func PrefixDir() string {
	if v := os.Getenv("MYNET_PREFIX"); v != "" {
		return v
	}
	if exePath, err := os.Executable(); err == nil {
		if realPath, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = realPath
		}
		exeDir := filepath.Dir(exePath)
		if filepath.Base(exeDir) == "bin" {
			return filepath.Dir(exeDir)
		}
	}
	return PlatformDefaultPrefix()
}

// PrefixBinDir 二进制统一安装位 <prefix>/bin（gnb 四件套与 mynet/mynetd 同居）。
// 纯路径不建目录 —— 谁装谁建（upgrade 的 InstallWith 会 MkdirAll）。
// 旧位 <home>/driver/gnb/bin 只作**查找**回退（存量节点），不再往那装。
func PrefixBinDir() string {
	return filepath.Join(PrefixDir(), "bin")
}
