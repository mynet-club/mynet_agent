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
	"sort"
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
	return &Service{
		IndexURL: indexURL,
		Fetch: func(url string) ([]Artifact, error) {
			return nil, fmt.Errorf("upgrade: Fetch 未注入（接 ctl apps 索引）")
		},
		Install: func(a Artifact) error {
			return fmt.Errorf("upgrade: Install 未注入: %s", a.Component)
		},
	}
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
			return drivers[i].Version > drivers[j].Version
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
	for _, a := range all {
		if a.Component == comp {
			return s.Install(a)
		}
	}
	return fmt.Errorf("索引中无组件 %s", comp)
}

// Upgrade 升级驱动到索引中最新版
func (s *Service) Upgrade(comp Component) error {
	return s.InstallDriver(comp)
}
