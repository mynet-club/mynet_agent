package mynet

// 本机 API 端口（默认 8096）冲突检测 —— 安装/升级前必查。
//
// 三种结果（约定 2026-09-26）：
//
//	PortFree        空闲 → 正常装
//	PortOurs        是我们的旧版 mynetd → **走升级**，保留 conf/var
//	PortForeign     别的进程占用 → **拒绝安装**，不硬抢
//
// 判「是不是我们」只看一条：GET /api/v1/node-status 或 /api/v1/status
// 能认出 MyNet 的形状。认不出就当别人 —— 宁可误判 foreign 也别
// 把用户机器上的别的服务弄挂。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// PortOwner 端口归属。
type PortOwner int

const (
	PortFree PortOwner = iota
	PortOurs
	PortForeign
)

func (p PortOwner) String() string {
	switch p {
	case PortFree:
		return "free"
	case PortOurs:
		return "ours"
	case PortForeign:
		return "foreign"
	default:
		return "unknown"
	}
}

// DefaultLocalAPI 与 mynetd.DefaultLocalAPI 保持一致（SSOT 8096）。
const DefaultLocalAPI = "127.0.0.1:8096"

// DetectPortOwner 探测 host:port 归谁。addr 空则用 DefaultLocalAPI。
func DetectPortOwner(addr string) PortOwner {
	if addr == "" {
		addr = DefaultLocalAPI
	}
	// 先看 TCP 能不能连 —— 连不上就是空闲
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return PortFree
	}
	conn.Close()

	// 有人在听。问它是不是 MyNet
	if isMyNetAPI(addr) {
		return PortOurs
	}
	return PortForeign
}

// isMyNetAPI 认 MyNet 本机 API 的形状。
// 有响应且 JSON 里能对上我们的字段就算数；纯 TCP 服务 / 别的 Web 一律 foreign。
func isMyNetAPI(addr string) bool {
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	for _, path := range []string{"/api/v1/node-status", "/api/v1/status"} {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Origin", "http://"+addr)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		var obj map[string]any
		dec := json.NewDecoder(ioLimit(resp.Body, 64<<10))
		err = dec.Decode(&obj)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		if looksLikeOurs(obj) {
			return true
		}
		// 有的版本包在 data 里
		if data, ok := obj["data"].(map[string]any); ok && looksLikeOurs(data) {
			return true
		}
	}
	return false
}

// looksLikeOurs 有我们的特征字段就算。**不猜** —— 没特征就 false。
func looksLikeOurs(m map[string]any) bool {
	// node-status 的状态字面量
	for _, k := range []string{"state", "one_line", "peers", "tun_iface"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	// status 端点常见 shape
	if s, ok := m["success"].(bool); ok {
		if _, has := m["data"]; has && s {
			return true
		}
	}
	return false
}

// DescribePortOwner 给用户看的一句话 + 处置建议。
func DescribePortOwner(addr string) (PortOwner, string) {
	owner := DetectPortOwner(addr)
	switch owner {
	case PortFree:
		return owner, fmt.Sprintf("%s 空闲，可安装", addr)
	case PortOurs:
		return owner, fmt.Sprintf("%s 已有 MyNet 在跑 —— 将执行**升级**（保留 conf/var）", addr)
	default:
		return owner, fmt.Sprintf("%s 被其他进程占用，拒绝安装。请先停掉占用者或改用其他机器。", addr)
	}
}

// mustAcceptPort 安装前闸门。ours 可继续（升级）；foreign 必须停。
// 返回 error 时调用方不得继续安装。
func mustAcceptPort(addr string) error {
	owner, msg := DescribePortOwner(addr)
	switch owner {
	case PortForeign:
		return fmt.Errorf("%s", msg)
	case PortOurs:
		// 升级路径：调用方负责停服务再覆盖二进制
		fmt.Println("⚠", msg)
		return nil
	default:
		fmt.Println(" ", msg)
		return nil
	}
}

// UpgradePreserve 声明：升级时这些目录**永不覆盖**。
var UpgradePreserve = []string{"conf", "var/token", "var/pid"}

// IsUpgradeSafePath 覆盖二进制前判目标路径是否属于「可覆盖」区。
// bin/ 下的可执行文件可以盖；conf/ 与身份目录不行。
func IsUpgradeSafePath(rel string) bool {
	rel = strings.TrimPrefix(rel, "/")
	for _, keep := range UpgradePreserve {
		if rel == keep || strings.HasPrefix(rel, keep+"/") {
			return false
		}
	}
	return true
}

// ioLimit 读 body 时设上限，防恶意大响应把内存吃光。
type limitReader struct {
	r io.Reader
	n int64
}

func ioLimit(r io.Reader, n int64) *limitReader { return &limitReader{r: r, n: n} }

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("响应过大")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}
