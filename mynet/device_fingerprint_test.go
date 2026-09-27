package mynet

import "testing"

func TestDeviceFingerprintStable(t *testing.T) {
	a := DeviceFingerprint()
	b := DeviceFingerprint()
	if a == "" || a != b {
		t.Fatalf("指纹不稳定: %q vs %q", a, b)
	}
	if len(a) < 32 {
		t.Fatalf("太短: %q", a)
	}
	t.Log("fingerprint", a)
}
