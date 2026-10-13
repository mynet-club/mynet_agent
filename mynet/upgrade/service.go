// Package upgrade — D-9 驱动域（列/装/升）。
//
// 与 CLI `mynet update` scope 互补：
//
//	mynet update     → ComponentMynet（agent 自身）
//	mynet driver …   → ComponentGNB / ComponentWireGuard（驱动）
//
// 共用一个实现，两个入口。
package upgrade

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
)

// Component 可升级组件
type Component string

const (
	ComponentMynet     Component = "mynet"
	ComponentGNB       Component = "gnb"
	ComponentWireGuard Component = "wireguard"
)

// Artifact 分发件元数据（来自 ctl 的 apps 索引）
type Artifact struct {
	Component Component `json:"component"`
	Version   string    `json:"version"`
	URL       string    `json:"url"`
	Checksum  string    `json:"checksum,omitempty"`
	Platform  string    `json:"platform"`
	// Ext 文件扩展（zip/tgz/exe/ipk…）；Kind = installer|archive（G4-D4-B）
	Ext  string `json:"ext,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// IsArchive 自更新归档（zip/tgz/tar.gz），不是装机安装包。
func (a Artifact) IsArchive() bool {
	e := strings.ToLower(a.Ext)
	return e == "zip" || e == "tgz" || e == "tar.gz"
}

// IsInstaller 装机包（exe/dmg/pkg/ipk）。
func (a Artifact) IsInstaller() bool {
	e := strings.ToLower(a.Ext)
	return e == "exe" || e == "dmg" || e == "pkg" || e == "ipk"
}

// Service 驱动域服务（与 CLI 共用）
type Service struct {
	// IndexURL 分发索引基址（E-9 SSOT：ctl.mynet.club）
	IndexURL string
	// Fetch 拉索引；测试可注入
	Fetch func(url string) ([]Artifact, error)
	// Install 落盘安装；测试可注入
	Install func(a Artifact) error
}

func NewService(indexURL string) *Service {
	s := &Service{IndexURL: indexURL}
	s.Fetch = func(url string) ([]Artifact, error) {
		c, err := newIndexClient(url, nil)
		if err != nil {
			return nil, err
		}
		return c.fetchAll()
	}
	s.Install = defaultInstall
	return s
}

// List 列出可装/可升驱动
func (s *Service) List() ([]Artifact, error) {
	all, err := s.Fetch(s.IndexURL)
	if err != nil {
		return nil, err
	}
	var drivers []Artifact
	for _, a := range all {
		if a.Component == ComponentGNB || a.Component == ComponentWireGuard {
			drivers = append(drivers, a)
		}
	}
	sort.Slice(drivers, func(i, j int) bool {
		if drivers[i].Component == drivers[j].Component {
			// 同组件按版本降序：必须用语义比较，字典序会把 0.10.0 排在 0.9.0 之后。
			return CompareVersions(drivers[i].Version, drivers[j].Version) > 0
		}
		return drivers[i].Component < drivers[j].Component
	})
	return drivers, nil
}

// Install 安装指定驱动
func (s *Service) InstallDriver(comp Component) error {
	if comp != ComponentGNB && comp != ComponentWireGuard {
		return fmt.Errorf("driver install 仅接受 gnb|wireguard，得到 %q（agent 自更新走 mynet update）", comp)
	}
	all, err := s.Fetch(s.IndexURL)
	if err != nil {
		return err
	}
	host := HostPlatform()
	var candidates []string
	for _, a := range all {
		if a.Component != comp {
			continue
		}
		// 平台匹配：空 = 通用件（历史索引可不带平台），精确命中 = 本机平台。
		// 不匹配不许装 —— 曾经「取组件第一个」会把 openwrt/mips 件装到 amd64 上。
		if a.Platform == "" || a.Platform == host {
			return s.Install(a)
		}
		candidates = append(candidates, a.Version+" ("+a.Platform+")")
	}
	if len(candidates) == 0 {
		return fmt.Errorf("索引中无组件 %s", comp)
	}
	return fmt.Errorf("索引中无 %s 的 %s 平台件；候选：%s", comp, host, strings.Join(candidates, ", "))
}

// Upgrade 升级驱动到索引中最新版
func (s *Service) Upgrade(comp Component) error {
	return s.InstallDriver(comp)
}

// HostPlatform 本机平台串（与 ctl 出包命名一致）：
// openwrt/amd64、linux/arm64、darwin/arm64、windows/amd64…
//
// OpenWrt 判定 = linux + /etc/openwrt_release；arch 归一到出包侧命名
// （mipsle→mipsel、mips64le→mips64el、arm→armv7）。
// softfp 变体（mips-softfp 等）无法从 uname 直接区分：精确不到就不装、
// 列候选指因（宁可不装，不装错）。
func HostPlatform() string {
	osName := runtime.GOOS
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/etc/openwrt_release"); err == nil {
			osName = "openwrt"
		}
	}
	return osName + "/" + indexArch(runtime.GOARCH)
}

func indexArch(goarch string) string {
	switch goarch {
	case "mipsle":
		return "mipsel"
	case "mips64le":
		return "mips64el"
	case "arm":
		return "armv7"
	default:
		return goarch
	}
}
