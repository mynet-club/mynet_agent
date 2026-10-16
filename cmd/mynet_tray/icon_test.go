package main

import (
	"bytes"
	"image/png"
	"testing"
)

// TestIconKindFor 锁三态映射：与 P3 文案同一条区分线 ——
// 「主动停着 / 正在启停」是正常未连接（淡），「失败 / 降级 / 服务不可用」是异常（斜杠）。
func TestIconKindFor(t *testing.T) {
	cases := []struct {
		in   State
		want iconKind
	}{
		{StateRunning, iconConnected},
		{StateStopped, iconIdle},
		{StateStarting, iconIdle},
		{StateStopping, iconIdle},
		{StateDegraded, iconFault},
		{StateFailed, iconFault},
		{StateSvcDown, iconFault},
	}
	for _, c := range cases {
		if got := iconKindFor(c.in); got != c.want {
			t.Errorf("iconKindFor(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestSnapshotCarriesIconKind 图标态必须和状态行同源（都出自 snapshot），
// 否则会出现「文案说运行中、图标是灰的」这种自相矛盾的界面。
func TestSnapshotCarriesIconKind(t *testing.T) {
	cases := map[string]iconKind{
		"running": iconConnected,
		"stopped": iconIdle,
		"down":    iconFault,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			isolateUserConfig(t)
			a, _ := newBootApp(t, bootProbes[name])
			a.bootSequence()
			if got := a.snapshot().icon; got != want {
				t.Errorf("snapshot().icon = %d, want %d", got, want)
			}
		})
	}
}

// TestAcceptIconOnlyOnChange 图标只在态变了才推 systray：
// 3s 轮询每轮白推一次，Windows 腿会白跑一次 Shell_NotifyIcon。
func TestAcceptIconOnlyOnChange(t *testing.T) {
	isolateUserConfig(t)
	stub := newStub(t, stubSpec{connectFile: "connect_ok.json", commandFile: "command_ok.json"})
	a, _ := testApp(t, stub.base(), &fakeProber{script: []Probe{bootProbes["stopped"]}})

	if !a.acceptIcon(iconIdle) {
		t.Error("冷启动 pushedIcon=iconUnset，第一次推必须放行")
	}
	if a.acceptIcon(iconIdle) {
		t.Error("同态重复推必须被拦下")
	}
	if !a.acceptIcon(iconFault) {
		t.Error("态变了必须放行")
	}
	if !a.acceptIcon(iconIdle) {
		t.Error("态变回去也必须放行")
	}
}

// alphaSum 解出 PNG 的 alpha 总量，用来断言「淡」真的是淡、「斜杠」真的挖掉了像素。
func alphaSum(t *testing.T, b []byte) float64 {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	sum := 0.0
	bnd := img.Bounds()
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			sum += float64(a) / 65535
		}
	}
	return sum
}

// TestIconVariantsAreDistinguishable 三态字节必须两两不同，且差异方向正确：
// 淡态 alpha 总量明显小于实心，斜杠态挖掉一条带所以也小于实心。
// 这是「不需要设计稿」的落点断言 —— 派生出来的三张图真的分得开。
func TestIconVariantsAreDistinguishable(t *testing.T) {
	for _, template := range []bool{true, false} {
		leg := "regular"
		if template {
			leg = "template"
		}
		t.Run(leg, func(t *testing.T) {
			c := iconBytes(iconConnected, template)
			i := iconBytes(iconIdle, template)
			f := iconBytes(iconFault, template)

			if bytes.Equal(c, i) || bytes.Equal(c, f) || bytes.Equal(i, f) {
				t.Fatal("三态字节必须两两不同")
			}
			sc, si, sf := alphaSum(t, c), alphaSum(t, i), alphaSum(t, f)
			if !(si < sc*0.6) {
				t.Errorf("淡态 alpha=%v 应明显小于实心 %v", si, sc)
			}
			if !(sf < sc*0.95) {
				t.Errorf("斜杠态 alpha=%v 应小于实心 %v（带上的像素被挖掉）", sf, sc)
			}
			if si <= 0 || sf <= 0 {
				t.Errorf("淡态/斜杠态不能整张透明（si=%v sf=%v）", si, sf)
			}
		})
	}
}

// TestTransformIconKeepsSourceOnGarbageInput 解码不了就原样返回：
// 图标是装饰，宁可显示原图也不能让菜单栏空一格。
func TestTransformIconKeepsSourceOnGarbageInput(t *testing.T) {
	bad := []byte("not a png")
	if got := transformIcon(bad, idleAlpha, true); !bytes.Equal(got, bad) {
		t.Errorf("应原样返回源字节，got %d bytes", len(got))
	}
}

// TestIconBytesFallsBackOnBadKind 越界态不得 panic：图标是装饰，
// 不值得让它把渲染路径带崩。
func TestIconBytesFallsBackOnBadKind(t *testing.T) {
	if got, want := iconBytes(iconKind(99), true), iconBytes(iconIdle, true); !bytes.Equal(got, want) {
		t.Error("越界态应回退到淡态")
	}
	if got, want := iconBytes(iconUnset, true), iconBytes(iconIdle, true); !bytes.Equal(got, want) {
		t.Error("iconUnset 应回退到淡态")
	}
}
