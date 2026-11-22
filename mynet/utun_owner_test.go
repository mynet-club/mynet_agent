package mynet

import (
	"encoding/binary"
	"testing"
)

func makeFakeSocketFDInfo(family, proto, kind int32, unit uint32, ctlName string) []byte {
	buf := make([]byte, 792)
	binary.LittleEndian.PutUint32(buf[180:184], uint32(proto))
	binary.LittleEndian.PutUint32(buf[184:188], uint32(family))
	binary.LittleEndian.PutUint32(buf[256:260], uint32(kind))
	binary.LittleEndian.PutUint32(buf[284:288], unit)
	copy(buf[288:384], []byte(ctlName))
	return buf
}

func TestParseUtunFromSocketFDInfo(t *testing.T) {
	// unit=8 对应 utun7
	buf := makeFakeSocketFDInfo(32, 2, 6, 8, "com.apple.net.utun_control")
	got, ok := parseUtunFromSocketFDInfo(buf)
	if !ok || got != "utun7" {
		t.Fatalf("got (%q, %v), want (utun7, true)", got, ok)
	}

	// unit=1 对应 utun0
	buf0 := makeFakeSocketFDInfo(32, 2, 6, 1, "com.apple.net.utun_control")
	if got, ok := parseUtunFromSocketFDInfo(buf0); !ok || got != "utun0" {
		t.Fatalf("got (%q, %v), want (utun0, true)", got, ok)
	}

	// 非 utun control socket 必须拒绝
	cases := []struct {
		name string
		buf  []byte
	}{
		{"wrong_family", makeFakeSocketFDInfo(2, 2, 6, 8, "com.apple.net.utun_control")},
		{"wrong_proto", makeFakeSocketFDInfo(32, 1, 6, 8, "com.apple.net.utun_control")},
		{"wrong_kind", makeFakeSocketFDInfo(32, 2, 2, 8, "com.apple.net.utun_control")},
		{"wrong_ctl_name", makeFakeSocketFDInfo(32, 2, 6, 8, "com.apple.netsrc")},
		{"zero_unit", makeFakeSocketFDInfo(32, 2, 6, 0, "com.apple.net.utun_control")},
		{"short_buf", make([]byte, 128)},
	}
	for _, tc := range cases {
		if got, ok := parseUtunFromSocketFDInfo(tc.buf); ok {
			t.Errorf("%s: unexpectedly matched %q", tc.name, got)
		}
	}
}

func TestRouteTouchedWithExistsDeadUtun(t *testing.T) {
	dead := func(name string) bool { return name == "utun9" }
	if !routeTouchedWithExists("utun7", "utun9", dead) {
		t.Fatal("已销毁的旧 utun7 残留路由应允许被新 utun9 接管")
	}
	bothAlive := func(name string) bool { return name == "utun7" || name == "utun9" }
	if routeTouchedWithExists("utun7", "utun9", bothAlive) {
		t.Fatal("仍存活的另一张 utun7 不应被越权改写")
	}
	if routeTouchedWithExists("wg_arthur", "utun9", dead) {
		t.Fatal("非 utun 接口（如 wg_arthur）不应被接管")
	}
}
