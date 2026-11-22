//go:build darwin

package mynet

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS XNU proc_info(2) 系统调用常量（核对自 MacOSX SDK sys/proc_info.h 与 sys/syscall.h）。
const (
	darwinSysProcInfo     = 336 // SYS_proc_info
	procInfoCallPIDInfo   = 2   // PROC_INFO_CALL_PIDINFO
	procInfoCallPIDFDInfo = 3   // PROC_INFO_CALL_PIDFDINFO
	procPIDListFDs        = 1   // PROC_PIDLISTFDS
	procPIDFDSocketInfo   = 3   // PROC_PIDFDSOCKETINFO
	proxFDTypeSocket      = 2   // PROX_FDTYPE_SOCKET
	procFDInfoSize        = 8   // sizeof(struct proc_fdinfo)
	socketFDInfoSize      = 792 // sizeof(struct socket_fdinfo)
	afSystem              = 32  // AF_SYSTEM
	sysprotoControl       = 2   // SYSPROTO_CONTROL
	sockinfoKernCtl       = 6   // SOCKINFO_KERN_CTL
	offSocketProtocol     = 180 // offsetof(socket_fdinfo, psi.soi_protocol)
	offSocketFamily       = 184 // offsetof(socket_fdinfo, psi.soi_family)
	offSocketKind         = 256 // offsetof(socket_fdinfo, psi.soi_kind)
	offKernCtlUnit        = 284 // offsetof(socket_fdinfo, psi.soi_proto.pri_kern_ctl.kcsi_unit)
	offKernCtlName        = 288 // offsetof(socket_fdinfo, psi.soi_proto.pri_kern_ctl.kcsi_name)
	lenKernCtlName        = 96  // MAX_KCTL_NAME
	utunControlName       = "com.apple.net.utun_control"
)

// utunIfaceForPID 通过 macOS 原生 SYS_proc_info 系统调用查询指定 PID 持有的 utunN 接口名。
// 零子进程、无需 lsof/ifconfig，直接从内核读取进程的 PF_SYSTEM/SYSPROTO_CONTROL socket。
func utunIfaceForPID(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	// 先探测所需缓冲区大小
	n, _, errno := unix.Syscall6(
		darwinSysProcInfo,
		uintptr(procInfoCallPIDInfo),
		uintptr(pid),
		uintptr(procPIDListFDs),
		0,
		0,
		0,
	)
	if errno != 0 || n == 0 {
		return "", false
	}
	// 预留少量余量防止并发打开新 fd 截断
	bufSize := int(n) + 16*procFDInfoSize
	fdBuf := make([]byte, bufSize)
	got, _, errno := unix.Syscall6(
		darwinSysProcInfo,
		uintptr(procInfoCallPIDInfo),
		uintptr(pid),
		uintptr(procPIDListFDs),
		0,
		uintptr(unsafe.Pointer(&fdBuf[0])),
		uintptr(len(fdBuf)),
	)
	if errno != 0 || got < procFDInfoSize {
		return "", false
	}

	sockBuf := make([]byte, socketFDInfoSize)
	count := int(got) / procFDInfoSize
	for i := 0; i < count; i++ {
		off := i * procFDInfoSize
		fd := int32(binary.LittleEndian.Uint32(fdBuf[off : off+4]))
		fdType := binary.LittleEndian.Uint32(fdBuf[off+4 : off+8])
		if fdType != proxFDTypeSocket || fd < 0 {
			continue
		}
		sn, _, serr := unix.Syscall6(
			darwinSysProcInfo,
			uintptr(procInfoCallPIDFDInfo),
			uintptr(pid),
			uintptr(procPIDFDSocketInfo),
			uintptr(uint64(fd)),
			uintptr(unsafe.Pointer(&sockBuf[0])),
			uintptr(len(sockBuf)),
		)
		if serr != 0 || int(sn) < offKernCtlName+lenKernCtlName {
			continue
		}
		if ifname, ok := parseUtunFromSocketFDInfo(sockBuf[:sn]); ok {
			return ifname, true
		}
	}
	return "", false
}

// activeGnbUtunIface 返回当前托管 gnb（由运行投影 gnb.pid 标识）真实持有的 utunN。
// 找不到或不是 macOS 返回 ""，调用方回落按 IP 反查。
func activeGnbUtunIface(home string) string {
	dir := gnbProjectionDir(home)
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "gnb.pid"))
	if err != nil {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return ""
	}
	if ifname, ok := utunIfaceForPID(pid); ok {
		return ifname
	}
	return ""
}
