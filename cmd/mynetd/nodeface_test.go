package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/mynetd/commands"
)

// 这组密钥取自 mynet/keys/testdata/node_face_vectors.json —— 即 C-2 的
// Go↔PHP 双端互认向量（PHP 侧 NodeFaceInteropTest 读同一文件）。
// 用它验签，证明结果帧的签名就是 back 会接受的那一个，而不是自证自话。
const (
	vecNodePriv = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	vecBackPub  = "db48257e1237976a74ad8cfedca00213408fe89ac6251f1b930245f242b5c31a"
	vecNodeID   = "1245816621804034"
)

// writeConf 接受旧 KEY=VALUE 文本，落到配置树 v2 的 keys/。
// 测试仍用旧形状的夹具，内部转成 node.private + meta.json。
func writeConf(t *testing.T, body string) string {
	t.Helper()
	kv := map[string]string{}
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return writeKeys(t, kv["NODE_ID"], kv["NODE_PRIV_HEX"], kv["BACK_PUB_HEX"])
}

func writeKeys(t *testing.T, nodeID, priv, backPub string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	dir := filepath.Join(home, "conf", "local", "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.private"), []byte(priv+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"node_id":"` + nodeID + `","algorithm":"x25519","back_pub_hex":"` + backPub + `"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func validConf() string {
	return "NODE_ID=" + vecNodeID + "\n" +
		"NODE_PRIV_HEX=" + vecNodePriv + "\n" +
		"NODE_PUB_HEX=0000000000000000000000000000000000000000000000000000000000000000\n" +
		"BACK_PUB_HEX=" + vecBackPub + "\n"
}

func TestLoadNodeIdentity(t *testing.T) {
	writeConf(t, validConf())
	id, err := loadNodeIdentity("")
	if err != nil {
		t.Fatalf("loadNodeIdentity: %v", err)
	}
	if id.nodeID != vecNodeID {
		t.Errorf("nodeID = %q, want %q", id.nodeID, vecNodeID)
	}
	if id.signer == nil {
		t.Fatal("signer 为 nil")
	}
}

// 注释行、空行、值两侧空格都要能正确解析。
func TestLoadNodeIdentityToleratesCommentsAndSpaces(t *testing.T) {
	conf := "# mynet setup 产出\n\n  NODE_ID = " + vecNodeID + "  \n" +
		"NODE_PRIV_HEX=" + vecNodePriv + "\n" +
		"# 下面是 back 公钥\nBACK_PUB_HEX=" + vecBackPub + "\n"
	id, err := loadNodeIdentity(writeConf(t, conf))
	if err != nil {
		t.Fatalf("loadNodeIdentity: %v", err)
	}
	if id.nodeID != vecNodeID {
		t.Errorf("nodeID = %q（空格未 trim？）", id.nodeID)
	}
}

func TestLoadNodeIdentityMissingFile(t *testing.T) {
	_, err := loadNodeIdentity(filepath.Join(t.TempDir(), "nope.conf"))
	if err == nil {
		t.Fatal("缺文件应报错")
	}
	if !strings.Contains(err.Error(), "mynet setup") {
		t.Errorf("错误应指向修复动作（mynet setup），实际: %v", err)
	}
}

// 缺字段必须**点名**缺哪个，否则现场排障要靠猜。
func TestLoadNodeIdentityMissingFields(t *testing.T) {
	_, err := loadNodeIdentity(writeConf(t, "NODE_ID=123\n"))
	if err == nil {
		t.Fatal("缺字段应报错")
	}
	for _, want := range []string{"node.private", "back_pub_hex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误应点名缺失 %s，实际: %v", want, err)
		}
	}
}

// 私钥长度不对时必须报错，不能构造出一个签不出有效签名的 signer。
func TestLoadNodeIdentityRejectsBadKey(t *testing.T) {
	conf := "NODE_ID=1\nNODE_PRIV_HEX=deadbeef\nBACK_PUB_HEX=" + vecBackPub + "\n"
	if _, err := loadNodeIdentity(writeConf(t, conf)); err == nil {
		t.Fatal("非法私钥长度应报错")
	}
}

