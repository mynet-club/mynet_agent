// 实验：私钥 → 公钥 可推导性（GNB ed25519 + WireGuard X25519）
// 用法: go run ./cmd/keyprove --gnb-priv HEX_OR_FILE --gnb-pub HEX_OR_FILE
//       [--wg-priv B64_OR_FILE --wg-pub B64_OR_FILE]
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

func readArgOrFile(v string) string {
	if v == "" {
		return ""
	}
	if b, err := os.ReadFile(v); err == nil {
		return strings.TrimSpace(string(b))
	}
	return strings.TrimSpace(v)
}

func main() {
	args := os.Args[1:]
	get := func(flag string) string {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}

	fmt.Println("=== 实验：私钥能否推导出公钥 ===")
	fmt.Println()

	gpriv := readArgOrFile(get("--gnb-priv"))
	gpub := readArgOrFile(get("--gnb-pub"))
	if gpriv != "" {
		derived, err := keys.GNBPublicKeyFromPrivateHex(gpriv)
		if err != nil {
			fmt.Println("GNB 推导失败:", err)
			os.Exit(1)
		}
		dHex := hex.EncodeToString(derived)
		fmt.Println("GNB（非标准 ed25519，priv=clamp(SHA512(seed))）:")
		fmt.Printf("  私钥…          : %s… (hex len=%d)\n", gpriv[:16], len(gpriv))
		fmt.Println("  推导出公钥     :", dHex)
		if gpub != "" {
			gpub = strings.TrimSpace(gpub)
			match := strings.EqualFold(dHex, gpub)
			fmt.Println("  对照存盘公钥   :", gpub)
			fmt.Println("  ★ 一致?        :", match)
			ok, verr := keys.VerifyGNBKeyPair(gpriv, gpub)
			fmt.Println("  VerifyGNBKeyPair:", ok, verr)
			if !match {
				os.Exit(2)
			}
		}
		// 对照 stdlib（seed‖pub 布局）——预期不同
		if b, err := hex.DecodeString(gpriv); err == nil && len(b) == 64 {
			std := ed25519.NewKeyFromSeed(b[:32])
			stdPub := hex.EncodeToString(std.Public().(ed25519.PublicKey))
			fmt.Println("  对照 stdlib 公钥:", stdPub)
			fmt.Println("  stdlib==GNB?   :", stdPub == dHex, "（预期 false，布局不同）")
		}
		fmt.Println()
	}

	wpriv := readArgOrFile(get("--wg-priv"))
	wpub := readArgOrFile(get("--wg-pub"))
	if wpriv != "" {
		fmt.Println("WireGuard（X25519）:")
		raw, err := base64.StdEncoding.DecodeString(wpriv)
		if err != nil {
			fmt.Println("  私钥 base64 解码失败:", err)
			os.Exit(1)
		}
		if len(raw) != 32 {
			fmt.Println("  私钥长度", len(raw), "≠ 32")
			os.Exit(1)
		}
		// 官方工具推导：wg pubkey
		cmd := exec.Command("wg", "pubkey")
		cmd.Stdin = strings.NewReader(strings.TrimSpace(wpriv) + "\n")
		out, err := cmd.Output()
		if err != nil {
			fmt.Println("  wg pubkey 失败:", err)
			os.Exit(1)
		}
		dB64 := strings.TrimSpace(string(out))
		fmt.Printf("  私钥…          : %s… (b64)\n", wpriv[:12])
		fmt.Println("  wg pubkey 推导 :", dB64)
		if wpub != "" {
			wpub = strings.TrimSpace(wpub)
			fmt.Println("  对照存盘公钥   :", wpub)
			fmt.Println("  ★ 一致?        :", dB64 == wpub)
			if dB64 != wpub {
				os.Exit(2)
			}
		}
		fmt.Println()
	}

	if gpriv == "" && wpriv == "" {
		fmt.Println("用法: go run ./cmd/keyprove --gnb-priv HEX_OR_FILE --gnb-pub HEX_OR_FILE")
		fmt.Println("      [--wg-priv B64_OR_FILE --wg-pub B64_OR_FILE]")
	}
}
