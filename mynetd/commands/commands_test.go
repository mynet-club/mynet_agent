package commands

import "testing"

func TestPlanFiveBinMynetChains(t *testing.T) {
	// D-6：原 command.linux.json 里 command=bin/mynet 的五条
	cases := []struct {
		op   string
		args map[string]string
		want []string
	}{
		{OpRefreshRoutesGnb, map[string]string{"node_id": "42"},
			[]string{"refresh-routes", "--auto", "--node-id", "42"}},
		{OpRefreshAddressGnb, nil,
			[]string{"refresh-address", "--auto"}},
		{OpRefreshRoutesWG, map[string]string{"node_id": "42"},
			[]string{"refresh-routes", "--auto", "--vpn-type", "wireguard", "--node-id", "42"}},
		{OpRefreshAddressWG, nil,
			[]string{"refresh-address", "--auto", "--vpn-type", "wireguard"}},
		{OpRouteRefresh, map[string]string{"node_id": "7"},
			[]string{"refresh-routes", "--auto", "--node-id", "7"}},
	}
	for _, c := range cases {
		got, ok := Plan(Request{Op: c.op, Args: c.args}, "")
		if !ok {
			t.Fatalf("%s not planned", c.op)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: %v want %v", c.op, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: %v want %v", c.op, got, c.want)
			}
		}
	}
}

func TestProxyOpsAreGone(t *testing.T) {
	if _, ok := Plan(Request{Op: "plugin.proxy.start"}, ""); ok {
		t.Fatal("plugin.proxy.* 已下架，不得规划")
	}
}

func TestRunUnknownOp(t *testing.T) {
	r := Run(func([]string) (string, string, error) { return "", "", nil },
		Request{ID: "1", Op: "curl"}, "")
	if r.OK || r.Error == "" {
		t.Fatalf("unknown op must fail: %+v", r)
	}
}
