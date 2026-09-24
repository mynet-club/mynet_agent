package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ── 词汇守护（替代已退休的 mynet_ctl/scripts/check-command-vocabulary.php）──
//
// 为什么改成 Go 测试：节点/客户端**没有 PHP 环境**，PHP 脚本无法在 agent 仓里跑；
// 而命令名的唯一真值来源在 agent（Plan 的 switch）。守护必须长在真值旁边。
//
// 历史教训（CPI-4）：back 单方面声明了一整套 agent 不认的命令
// （mynet.boot / shutdown / regenConfig / reloadConfigs / plugin.proxy.reload），
// 其中 mynet.reboot 让「重启节点」整整失效，且**只在节点本地日志留一行**，
// back 侧完全看不出来。所以本组测试的核心不是「能映射」，而是
// 「**映射不了时必须显式失败**」。

// backCommandsSnapshot 是 back 侧实际会下发的 command 全集快照。
//
// 来源：mynet_back 全仓 grep `'mynet\.[a-z.]*'`（2026-09-24，13 条）。
// ⚠️ 这是**快照**，不是自动同步：back 新增命令时本测试不会自动发现。
// 真正的兜底是运行期——OpFromBackCommand 返回 ok=false 时调用方必须显式上报，
// 于是 back 会收到「unknown command」而不是静默无事发生。
var backCommandsSnapshot = []string{
	"mynet.gnb.refresh.address",
	"mynet.gnb.refresh.route",
	"mynet.node.update",
	"mynet.restart",
	"mynet.route.refresh",
	"mynet.route.update",
	"mynet.service.restart",
	"mynet.service.start",
	"mynet.service.stop",
	"mynet.start",
	"mynet.stop",
	"mynet.wg.refresh.address",
	"mynet.wg.refresh.route",
}

// back 下发的每一条命令都必须能映射到 agent 认识的 Op。
func TestEveryBackCommandMapsToKnownOp(t *testing.T) {
	for _, cmd := range backCommandsSnapshot {
		op, ok := OpFromBackCommand(cmd)
		if !ok {
			t.Errorf("%s 无法映射到任何 Op —— back 下发它时节点会静默失效（CPI-4 的复发形态）", cmd)
			continue
		}
		if !IsKnownOp(op) {
			t.Errorf("%s 映射到 %q，但 Plan() 不认识它", cmd, op)
			continue
		}
		// 映射结果必须真能被 Plan 翻译成 argv
		if _, planned := Plan(Request{ID: "t", Op: op}, "node-1"); !planned {
			t.Errorf("%s → op %q 无法被 Plan() 展开成 argv", cmd, op)
		}
	}
}

// 9 条遵循 `mynet.<op>` 常规规则；4 条走显式别名表。
// 分开断言，这样别名表被误删时能立刻看出是哪一类坏了。
func TestRegularCommandsUsePrefixRule(t *testing.T) {
	regular := []string{
		"mynet.gnb.refresh.address", "mynet.gnb.refresh.route",
		"mynet.node.update", "mynet.route.refresh", "mynet.route.update",
		"mynet.service.start", "mynet.service.stop",
		"mynet.wg.refresh.address", "mynet.wg.refresh.route",
	}
	for _, cmd := range regular {
		want := strings.TrimPrefix(cmd, "mynet.")
		got, ok := OpFromBackCommand(cmd)
		if !ok || got != want {
			t.Errorf("%s → (%q, %v)，want (%q, true)", cmd, got, ok, want)
		}
	}
}

func TestLegacyCommandAliases(t *testing.T) {
	cases := map[string]string{
		"mynet.start":           OpServiceStart,
		"mynet.stop":            OpServiceStop,
		"mynet.restart":         OpServiceReload,
		"mynet.service.restart": OpServiceReload,
	}
	for cmd, want := range cases {
		got, ok := OpFromBackCommand(cmd)
		if !ok {
			t.Errorf("%s 应映射到 %q，实际 ok=false", cmd, want)
			continue
		}
		if got != want {
			t.Errorf("%s → %q, want %q", cmd, got, want)
		}
	}
}

