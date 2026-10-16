package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"math"

	_ "embed"
)

// iconTemplatePNG macOS 菜单栏 Template 图（单色剪影，系统自动染深浅）。
//
//go:embed icon_template.png
var iconTemplatePNG []byte

// iconPNG 彩色图（Windows / 无 Template 时的回退）。
//
//go:embed icon.png
var iconPNG []byte

// iconKind 菜单栏图标三态（P4：区分连接、未连接、异常）。
type iconKind int

const (
	iconConnected iconKind = iota // 实心剪影：隧道在跑
	iconIdle                      // 淡剪影：没连上，但一切正常（主动停着 / 正在启停）
	iconFault                     // 剪影切一道斜杠：出事了（失败 / 降级 / 本机服务不可用）

	iconKindCount = int(iconFault) + 1
)

// iconUnset 还没推过任何图标。零值不能当「已推 connected」用，
// 否则冷启动第一帧若真是 Running 就会被 acceptIcon 当成重复而跳过。
const iconUnset iconKind = -1

// iconKindFor 把 FSM 状态翻成图标态。纯函数，可单测。
//
// 与 P3 的文案同一条区分线：「用户主动停着 / 正在启停」是正常未连接（淡），
// 「失败 / 降级 / 本机服务不可用」是异常（斜杠）。
func iconKindFor(s State) iconKind {
	switch s {
	case StateRunning:
		return iconConnected
	case StateDegraded, StateFailed, StateSvcDown:
		return iconFault
	default:
		return iconIdle
	}
}

// icons 三态 × 两条平台腿的字节，进程内只算一次。
//
// 为什么能纯代码派生、不需要设计稿：
//   - macOS 那条腿只吃 template 那份（systray 的 SetTemplateIcon 在 darwin 把
//     regular 直接丢掉），且 NSImage.template=YES 只按 **alpha** 渲染剪影、
//     颜色由系统按菜单栏深浅决定 —— 所以「亮/灰」在 macOS 上不是换颜色，
//     是压 alpha；淡剪影会被系统自动染成浅灰，深浅菜单栏都适配。
//   - Windows 那条腿反过来 = SetIcon(regular)，用彩色图；压 alpha 一样变淡。
//   - 第三态没有颜色可用（template 不吃颜色），只能改**形状**：切一道斜杠
//     就是把这条带上的 alpha 清零，两腿都看得见。
var icons = buildIcons()

type iconPair struct {
	template, regular []byte
}

func buildIcons() [iconKindCount]iconPair {
	var out [iconKindCount]iconPair
	for k := 0; k < iconKindCount; k++ {
		mul := 1.0
		if iconKind(k) == iconIdle {
			mul = idleAlpha
		}
		slash := iconKind(k) == iconFault
		out[k] = iconPair{
			template: transformIcon(iconTemplatePNG, mul, slash),
			regular:  transformIcon(iconPNG, mul, slash),
		}
	}
	return out
}

// idleAlpha 未连接态的不透明度。太低在深色菜单栏上会看不见，太高和实心分不开。
const idleAlpha = 0.35

// transformIcon 解码 PNG，按比例压 alpha、可选切一道斜杠，再编码回去。
// 解码/编码失败就原样返回 —— 图标是装饰，不值得让它把托盘带崩。
func transformIcon(src []byte, alphaMul float64, slash bool) []byte {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	b := img.Bounds()
	out := image.NewNRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := out.NRGBAAt(x, y)
			switch {
			case slash && onSlash(x-b.Min.X, y-b.Min.Y, b.Dx(), b.Dy()):
				c.A = 0
			case alphaMul != 1:
				c.A = uint8(math.Round(float64(c.A) * alphaMul))
			}
			out.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return src
	}
	return buf.Bytes()
}

// onSlash 主对角线方向的带（归一化坐标 |u-v| < 宽），与源图分辨率无关。
func onSlash(x, y, w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	return math.Abs(float64(x)/float64(w)-float64(y)/float64(h)) < slashWidth
}

const slashWidth = 0.18

// iconBytes 取某态某腿的字节。icons 是包级 var，进程内只算一次。
func iconBytes(k iconKind, template bool) []byte {
	if k < 0 || int(k) >= iconKindCount {
		k = iconIdle
	}
	if template {
		return icons[k].template
	}
	return icons[k].regular
}
