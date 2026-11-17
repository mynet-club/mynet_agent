package tunnelstate

import "testing"

// 意图读写往返 + MarkWant 合并语义（NodeID 不被启停动作抹掉）。
func TestMarkWantRoundTripKeepsNodeID(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	if err := Save(State{WantRunning: true, Driver: "gnb", NodeID: "8888888888880001"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkWant(false, "gnb"); err != nil {
		t.Fatal(err)
	}
	st := Load()
	if st.WantRunning {
		t.Fatal("stop 后 want_running 应为 false")
	}
	if st.NodeID != "8888888888880001" {
		t.Fatalf("MarkWant 不该抹掉 node_id，got %q", st.NodeID)
	}
	if st.Driver != "gnb" {
		t.Fatalf("driver=%q", st.Driver)
	}
	if err := MarkWant(true, "wireguard"); err != nil {
		t.Fatal(err)
	}
	st = Load()
	if !st.WantRunning || st.Driver != "wireguard" {
		t.Fatalf("got %+v", st)
	}
}

func TestLoadMissingIsZero(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	st := Load()
	if st.WantRunning || st.Driver != "" {
		t.Fatalf("读不到文件应为零值，got %+v", st)
	}
}
