package mynet

import "testing"

func TestIdentityGateErrCodes(t *testing.T) {
	e := &IdentityGateErr{Code: "key_mismatch", Message: "m"}
	if e.Error() != "m" {
		t.Fatalf("Error()=%q", e.Error())
	}
}

func TestRequireLocalIdentityNeedsPrivate(t *testing.T) {
	err := RequireLocalIdentity()
	if err == nil {
		t.Skip("本机已有私钥，跳过 need_private 断言")
	}
	ige, ok := err.(*IdentityGateErr)
	if !ok || ige.Code != "need_private" {
		t.Fatalf("want need_private, got %#v", err)
	}
}
