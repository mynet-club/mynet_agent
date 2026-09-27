package main

import "testing"

func TestEventFromProbe(t *testing.T) {
	cases := []struct {
		p    Probe
		want Event
	}{
		{Probe{Up: false}, EvDaemonDown},
		{Probe{Up: true, NodeState: "RUNNING", ProcessUp: true}, EvNodeHealthy},
		{Probe{Up: true, NodeState: "RUNNING", ProcessUp: false}, EvDaemonDown},
		{Probe{Up: true, NodeState: "DEGRADED", ProcessUp: true}, EvNodeDegraded},
		{Probe{Up: true, NodeState: "FAILED", ProcessUp: true}, EvNodeFailed},
		{Probe{Up: true, NodeState: "FAILED"}, EvDaemonDown},
		// 节点未启动 ≠ 异常：一律 Down → Stopped
		{Probe{Up: true, NodeState: "UNINSTALLED"}, EvDaemonDown},
		{Probe{Up: true, NodeState: "INSTALLED"}, EvDaemonDown},
		{Probe{Up: true, NodeState: "CONFIGURED"}, EvDaemonDown},
		{Probe{Up: true, NodeState: ""}, EvDaemonDown},
	}
	for _, c := range cases {
		if got := eventFromProbe(c.p); got != c.want {
			t.Errorf("eventFromProbe(%+v) = %s, want %s", c.p, got, c.want)
		}
	}
}

func TestNewControllerHasDescribe(t *testing.T) {
	c := NewController("")
	if c.Describe() == "" {
		t.Fatal("Describe 不应为空")
	}
}

type fakeProber struct {
	up        bool
	state     string
	processUp bool
}

func (f *fakeProber) Probe() (Probe, error) {
	return Probe{Up: f.up, NodeState: f.state, ProcessUp: f.processUp}, nil
}

func TestProbeFeedsFSM(t *testing.T) {
	f := NewFSM()
	p := &fakeProber{up: false}
	pr, _ := p.Probe()
	_, _, _ = f.On(eventFromProbe(pr))
	if f.State() != StateStopped {
		t.Fatalf("down → %s", f.State())
	}

	p.up, p.state, p.processUp = true, "RUNNING", true
	pr, _ = p.Probe()
	_, _, _ = f.On(eventFromProbe(pr))
	if f.State() != StateRunning {
		t.Fatalf("RUNNING → %s", f.State())
	}

	p.state = "DEGRADED"
	pr, _ = p.Probe()
	_, _, _ = f.On(eventFromProbe(pr))
	if f.State() != StateDegraded {
		t.Fatalf("DEGRADED → %s", f.State())
	}
}
