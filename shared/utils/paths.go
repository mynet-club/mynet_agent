package utils

// 跨平台路径 —— 配置树 v2（conf / plugins / driver / var / state 三分）。
//
//	env 覆盖 > MYNET_HOME > 平台默认
//
// 开发期不留 legacy ~/.mynet。旧扁平 conf/mynet.conf 不在本布局内。

import (
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
func HomeSource() string {
	if os.Getenv(envHome) != "" {
		return "env"
	}
	if exePath, err := os.Executable(); err == nil {
		if realPath, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = realPath
		}
		if filepath.Base(filepath.Dir(exePath)) == "bin" {
			return "exe"
		}
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

// NodeKeysDir 节点级密钥目录：{space}/nodes/{nodeID}/keys
// space 为 LocalConfDir() 或 OnlineTenantDir(t) —— 密钥**挂节点**，不挂租户。
func NodeKeysDir(space, nodeID string) (string, error) {
	if err := ValidateDirName(nodeID); err != nil {
		return "", err
	}
	d := filepath.Join(space, "nodes", nodeID, "keys")
	_ = SafeCreateDir(d, 0o700)
	return d, nil
}

// NodeSpaceDir 节点空间根：{space}/nodes/{nodeID}
func NodeSpaceDir(space, nodeID string) (string, error) {
	if err := ValidateDirName(nodeID); err != nil {
		return "", err
	}
	d := filepath.Join(space, "nodes", nodeID)
	_ = SafeCreateDir(d, 0o700)
	return d, nil
}

// LegacyKeysDir 兼容：旧布局 {space}/keys（迁移读回用，不再写入）。
func LegacyKeysDir(space string) string {
	return filepath.Join(space, "keys")
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

// ResolveKeysDir 解析节点密钥目录。
//
// 顺序：**当前来源 space** → **同级 conf/local**（历史/跨档落点）→ 新建 space 下新路径。
//
// 为什么跨档回退：密钥是**节点身份**，不该随 local/online 切档而"换目录" ——
// 切档后若只在当前档找，会误判「缺密钥/缺后端公钥」（实测 `keys sync` 因此
// 触发多余回填并因空 URL 失败）。宁可认同一台机只有一份密钥。
func ResolveKeysDir(space, nodeID string) string {
	candidates := []string{space}
	if local := siblingLocal(space); local != "" && filepath.Clean(local) != filepath.Clean(space) {
		candidates = append(candidates, local)
	}
	for _, sp := range candidates {
		if nodeID != "" {
			p := filepath.Join(sp, "nodes", nodeID, "keys")
			if fileExists(filepath.Join(p, "node.private")) {
				return p
			}
		}
		if fileExists(filepath.Join(LegacyKeysDir(sp), "node.private")) {
			return LegacyKeysDir(sp)
		}
	}
	if nodeID != "" {
		if p, err := NodeKeysDir(space, nodeID); err == nil {
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

// DriverDir 驱动投影根（driver/gnb、driver/wireguard）。
func DriverDir() string {
	d := filepath.Join(GetMyNetHome(), "driver")
	_ = SafeCreateDir(d, 0o755)
	return d
}

// GnbConfDir gnb 投影：driver/gnb/conf。
func GnbConfDir() string {
	d := filepath.Join(DriverDir(), "gnb", "conf")
	_ = SafeCreateDir(d, 0o755)
	return d
}

// GnbBinDir gnb 套件目录：driver/gnb/bin（四件套同目录配套，paths.md R2）。
// 纯路径不建目录 —— 谁装谁建（upgrade 的 InstallWith 会 MkdirAll）。
func GnbBinDir() string {
	return filepath.Join(GetMyNetHome(), "driver", "gnb", "bin")
}

// GnbNodeConfDir driver/gnb/conf/{nodeID}（AGENTS.md §1.1 布局不变）。
func GnbNodeConfDir(nodeID string) (string, error) {
	if err := ValidateDirName(nodeID); err != nil {
		return "", err
	}
	d := filepath.Join(GnbConfDir(), nodeID)
	_ = SafeCreateDir(d, 0o700)
	return d, nil
}

// WireGuardNodeDir driver/wireguard/{nodeID}。
func WireGuardNodeDir(nodeID string) (string, error) {
	if err := ValidateDirName(nodeID); err != nil {
		return "", err
	}
	d := filepath.Join(DriverDir(), "wireguard", nodeID)
	_ = SafeCreateDir(d, 0o700)
	return d, nil
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
// 注意与 defaultPrefix（二进制安装前缀 /opt/mynet、C:\Program Files\MyNet）**不是一回事**：
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