// 不认识的命令必须 ok=false —— 这是「不静默失效」的核心保证。
func TestUnknownCommandIsRejectedNotIgnored(t *testing.T) {
	bad := []string{
		"",                          // 空
		"   ",                       // 只有空白
		"mynet.",                    // 只有前缀
		"mynet.reboot",              // 历史上真实漂过的那一条
		"mynet.boot",                // CPI-4 残留
		"mynet.shutdown",            // CPI-4 残留
		"mynet.plugin.proxy.reload", // 已下架的 mynet_proxy 域
		"node.update",               // 缺 mynet. 前缀
		"mynet.node.update.extra",   // 多一段
		"Mynet.node.update",         // 大小写不符
	}
	for _, cmd := range bad {
		if op, ok := OpFromBackCommand(cmd); ok {
			t.Errorf("%q 不应被接受，却映射到了 %q", cmd, op)
		}
	}
}

// knownOps 必须与 Plan() 的 switch 真同步。
//
// 用 go/ast 解析本包源码，而不是靠人记得改两处——否则 knownOps 会悄悄落后，
// 表现为「Plan 支持某 op，但 OpFromBackCommand 拒绝它」。
func TestKnownOpsMatchesPlanSwitch(t *testing.T) {
	inPlan := planSwitchOps(t)

	// 方向一：knownOps 里每个都必须在 Plan 的 switch 里（否则 IsKnownOp 说谎）
	for _, op := range knownOps {
		if !inPlan[op] {
			t.Errorf("knownOps 列了 %q，但 Plan() 的 switch 里没有它", op)
		}
	}
	// 方向二：Plan 的 switch 里每个都必须在 knownOps 里（否则新 op 会被拒）
	listed := map[string]bool{}
	for _, op := range knownOps {
		listed[op] = true
	}
	for op := range inPlan {
		if !listed[op] {
			t.Errorf("Plan() 支持 %q，但 knownOps 没列它 —— 该 op 会被 OpFromBackCommand 拒掉", op)
		}
	}
}

// planSwitchOps 解析 commands.go，收集 Plan() 里 switch req.Op 的全部 case 常量名，
// 再解析成常量的字符串值。
func planSwitchOps(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "commands.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 commands.go: %v", err)
	}

	// 先收集 Op* 常量 → 字面值
	constValues := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			constValues[vs.Names[0].Name] = strings.Trim(lit.Value, `"`)
		}
	}

	// 再找 Plan 函数里 switch 的 case 常量
	out := map[string]bool{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "Plan" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			for _, c := range sw.Body.List {
				cc, ok := c.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					switch e := expr.(type) {
					case *ast.Ident:
						// case OpDetector: —— 常量名，查其字面值
						if v, found := constValues[e.Name]; found {
							out[v] = true
						}
					case *ast.BasicLit:
						// case "sneaky.newop": —— 裸字符串字面量。
						// 必须一并收集，否则有人往 Plan 里塞字面量 case 就能绕过本守护
						// （变异测试实测过：只收 Ident 时这条漏网）。
						if e.Kind == token.STRING {
							out[strings.Trim(e.Value, `"`)] = true
						}
					}
				}
			}
			return true
		})
	}

	if len(out) == 0 {
		t.Fatal("没从 Plan() 解析出任何 case —— 解析逻辑失效，本守护形同虚设")
	}
	return out
}

// BackCommands() 必须与「常规规则 + 别名表」自洽，供文档与守护使用。
func TestBackCommandsIsCompleteAndSorted(t *testing.T) {
	got := BackCommands()
	if len(got) != len(knownOps)+len(legacyCommandAliases) {
		t.Errorf("BackCommands() 返回 %d 条，want %d", len(got), len(knownOps)+len(legacyCommandAliases))
	}

	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			t.Errorf("BackCommands() 有重复项 %q", c)
		}
		seen[c] = true
		if !strings.HasPrefix(c, "mynet.") {
			t.Errorf("BackCommands() 的 %q 缺 mynet. 前缀", c)
		}
		if _, ok := OpFromBackCommand(c); !ok {
			t.Errorf("BackCommands() 列出的 %q 自己映射不回去", c)
		}
	}

	// 快照里的每一条都必须在 BackCommands() 里
	for _, cmd := range backCommandsSnapshot {
		if !seen[cmd] {
			t.Errorf("back 会下发 %q，但 BackCommands() 没列它", cmd)
		}
	}
}
