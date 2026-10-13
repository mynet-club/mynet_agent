package mynet

import (
	"strings"
	"testing"
)

// C1-S1 回归：`config keys set` 私钥走 stdin（--private-stdin，对齐 auth login --password-stdin），
// 不走 argv —— argv 会进 `ps` 与本机 API 的 argv 回显面。
func TestParseKeysSetArgsStdin(t *testing.T) {
	secret := strings.Repeat("ab", 64)
	priv, pub, nodeID, algo, err := parseKeysSetArgs(
		[]string{"--private-stdin", "--public", "aabb", "--node-id", "n1", "--algorithm", "ed25519"},
		strings.NewReader(secret+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if priv != secret {
		t.Fatalf("stdin 私钥没读到: %q", priv)
	}
	if pub != "aabb" || nodeID != "n1" || algo != "ed25519" {
		t.Fatalf("其余参数解析错: pub=%q nodeID=%q algo=%q", pub, nodeID, algo)
	}
}

// --private HEX 保留给人工终端一次性导入（安装文档命令）。
func TestParseKeysSetArgsDirectFlag(t *testing.T) {
	secret := strings.Repeat("cd", 64)
	priv, _, _, _, err := parseKeysSetArgs([]string{"--private", secret}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if priv != secret {
		t.Fatalf("--private 没读到: %q", priv)
	}
}

// 既无 --private 也无 --private-stdin → 明确报错（提示走 stdin）。
func TestParseKeysSetArgsMissingPrivate(t *testing.T) {
	_, _, _, _, err := parseKeysSetArgs([]string{"--node-id", "n1"}, strings.NewReader(""))
	if err == nil {
		t.Fatal("缺私钥应报错")
	}
	if !strings.Contains(err.Error(), "--private-stdin") {
		t.Fatalf("报错应提示 --private-stdin: %v", err)
	}
}

// --private-stdin 但 stdin 空 → 报错，不写空私钥。
func TestParseKeysSetArgsEmptyStdin(t *testing.T) {
	_, _, _, _, err := parseKeysSetArgs([]string{"--private-stdin"}, strings.NewReader("\n"))
	if err == nil {
		t.Fatal("空 stdin 应报错")
	}
}
