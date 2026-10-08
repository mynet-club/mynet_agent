package relaypolicy

import "testing"

func TestDenyBeforeAllow(t *testing.T) {
	// 隐性知识：先 deny 后 allow；两动作必须同时做
	if !IsAllowed("/api/v1/auth/login") {
		t.Fatal("auth/login should be allowed")
	}
	if !IsAllowed("/api/v1/auth/logout") {
		t.Fatal("auth/logout should be allowed")
	}
	if IsAllowed("/api/v1/admin/secret") {
		t.Fatal("admin must be denied even if prefix open")
	}
	if IsAllowed("/api/v1/admin/login") {
		t.Fatal("admin must be denied")
	}
}

func TestPercentAlwaysDenied(t *testing.T) {
	if IsAllowed("/api/v1/nodes%2f../admin") {
		t.Fatal("percent must be denied")
	}
	if IsAllowed("/api/v1/nodes/%2e%2e") {
		t.Fatal("percent must be denied")
	}
}

func TestCustomPrefixStillHonorsDeny(t *testing.T) {
	// 即便运维配了过宽前缀，deniedFragments 仍生效
	wide := []string{"/api/"}
	if IsAllowedWith("/api/v1/admin/x", wide) {
		t.Fatal("deny must win over wide allow")
	}
}

func TestSimplifiedDeviceFlowPaths(t *testing.T) {
	for _, path := range []string{"/api/v1/nodes/quick-create", "/api/v1/nodes/42/keys", "/api/v1/nodes/42/status", "/api/v1/nodes/42/bundle/publish", "/api/v1/nodes/42/bundle"} {
		if !IsAllowed(path) {
			t.Fatalf("device flow blocked: %s", path)
		}
	}
}
