package mynet

import (
	"fmt"
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"os"
	"path/filepath"
)

func ensureNodeKeys(dir, vpn, nodeID, backPub string) error {
	priv, pub, algo, existingID, err := keys.LoadNodeKeys(dir)
	expected := "ed25519"
	if vpn == "wireguard" {
		expected = "x25519"
	} else if vpn != "gnb" {
		return fmt.Errorf("不支持的 VPN 类型 %q", vpn)
	}
	if err == nil {
		if priv == "" || pub == "" || algo != expected || (existingID != "" && existingID != nodeID) {
			return fmt.Errorf("已有密钥与设备身份不匹配，请在专家模式检查；未覆盖密钥")
		}
		derived, err := keys.DerivePublicKeyFromPrivateHex(priv, expected)
		if err != nil || derived != pub {
			return fmt.Errorf("已有密钥校验失败，请在专家模式检查；未覆盖密钥")
		}
		return nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	for _, entry := range entries {
		// A partial import is recoverable by its owner; never overwrite automatically.
		if filepath.Ext(entry.Name()) == ".private" || filepath.Ext(entry.Name()) == ".public" {
			return fmt.Errorf("已有密钥文件不完整，请在专家模式修复；未覆盖密钥")
		}
	}
	priv, pub, algo, err = keys.GenerateNodeKeys(vpn)
	if err != nil {
		return err
	}
	return keys.WriteNodeKeys(dir, priv, pub, algo, nodeID, backPub)
}
