package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCrossInstanceNotifyDelivers 是 V4-4/V4-5 判据的核心：
//
//	节点连 A、back 通知 B → B 能推达
//
// 改之前 `Registry.Notify` 只查本地 `conns`，所以节点连在 A 时 back 打到 B
// **必定 404**（errNotOnline）。现在 B 会查 presence、经 Redis 把帧原文广播过去，
// 由 A 的 watchNode 收货并投递给本地连接。
//
// 这条测试用**真 Redis**（本机 127.0.0.1:6379），不是 mock —— 按 §7.1 D2，
// 测试必须打真端点；用 MemoryPresence 共享变量的话，测的是「Go 变量赋值」而不是
// 「跨实例 pub/sub」，那个连通性问题它一个也发现不了。
func TestCrossInstanceNotifyDelivers(t *testing.T) {
	// 用独立 DB（2），避免碰 back 心跳验证用的 DB 1，也避免污染 0
	ps, err := NewRedisPresence("127.0.0.1:6379", "", 2)
	if err != nil {
		t.Fatalf("NewRedisPresence: %v", err)
	}
	if _, isMem := ps.(*MemoryPresence); isMem {
		// Redis 连不上时 NewRedisPresence 不报错、只降级到内存。
		// 那样两个 Registry 共享的是**同一个变量**，测不出跨实例 ——
		// 必须明说而不是硬跑一个假绿的测试。
		t.Skip("本机 127.0.0.1:6379 不可达，跨实例测试需要真 Redis（DB2）")
	}
	defer ps.Close()
	ctx := context.Background()

	// 两个 Registry = 两个 gateway 实例，**共用**同一个 presence（Redis）
	regA := NewRegistry()
	regB := NewRegistry()
	regA.SetPresence(ps)
	regB.SetPresence(ps)

	// 清一下上一次测试的残留
	_ = ps.SetOffline(ctx, "node-cross-instance")

	// 节点连 A，并被 back 置位 verified（C-5 只推已置位的连接）
	regA.RegisterCtx("node-cross-instance", nil)
	defer func() { regA.Unregister("node-cross-instance") }()
	if !regA.MarkVerified("node-cross-instance") {
		t.Fatal("MarkVerified 应成功")
	}

	// 等注册与订阅就绪：Subscribe 的 Redis SUBSCRIBE 是异步建立的，
	// 立刻 Publish 可能早于订阅完成（pub/sub 不重放）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		on, ver, err := ps.IsOnline(ctx, "node-cross-instance")
		if err == nil && on && ver {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("presence 未在 3s 内变为 online+verified: on=%v ver=%v err=%v", on, ver, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // 再等 Redis SUBSCRIBE 真正建立

	// B 通知：B 本地**没有**这个节点的连接
	if _, has := regB.Get("node-cross-instance"); has {
		t.Fatal("测试前提不成立：B 上不该有这个节点")
	}
	evt := "command_queued"
	data := map[string]string{"command_id": "9007199254740993"}
	if err := regB.Notify(ctx, "node-cross-instance", evt, data); err != nil {
		t.Fatalf("B 通知失败（改之前这里必是 errNotOnline → 404）: %v", err)
	}

	// A 的连接应当收到完整帧
	conn, has := regA.Get("node-cross-instance")
	if !has {
		t.Fatal("A 上应有这个节点")
	}
	select {
	case frame := <-conn.send:
		var got struct {
			Event string            `json:"event"`
			Data  map[string]string `json:"data"`
		}
		if err := json.Unmarshal(frame, &got); err != nil {
			t.Fatalf("收到的帧不是合法 JSON: %v\n  原文: %s", err, frame)
		}
		if got.Event != evt {
			t.Errorf("event = %q，期望 %q（原接口只传 nodeID，拿不到 event —— 这正是要改的地方）", got.Event, evt)
		}
		if got.Data["command_id"] != data["command_id"] {
			t.Errorf("command_id = %q，期望 %q", got.Data["command_id"], data["command_id"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("A 没有在 3s 内收到跨实例投递的帧")
	}
}

// 节点确实离线时才该 404 —— 这是唯一该 404 的情形。
// 守住它，免得为了「不 404」把离线也谎报成成功。
func TestNotifyOffNodeStill404(t *testing.T) {
	reg := NewRegistry() // 内存 presence，单实例
	ctx := context.Background()
	_ = reg.presence.SetOffline(ctx, "ghost-node")
	// 注册后确实不在线 —— 下面这一步才是被测行为
	if err := reg.Notify(ctx, "ghost-node", "command_queued", nil); err != errNotOnline {
		t.Fatalf("离线节点应返回 errNotOnline，得到 %v", err)
	}
}

// 节点在本实例时要**直接投递**，不该绕一圈 Redis
// （绕圈会多一跳延迟，而且 Redis 挂了时会把本可送达的也丢掉）。
func TestNotifyLocalDeliversDirectly(t *testing.T) {
	reg := NewRegistry()
	ctx := context.Background()
	reg.RegisterCtx("local-node", nil)
	defer func() { reg.Unregister("local-node") }()

	if err := reg.Notify(ctx, "local-node", "bundle_changed", nil); err != errNotVerified {
		t.Fatalf("未置位应返回 errNotVerified（403），得到 %v", err)
	}
	if !reg.MarkVerified("local-node") {
		t.Fatal("MarkVerified 应成功")
	}
	if err := reg.Notify(ctx, "local-node", "bundle_changed", map[string]string{"ts": "1"}); err != nil {
		t.Fatalf("本实例投递失败: %v", err)
	}
	conn, _ := reg.Get("local-node")
	select {
	case frame := <-conn.send:
		if !strings.Contains(string(frame), "bundle_changed") {
			t.Errorf("帧内容不对: %s", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("1s 内没收到帧")
	}
}
