package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBinAt(t *testing.T, dir, name, banner, buildTime string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, ExeName(name))
	body := "\x7fELF-stub\x00garbage\x00"
	if banner != "" {
		body += banner + "\x00"
	}
	if buildTime != "" {
		body += "Build Time [" + buildTime + "]\x00"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

const banner168 = "GNB version Dev 1.6.8  protocol version 1.6.0"
const banner169 = "GNB version Dev 1.6.9  protocol version 1.6.0"

func TestGnbSuiteVersionExtractsBanner(t *testing.T) {
	dir := t.TempDir()
	p := writeBinAt(t, dir, "gnb", banner169, "", time.Time{})
	if got := GnbSuiteVersion(p); got != banner169 {
		t.Fatalf("got %q", got)
	}
	if got := GnbSuiteVersion(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("读不到应为空串，got %q", got)
	}
}

func TestGnbSuiteStampPrefersBuildTimeOverMtime(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2026, 11, 18, 8, 5, 0, 0, time.UTC)
	p := writeBinAt(t, dir, "gnb", banner169, "Nov 18 2026,03:51:17", mtime)
	got := GnbSuiteStamp(p)
	want := time.Date(2026, 11, 18, 3, 51, 17, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("编译时间可见时应优先用之：got %v want %v", got, want)
	}
	// Hidden / 无编译时间 → 退 mtime
	p2 := writeBinAt(t, dir, "gnb_ctl", banner169, "Hidden", mtime)
	if got := GnbSuiteStamp(p2); !got.Equal(mtime) {
		t.Fatalf("Hidden 应退 mtime: got %v want %v", got, mtime)
	}
}

// R2 死限：异源码代际（横幅不同）必拒 —— 2026-11-18 gnb_es 段错误方向。
func TestCheckGNBSuiteHardStopOnMismatchedBanner(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	gnb := writeBinAt(t, dir, "gnb", banner168, "Nov 18 2026,08:00:00", now)
	writeBinAt(t, dir, "gnb_ctl", banner168, "Nov 18 2026,08:00:03", now)
	writeBinAt(t, dir, "gnb_es", banner169, "Nov 18 2026,08:00:06", now)
	err := CheckGNBSuite(gnb)
	if err == nil {
		t.Fatal("异构建必须拒（死限），不能只告警")
	}
	if !strings.Contains(err.Error(), "R2 死限") || !strings.Contains(err.Error(), "gnb_es") {
		t.Fatalf("错误信息要指认异构建成员: %v", err)
	}
}

// 同批性死限（用户口径：编译时间 1 分钟窗 —— 一次 make 秒级出齐）：
// 同批放行、混批拒，窗边界锁死。
func TestCheckGNBSuiteHardStopOnMixedBatch(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 18, 8, 5, 0, 0, time.UTC)
	gnb := writeBinAt(t, dir, "gnb", banner169, "Nov 18 2026,08:05:00", base)
	writeBinAt(t, dir, "gnb_ctl", banner169, "Nov 18 2026,08:05:10", base)
	writeBinAt(t, dir, "gnb_es", banner169, "Nov 18 2026,08:05:25", base)
	if err := CheckGNBSuite(gnb); err != nil {
		t.Fatalf("同批（1 分钟窗内）应放行: %v", err)
	}

	// 窗边界：59s 放行、71s 拒（窗 = 1 分钟）
	writeBinAt(t, dir, "gnb", banner169, "Nov 18 2026,08:05:59", base)
	if err := CheckGNBSuite(gnb); err != nil {
		t.Fatalf("59 秒窗内应放行: %v", err)
	}
	writeBinAt(t, dir, "gnb", banner169, "Nov 18 2026,08:06:11", base)
	err := CheckGNBSuite(gnb)
	if err == nil {
		t.Fatal("超 1 分钟窗必须拒（死限）")
	} else if !strings.Contains(err.Error(), "非同批") {
		t.Fatalf("错误信息应指认非同批: %v", err)
	}

	// 事故同款形态（08:05 vs 11:25，小时级混批）
	writeBinAt(t, dir, "gnb", banner169, "Nov 18 2026,11:25:00", base)
	if err := CheckGNBSuite(gnb); err == nil {
		t.Fatal("小时级混批必须拒（死限）")
	}

	// 编译时间全 Hidden（存量旧构建形态）→ 退 mtime 代理，同样抓混批
	writeBinAt(t, dir, "gnb", banner169, "Hidden", base)
	writeBinAt(t, dir, "gnb_ctl", banner169, "Hidden", base)
	writeBinAt(t, dir, "gnb_es", banner169, "Hidden", base)
	if err := CheckGNBSuite(gnb); err != nil {
		t.Fatalf("全 Hidden 且同批 mtime 应放行: %v", err)
	}
	writeBinAt(t, dir, "gnb_es", banner169, "Hidden", base.Add(3*time.Hour))
	if err := CheckGNBSuite(gnb); err == nil {
		t.Fatal("Hidden 退 mtime 后混批（3 小时差）必须拒")
	}
}

// 无横幅的桩（测试替身）不此判 —— 不误伤。
func TestCheckGNBSuiteSkipsStubs(t *testing.T) {
	dir := t.TempDir()
	gnb := writeBinAt(t, dir, "gnb", "", "", time.Time{})
	writeBinAt(t, dir, "gnb_ctl", banner169, "", time.Time{})
	if err := CheckGNBSuite(gnb); err != nil {
		t.Fatalf("gnb 无横幅不此判: %v", err)
	}
}
