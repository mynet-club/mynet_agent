package utils

// gnb 套件构建一致性 —— R2 **死限**（不是告警）。
//
// R2（paths.md）：gnb_ctl/gnb_es 必须与 gnb 同构建同目录；异构建共享
// ctl block（gnb_conf_t 嵌在共享块里）会按错位布局解析 —— 混代部署的
// gnb_es 段错误（2026-11-18，libc strlen 扫垃圾字符串）即此根因：
// 08:05 只换了 gnb_es/ctl/crypto、旧 gnb 继续跑，09:50 崩。
//
// 判据（两段，用户裁定「死限」）：
//  1. 版本横幅（gnb_version.h 的 GNB_VERSION_STRING 字面量）必须一致 ——
//     跨源码代际共享块必错位；
//  2. 同批性（用户口径：编译时间上下 10 分钟内）—— 编译时间可见
//     （Build Time [日期,时刻]）按其比；被藏（-DGNB_SKIP_BUILD_TIME 的旧构建）
//     则以**落位 mtime** 代理（同批构建+同批落位）。超出 10 分钟窗 = 非同批，拒。
//     OpenWrt Makefile 已去掉 GNB_SKIP_BUILD_TIME（2026-11-18 用户裁定，弃可重现
//     构建换批次可判）——新构建直接比编译时间；存量 [Hidden] 件靠 mtime 代理过渡。
//
// 判据对象：gnb 与同目录 gnb_ctl/gnb_es（gnb_crypto 无横幅，仅入时间窗）。
// 无横幅且读不到 mtime 的桩（测试替身）不此判。
// 死限放行点：mynet utc driver-run（持进程咽喉）、driver Start/Reload、subject。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const gnbVersionBannerPrefix = "GNB version Dev "

// gnbBuildTimeWindow 同批性窗口（用户裁定 2026-11-18）：四件套一次 make 编译
// 在 1 分钟内出齐 —— 窗压到 1 分钟，混批（哪怕只隔几分钟单换一件）漏不进来。
const gnbBuildTimeWindow = 1 * time.Minute

// GnbSuiteVersion 读二进制内嵌的版本横幅；读不到返回空串。
func GnbSuiteVersion(binPath string) string {
	b, err := os.ReadFile(binPath)
	if err != nil {
		return ""
	}
	i := bytes.Index(b, []byte(gnbVersionBannerPrefix))
	if i < 0 {
		return ""
	}
	s := b[i:]
	j := 0
	for j < len(s) && s[j] >= 0x20 && s[j] < 0x7f {
		j++
	}
	return string(s[:j])
}

// GnbSuiteStamp 单件的批次时间戳：编译时间可见优先（Build Time [日期,时刻]），
// 被藏（[Hidden]）则退落位 mtime。都没有返回零值。
func GnbSuiteStamp(binPath string) time.Time {
	if b, err := os.ReadFile(binPath); err == nil {
		if i := bytes.Index(b, []byte("Build Time [")); i >= 0 {
			s := b[i+len("Build Time ["):]
			j := 0
			for j < len(s) && s[j] >= 0x20 && s[j] < 0x7f {
				j++
			}
			inner := string(s[:j])
			if k := strings.IndexByte(inner, ']'); k >= 0 {
				inner = inner[:k]
			}
			// __DATE__ 个位日期补空格（"Nov  8 2026"），__TIME__ 为 15:04:05
			if t, err := time.Parse("Jan _2 2006,15:04:05", inner); err == nil {
				return t
			}
		}
	}
	if fi, err := os.Stat(binPath); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// CheckGNBSuite R2 死限：横幅一致 + 同批性（≤10 分钟窗）才放行。
// 返回非 nil = 拒绝（调用方必须 fail loud，不许降级继续）。
func CheckGNBSuite(gnbBin string) error {
	gnbVer := GnbSuiteVersion(gnbBin)
	if gnbVer == "" {
		return nil // 测试桩/非标准件：不此判
	}
	dir := filepath.Dir(gnbBin)
	names := []string{"gnb_ctl", "gnb_es", "gnb_crypto"}
	stamps := map[string]time.Time{}
	for _, name := range names {
		p := filepath.Join(dir, ExeName(name))
		if _, err := os.Stat(p); err != nil {
			continue // 缺件归 R5
		}
		if v := GnbSuiteVersion(p); v != "" && v != gnbVer {
			return fmt.Errorf(
				"R2 死限（异构建拒跑）: %s=%q 与 gnb=%q 版本横幅不一致 —— 混代共享 ctl block 会按错位布局解析（段错误根因）；四件套必须同构建、同批落位、一起重启",
				p, v, gnbVer)
		}
		if t := GnbSuiteStamp(p); !t.IsZero() {
			stamps[p] = t
		}
	}
	if t := GnbSuiteStamp(gnbBin); !t.IsZero() {
		stamps[gnbBin] = t
	}
	if len(stamps) > 1 {
		times := make([]time.Time, 0, len(stamps))
		for _, t := range stamps {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		if span := times[len(times)-1].Sub(times[0]); span > gnbBuildTimeWindow {
			return fmt.Errorf(
				"R2 死限（非同批拒跑）: 套件批次时间差 %s 超过 %s 窗（最早 %s、最晚 %s）—— 四件套必须同构建、同批落位、一起重启；混批共享 ctl block 是段错误根因",
				span.Round(time.Second), gnbBuildTimeWindow,
				times[0].Format("2006-01-02 15:04:05"), times[len(times)-1].Format("2006-01-02 15:04:05"))
		}
	}
	return nil
}
