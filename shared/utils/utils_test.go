package utils

import "testing"

func TestNodeStatusRoundTrip(t *testing.T) {
	cases := []struct {
		num int
		str string
	}{
		{NodeStatusOnline, NodeStatusOnlineStr},
		{NodeStatusOffline, NodeStatusOfflineStr},
		{NodeStatusUnknown, NodeStatusUnknownStr},
	}
	for _, c := range cases {
		if got := NodeStatusToString(c.num); got != c.str {
			t.Fatalf("NodeStatusToString(%d)=%q want %q", c.num, got, c.str)
		}
		if got := NodeStatusFromString(c.str); got != c.num {
			t.Fatalf("NodeStatusFromString(%q)=%d want %d", c.str, got, c.num)
		}
	}
}

func TestIsValidNodeType(t *testing.T) {
	if !IsValidNodeType(NodeTypeGNB) || !IsValidNodeType(NodeTypeWireGuard) {
		t.Fatal("expected gnb/wireguard valid")
	}
	if IsValidNodeType("openvpn") {
		t.Fatal("openvpn must be invalid")
	}
}

func TestFormatBytes(t *testing.T) {
	if FormatBytes(512) != "512 B" {
		t.Fatalf("got %q", FormatBytes(512))
	}
	if FormatBytes(2048) != "2.0 KB" {
		t.Fatalf("got %q", FormatBytes(2048))
	}
}

func TestSafeCreateDir(t *testing.T) {
	dir := t.TempDir() + "/nested/leaf"
	if err := SafeCreateDir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}
