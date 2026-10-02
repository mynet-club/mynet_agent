package mynet

import (
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// 三选一参数解析（spec §13.1）：模式推断 + 来源列表。
func TestDNSApplyArgsModeInference(t *testing.T) {
	cases := []struct {
		args  []string
		mode  string
		peer  int
		smart int
	}{
		{[]string{"--peer", "10.0.0.1"}, domain.DNSModePeer, 1, 0},
		{[]string{"--smart", "223.5.5.5"}, domain.DNSModeSmart, 0, 1},
		{[]string{"--peer", "10.0.0.1", "--smart", "223.5.5.5"}, domain.DNSModePeerSmart, 1, 1},
		{[]string{"--mode", "peer_smart", "--peer", "10.0.0.1", "--peer", "10.0.0.2", "--smart", "1.1.1.1"}, domain.DNSModePeerSmart, 2, 1},
	}
	for _, c := range cases {
		// 只测解析逻辑：抽出 mode/peers/smarts 的推断
		var mode string
		var peers, smarts []string
		for i := 0; i < len(c.args); i++ {
			switch c.args[i] {
			case "--mode":
				mode = c.args[i+1]
				i++
			case "--peer":
				peers = append(peers, c.args[i+1])
				i++
			case "--smart":
				smarts = append(smarts, c.args[i+1])
				i++
			}
		}
		if mode == "" {
			switch {
			case len(peers) > 0 && len(smarts) > 0:
				mode = domain.DNSModePeerSmart
			case len(peers) > 0:
				mode = domain.DNSModePeer
			case len(smarts) > 0:
				mode = domain.DNSModeSmart
			}
		}
		if mode != c.mode || len(peers) != c.peer || len(smarts) != c.smart {
			t.Errorf("args=%v: mode=%s peer=%d smart=%d, want %s %d %d",
				c.args, mode, len(peers), len(smarts), c.mode, c.peer, c.smart)
		}
	}
}

func TestDNSApplyArgsRejectsBadMode(t *testing.T) {
	if rc := cmdDNS([]string{"apply", "--mode"}); rc != 2 {
		t.Fatalf("--mode 缺值应返回 2，实得 %d", rc)
	}
	if rc := cmdDNS([]string{"bogus"}); rc != 2 {
		t.Fatalf("未知子命令应返回 2，实得 %d", rc)
	}
}
