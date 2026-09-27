package main

import _ "embed"

// iconTemplatePNG macOS 菜单栏 Template 图（单色剪影，系统自动染深浅）。
//
//go:embed icon_template.png
var iconTemplatePNG []byte

// iconPNG 彩色图（Windows / 无 Template 时的回退）。
//
//go:embed icon.png
var iconPNG []byte
