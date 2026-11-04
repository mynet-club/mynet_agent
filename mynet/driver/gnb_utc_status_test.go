//go:build !windows

package driver

import (
	"crypto/sha512"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUTCStatusRequiresExactCurrentProjectionAndFreshBoot(t *testing.T) {
	d := utcTestDriver(t)
	p := utcProjection()
	boot := strings.Repeat("a", 32)
	now := time.Unix(1900000000, 0)
	canonical, err := p.canonical()
	if err != nil {
		t.Fatal(err)
	}
	policy := "policy" + strings.TrimPrefix(strings.Split(string(canonical), "\n")[1], "peer")
	digest := sha512.Sum512([]byte(policy + "\n"))
	status := fmt.Sprintf("status-v1 lab node7 7 %s 1 1900000000 OK 123\nmetrics 1 0 0 0\n%s\npeer 8 78 1 APPLIED POLICY_LOADED %s %x\n", boot, policy, p.Peers[0].Manifest, digest)
	for _, test := range []struct {
		name, text string
		valid      bool
	}{
		{"valid", status, true},
		{"oldboot", strings.Replace(status, boot, strings.Repeat("b", 32), 1), false},
		{"staletime", strings.Replace(status, "1900000000", "1899999994", 1), false},
		{"future", strings.Replace(status, "1900000000", "1900000001", 1), false},
		{"oldpid", strings.Replace(status, "OK 123", "OK 122", 1), false},
		{"blocked", strings.Replace(status, "APPLIED", "BLOCKED", 1), false},
		{"endpoint", strings.Replace(status, "14001", "14002", 1), false},
		{"digest", strings.Replace(status, fmt.Sprintf("%x", digest), strings.Repeat("0", 128), 1), false},
		{"extra", status + "peer 9 1 1 APPLIED\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(d.ConfDir, "utc.status"), []byte(test.text), 0600); err != nil {
				t.Fatal(err)
			}
			err := d.VerifyUTCProjection(p, boot, 123, now)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}
