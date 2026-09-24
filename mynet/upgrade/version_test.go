package upgrade

import "testing"

// 本组测试的核心判据：**字典序会给出相反答案**的那些用例必须显式覆盖。
// 若有人把 CompareVersions 换回 `a > b`，下面第一组会立刻失败。
func TestCompareVersionsNumericNotLexicographic(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 字典序下 "0.10.0" < "0.9.0"（'1' < '9'），语义上相反
		{"0.10.0", "0.9.0", 1},
		{"0.9.0", "0.10.0", -1},
		// 字典序下 "1.100.0" < "1.99.0"，语义上相反
		{"1.100.0", "1.99.0", 1},
		// 字典序下 "2.0.0" < "10.0.0"，语义上相反
		{"2.0.0", "10.0.0", -1},
		{"10.0.0", "2.0.0", 1},
		// 多段同样要按数值比
		{"1.2.10", "1.2.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		// 反对称性
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d（反对称性）", c.b, c.a, got, -c.want)
		}
	}
}

func TestCompareVersionsEqualAndPrefix(t *testing.T) {
	equal := [][2]string{
		{"1.2.3", "1.2.3"},
		{"v1.2.3", "1.2.3"},              // 前导 v 可省略
		{"V1.2.3", "v1.2.3"},             // 大小写 v 都可
		{" 1.2.3 ", "1.2.3"},             // 容忍两侧空白
		{"1.2", "1.2.0"},                 // 缺段视为 0
		{"1.2.3+build1", "1.2.3+build2"}, // 构建元数据按 semver 忽略
		{"1.2.3", "1.2.3+anything"},
	}
	for _, c := range equal {
		if got := CompareVersions(c[0], c[1]); got != 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want 0", c[0], c[1], got)
		}
	}
}

// 预发布 < 正式版（semver §11）
func TestCompareVersionsPrerelease(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1}, // 预发布里的数字段也要数值比
		// 数字标识 < 字母标识
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0.0-alpha", "1.0.0-1", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// ctl 实际会出现的形态（版本段正则是 `v[0-9a-z.\-]+`，比严格 semver 宽）
func TestCompareVersionsCtlShapes(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		// v1.6.0.a 是 GNB 历史用法：视为 1.6.0 的预发布，低于 1.6.0
		{"v1.6.0.a", "v1.6.0", -1, "多出的非数字段按预发布处理"},
		{"v1.6.0", "v1.6.0.a", 1, ""},
		{"v1.6.0.a", "v1.6.0.b", -1, "同为预发布则比标识"},
		{"v1.6.1.a", "v1.6.0.a", 1, "主版本先比"},
		// 构建产生的形态
		{"v0.0.0-e831eb8", "v0.0.0", -1, "带预发布标识 < 同主版本的正式版"},
		{"v0.0.0-e831eb8", "v0.0.0-abc1234", 1, "e > a"},
		{"v0.0.0-e831eb8-dirty", "v0.0.0-e831eb8", 1, "dirty 是额外的预发布段，段更多者更大"},
		// 跨形态
		{"v0.10.0", "v0.9.0", 1, "字典序会判反"},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d（%s）", c.a, c.b, got, c.want, c.why)
		}
	}
}

func TestCompareVersionsEmptyIsLowest(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"", "1.0.0"}, {"1.0.0", ""}, {"v", "1.0.0"},
	} {
		got := CompareVersions(c.a, c.b)
		if c.a == c.b {
			continue
		}
		// 空版本应当是最低的（比不过任何正常版本）
		if got == 0 {
			t.Errorf("CompareVersions(%q, %q) = 0，空版本不应等于正常版本", c.a, c.b)
		}
	}
}

func TestLatestOf(t *testing.T) {
	all := []Artifact{
		{Component: ComponentMynet, Version: "0.9.0", Platform: "linux"},
		{Component: ComponentMynet, Version: "0.10.0", Platform: "linux"}, // 语义上最新，字典序最旧
		{Component: ComponentMynet, Version: "0.2.0", Platform: "darwin"},
		{Component: ComponentGNB, Version: "1.6.0", Platform: "linux"},
	}

	got := LatestOf(all, ComponentMynet)
	if got == nil {
		t.Fatal("应找到 mynet 组件")
	}
	if got.Version != "0.10.0" {
		t.Errorf("LatestOf 选中 %q，want 0.10.0（字典序会错选 0.9.0）", got.Version)
	}

	if got := LatestOf(all, ComponentGNB); got == nil || got.Version != "1.6.0" {
		t.Errorf("LatestOf(gnb) = %+v, want 1.6.0", got)
	}
	if got := LatestOf(all, ComponentWireGuard); got != nil {
		t.Errorf("集合里没有 wireguard，应返回 nil，实际 %+v", got)
	}
	if got := LatestOf(nil, ComponentMynet); got != nil {
		t.Errorf("空集合应返回 nil，实际 %+v", got)
	}
}

// 排序稳定性：List() 依赖 CompareVersions 把同组件按版本降序。
func TestCompareVersionsUsableAsSortLess(t *testing.T) {
	versions := []string{"0.9.0", "0.10.0", "0.2.0", "0.10.0-rc1"}
	// 期望降序：0.10.0 > 0.10.0-rc1 > 0.9.0 > 0.2.0
	want := []string{"0.10.0", "0.10.0-rc1", "0.9.0", "0.2.0"}
	for i := 0; i < len(want)-1; i++ {
		if CompareVersions(want[i], want[i+1]) <= 0 {
			t.Errorf("期望 %q > %q", want[i], want[i+1])
		}
	}
	if len(versions) != len(want) {
		t.Fatal("用例不一致")
	}
}
