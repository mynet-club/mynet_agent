// Package creds —— 会话凭证 0600 落盘 + 设备指纹（G3 纯小包）。
//
// 从根 `mynet` 抽出的最小面：组合根（cmd/mynetd）与执行器可共用，
// 不引入 detector/tools/apply 等系统能力包。B2 允许本包被 mynetd 引用
// （密钥/凭证原语，无系统副作用）。
package creds

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Credential 用户会话（0600）。字段与 mynet.Credential 一致（wire 兼容）。
type Credential struct {
	UserID            string `json:"user_id"`
	UserEmail         string `json:"user_email"`
	Token             string `json:"token"`
	RefreshToken      string `json:"refresh_token"`
	MachineID         string `json:"machine_id,omitempty"`
	DeviceFingerprint string `json:"device_fingerprint,omitempty"`
	TenantID          string `json:"tenant_id,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	AuthExpiresAt     string `json:"auth_token_expires_at,omitempty"`
	RefreshExpiresAt  string `json:"refresh_token_expires_at,omitempty"`
}

func confDir() string {
	return filepath.Join(utils.GetMyNetHome(), "conf")
}

func credentialPath() string {
	return filepath.Join(confDir(), "credential.json")
}

// SaveCredential 0600：含用户 token。
func SaveCredential(c *Credential) error {
	if err := os.MkdirAll(confDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(credentialPath(), append(b, '\n'), 0o600)
}

// LoadCredential 读不到/解析失败返回 nil（按未登录处理）。
func LoadCredential() *Credential {
	b, err := os.ReadFile(credentialPath())
	if err != nil {
		return nil
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return &c
}

// DeviceFingerprint 稳定设备指纹（Token Binding）。同机稳定、异机不同。
func DeviceFingerprint() string {
	mid := readMachineID()
	unique := runtime.GOOS + "|" + runtime.GOARCH + "|" + mid
	var ns [16]byte
	copy(ns[:], []byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8})
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(unique))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func readMachineID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	p := filepath.Join(utils.GetMyNetHome(), "var", "machine-id")
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	var buf [16]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		h, _ := os.Hostname()
		return "fallback:" + h
	}
	s := hex.EncodeToString(buf[:])
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		_ = os.WriteFile(p, []byte(s+"\n"), 0o600)
	}
	return s
}

// NowISO 统一时间戳形状。
func NowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
