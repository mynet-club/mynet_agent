package upgrade

import (
	"strconv"
	"strings"
)

// CompareVersions 比较两个版本号，返回 -1（a<b）/ 0（相等）/ 1（a>b）。
//
// 为什么不能直接比字符串：`"0.10.0" > "0.9.0"` 在字典序下为 false ——
// 于是「挑最新版」会选中更旧的 0.9.0。升级选错版本是生产事故，且症状是
// 「升级后版本号变小」，不报错。
//
// 兼容 ctl 实际会出现的三种形态（ReleaseDomainService 的版本段正则
// 是 `v[0-9a-z.\-]+`，比严格 semver 宽）：
//
//	v1.2.3              标准
//	v1.6.0.a            多一段（GNB 历史用法）—— 视为 1.6.0 的预发布，低于 1.6.0
//	v0.0.0-e831eb8-dirty 带预发布标识
//
// 规则（贴近 semver，但对非数字段宽容）：
//  1. 去掉前导 v（大小写都认）
//  2. `+` 之后是构建元数据，按 semver 忽略
//  3. 第一个 `-` 之后是预发布标识：有预发布 < 无预发布
//  4. 主版本按 `.` 分段逐段数值比较；**缺段按 0 处理**（v1.2 == v1.2.0）
//  5. 尾部非数字段并入预发布：v1.6.0.a → 主版本 [1 6 0] + 预发布 "a"，
//     故 v1.6.0.a < v1.6.0（这是 GNB 的历史用法，不是第 4 个版本号）
func CompareVersions(a, b string) int {
	am, apre := splitVersion(a)
	bm, bpre := splitVersion(b)

	if c := compareSegments(am, bm); c != 0 {
		return c
	}

	// 主版本相同，比预发布：无预发布者更大
	switch {
	case apre == "" && bpre == "":
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return comparePrerelease(apre, bpre)
}

// splitVersion 拆出主版本段与预发布标识。
//
// 额外处理「主版本段数不等」：v1.6.0.a 的 `a` 不是数字，按 ctl 的用法它是
// 预发布标识而非第 4 个版本号，故把它连同其后内容归入 prerelease。
func splitVersion(v string) (main []string, pre string) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")

	// 构建元数据按 semver 忽略
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	// `-` 之后整体是预发布
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}

	if s == "" {
		return nil, pre
	}
	segs := strings.Split(s, ".")

	// 尾部非数字段并入预发布：v1.6.0.a → main=[1 6 0], pre="a"
	for len(segs) > 1 && !isNumeric(segs[len(segs)-1]) {
		extra := segs[len(segs)-1]
		segs = segs[:len(segs)-1]
		if pre == "" {
			pre = extra
		} else {
			pre = extra + "." + pre
		}
	}
	return segs, pre
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareSegments 逐段比较主版本。
//
// 进到这里时所有段都已是数字——splitVersion 已把尾部非数字段挪进预发布
// （v1.6.0.a → main=[1 6 0], pre="a"）。因此**缺段按 0 处理**：
// v1.2 == v1.2.0。段数不等本身不构成大小关系，那是 splitVersion 的职责。
func compareSegments(a, b []string) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(a) {
			ai, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			bi, _ = strconv.Atoi(b[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrerelease 比较预发布标识（按 `.` 分段，数字段数值比、其余字典序）。
func comparePrerelease(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		x, y := as[i], bs[i]
		switch {
		case isNumeric(x) && isNumeric(y):
			xi, _ := strconv.Atoi(x)
			yi, _ := strconv.Atoi(y)
			if xi != yi {
				if xi < yi {
					return -1
				}
				return 1
			}
		case isNumeric(x) && !isNumeric(y):
			return -1 // 数字标识 < 字母标识（semver §11）
		case !isNumeric(x) && isNumeric(y):
			return 1
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}

// LatestOf 从一组 artifact 中挑出指定组件的最新版。
// 返回 nil 表示该组件不在集合里。
func LatestOf(all []Artifact, comp Component) *Artifact {
	var best *Artifact
	for i := range all {
		if all[i].Component != comp {
			continue
		}
		if best == nil || CompareVersions(all[i].Version, best.Version) > 0 {
			best = &all[i]
		}
	}
	return best
}
