package transport

import "testing"

// pong 是传输层控制帧，不得被当成业务帧喂给 HandleFrame——
// 否则每 30s 一次 ping 就刷一条「无法识别的帧」错误日志，把真问题淹掉。
func TestIsPong(t *testing.T) {
	yes := []string{
		`{"t":"pong"}`,
		`{"t":"pong","node_id":"1245816621804034"}`,
		`{"node_id":"1","t":"pong"}`,
		`  {"t":"pong"}  `,
	}
	for _, f := range yes {
		if !isPong([]byte(f)) {
			t.Errorf("isPong(%s) = false, want true", f)
		}
	}

	no := []string{
		`{"t":"ping"}`,                        // 自己发的，不该被当 pong
		`{"event":"route_changed","data":{}}`, // 业务事件帧
		`{"id":"c1","op":"metrics"}`,          // 裸命令帧
		`{}`,                                  // 空对象
		`not json`,                            // 非法 JSON
		``,                                    // 空帧
		`{"t":"pongx"}`,                       // 前缀相同但不是 pong
	}
	for _, f := range no {
		if isPong([]byte(f)) {
			t.Errorf("isPong(%s) = true, want false", f)
		}
	}
}