// 结果帧必须：路径符合 back 契约、body 符合 reportResult 的校验规则、
// 且签名能被 C-2 向量密钥验过。
func TestEncodeResultFrameIsSignedAndShaped(t *testing.T) {
	id, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}

	res := commands.Result{
		ID: "cmd-777", OK: true, Stdout: "routes refreshed", Stderr: "", Elapsed: 42,
	}
	raw, err := id.encodeResult(res)
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}

	var f resultFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("结果帧不是合法 JSON: %v（%s）", err, raw)
	}

	if f.T != "result" {
		t.Errorf("t = %q, want result", f.T)
	}
	if f.NodeID != vecNodeID {
		t.Errorf("node_id = %q, want %q", f.NodeID, vecNodeID)
	}
	if f.Method != "PATCH" {
		t.Errorf("method = %q, want PATCH（back 的路由是 PATCH）", f.Method)
	}
	wantPath := "/api/v1/node/commands/cmd-777/result"
	if f.Path != wantPath {
		t.Errorf("path = %q, want %q", f.Path, wantPath)
	}

	// body 必须符合 NodeHeartbeatController::reportResult 的校验：
	// success:required|boolean, message:nullable|string, result:nullable|array
	var body struct {
		Success bool           `json:"success"`
		Message string         `json:"message"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(f.Body), &body); err != nil {
		t.Fatalf("body 不是合法 JSON: %v（%s）", err, f.Body)
	}
	if !body.Success {
		t.Error("OK=true 的结果 success 应为 true")
	}
	if body.Result["stdout"] != "routes refreshed" {
		t.Errorf("stdout 未进 result: %+v", body.Result)
	}
	if body.Result["elapsed_ms"] != float64(42) {
		t.Errorf("elapsed_ms = %v, want 42", body.Result["elapsed_ms"])
	}

	// 用 C-2 向量密钥验签：证明这就是 back 会接受的签名
	verifier, err := keys.NewNodeFaceSigner(vecNodePriv, vecBackPub)
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.Verify(f.Method, f.Path, f.TS, f.Body, f.Sig) {
		t.Errorf("签名验不过（method=%s path=%s ts=%s）—— back 会拒收", f.Method, f.Path, f.TS)
	}
	if f.TS == "" || f.Sig == "" {
		t.Error("ts / sig 不得为空")
	}
}

// 失败的命令必须如实上报 success=false，且 message 带上原始错误。
// 把失败报成成功比不报更糟。
func TestEncodeResultFrameReportsFailure(t *testing.T) {
	id, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := id.encodeResult(commands.Result{
		ID: "cmd-888", OK: false, Error: "gnb_ctl: 节点未运行", Stderr: "exit 1",
	})
	if err != nil {
		t.Fatalf("encodeResult: %v", err)
	}

	var f resultFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(f.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Success {
		t.Error("OK=false 的结果 success 必须为 false")
	}
	if !strings.Contains(body.Message, "gnb_ctl") {
		t.Errorf("message 应保留原始错误，实际 %q", body.Message)
	}
}

// 签名覆盖 body：body 被改一个字节，签名就必须验不过。
// 这条锁住「gateway 不得在转发途中改写 body」的约束。
func TestSignatureCoversBody(t *testing.T) {
	id, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := id.encodeResult(commands.Result{ID: "c1", OK: true, Stdout: "ok"})
	if err != nil {
		t.Fatal(err)
	}

	var f resultFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}

	verifier, _ := keys.NewNodeFaceSigner(vecNodePriv, vecBackPub)
	if !verifier.Verify(f.Method, f.Path, f.TS, f.Body, f.Sig) {
		t.Fatal("原始帧应验签通过")
	}
	tampered := strings.Replace(f.Body, `"success":true`, `"success":false`, 1)
	if tampered == f.Body {
		t.Skip("body 里没有可篡改的字段，用例需更新")
	}
	if verifier.Verify(f.Method, f.Path, f.TS, tampered, f.Sig) {
		t.Error("body 被篡改后签名仍验过 —— 签名没有覆盖 body")
	}
}

// keys 目录必须跟 mynet setup v2 的写入方一致，否则永远读不到。
func TestNodeKeysDirMatchesSetupWriter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	// 无 mode.json → 默认 local
	got, err := nodeKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "conf", "local", "keys"); got != want {
		t.Errorf("默认 keys = %q, want %q", got, want)
	}
	// mode.json online → online/{tenant}/keys
	if err := os.WriteFile(filepath.Join(home, "mode.json"),
		[]byte(`{"schema":2,"mode":"online","active":{"tenant_id":"t_9","node_id":"n"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = nodeKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "conf", "online", "t_9", "nodes", "n", "keys"); got != want {
		t.Errorf("online keys = %q, want %q", got, want)
	}
}
