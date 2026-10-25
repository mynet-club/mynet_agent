package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 同一进程里第二次抢锁必须失败：两个入口同时拉起时，后来者要立刻知道
// 已有实例在跑，而不是等 systray 起出第二个菜单栏图标。
func TestTryLockRejectsSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.lock")

	f, ok, err := tryLock(path)
	if err != nil || !ok {
		t.Fatalf("首次抢锁应成功：(ok=%v, err=%v)", ok, err)
	}
	defer unlock(f)

	f2, ok2, err2 := tryLock(path)
	if err2 != nil {
		t.Fatalf("第二个实例应是「已在运行」的拒绝，不是错误：%v", err2)
	}
	if ok2 {
		unlock(f2)
		t.Fatal("第二个实例也拿到了锁 —— 会出两个菜单栏图标")
	}
}

// 释放后必须能重新拿到：托盘退出后用户再点图标，得能正常起来。
func TestTryLockReacquireAfterUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.lock")

	f, ok, err := tryLock(path)
	if err != nil || !ok {
		t.Fatalf("首次抢锁失败：(ok=%v, err=%v)", ok, err)
	}
	unlock(f)

	f2, ok2, err2 := tryLock(path)
	if err2 != nil || !ok2 {
		t.Fatalf("释放后应能重新抢到：(ok=%v, err=%v)", ok2, err2)
	}
	unlock(f2)
}

// 锁文件落在用户配置目录的 MyNet/ 下，且目录按 0700 建 —— 托盘以登录用户
// 身份跑，写不进 root 的 $MYNET_HOME/var（见 intent.go 的同一理由）。
func TestTrayLockPathInUserConfigDir(t *testing.T) {
	isolateUserConfig(t)

	p, err := trayLockPath()
	if err != nil {
		t.Fatalf("trayLockPath: %v", err)
	}
	dir := filepath.Dir(p)
	if filepath.Base(dir) != "MyNet" {
		t.Errorf("锁目录 = %q，应在用户配置目录下的 MyNet/", dir)
	}
	if filepath.Base(p) != "tray.lock" {
		t.Errorf("锁文件名 = %q，want tray.lock", filepath.Base(p))
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("目录没建出来: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("目录权限 = %o，want 700（用户私有）", perm)
	}
}
