package mynet

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// parseUtunFromSocketFDInfo 从 macOS struct socket_fdinfo 原始字节中解析 utun 接口名。
// 抽成平台无关纯函数，方便在 Linux CI 上直接单测验证字节偏移与解析逻辑。
func parseUtunFromSocketFDInfo(buf []byte) (string, bool) {
	const (
		afSys       = 32 // AF_SYSTEM
		sysprotoCtl = 2  // SYSPROTO_CONTROL
		kindKernCtl = 6  // SOCKINFO_KERN_CTL
		offProto    = 180
		offFamily   = 184
		offKind     = 256
		offUnit     = 284
		offName     = 288
		lenName     = 96
		ctlName     = "com.apple.net.utun_control"
	)
	if len(buf) < offName+lenName {
		return "", false
	}
	proto := int32(binary.LittleEndian.Uint32(buf[offProto : offProto+4]))
	family := int32(binary.LittleEndian.Uint32(buf[offFamily : offFamily+4]))
	kind := int32(binary.LittleEndian.Uint32(buf[offKind : offKind+4]))
	if family != afSys || proto != sysprotoCtl || kind != kindKernCtl {
		return "", false
	}
	rawName := buf[offName : offName+lenName]
	if nul := bytes.IndexByte(rawName, 0); nul >= 0 {
		rawName = rawName[:nul]
	}
	if string(rawName) != ctlName {
		return "", false
	}
	unit := binary.LittleEndian.Uint32(buf[offUnit : offUnit+4])
	if unit == 0 {
		return "", false
	}
	return fmt.Sprintf("utun%d", unit-1), true
}
