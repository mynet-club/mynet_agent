package utils

import "time"

// BaseResponse 通用 API 响应结构
type BaseResponse struct {
	Success   bool   `json:"success"`
	Code      int    `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// PaginationInfo 分页信息
type PaginationInfo struct {
	Total       int `json:"total"`
	PerPage     int `json:"per_page"`
	CurrentPage int `json:"current_page"`
	LastPage    int `json:"last_page"`
	From        int `json:"from"`
	To          int `json:"to"`
}

// NodeIdentifier 节点唯一标识
type NodeIdentifier struct {
	ZoneID int64 `json:"zone_id"`
	NodeID int64 `json:"node_id"`
}

// NodeStatus 节点状态快照
type NodeStatus struct {
	Status    int       `json:"status"`
	StatusStr string    `json:"status_str"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	IsActive  bool      `json:"is_active"`
	Version   string    `json:"version,omitempty"`
	PID       int       `json:"pid,omitempty"`
}

// ConfigPaths 已删（D4-E）：driver/ 安装目录层退役，路径真相走 paths.go 的函数 API。

// BuildInfo 构建信息
type BuildInfo struct {
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	GitCommit string `json:"git_commit"`
	GitBranch string `json:"git_branch"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}
