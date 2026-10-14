package mynet

// config keys rotate —— 密钥轮换一条龙（G3 缺口⑤ agent 侧）。
//
// 把「换钥 → 失效 → 对端补拉 → 凭证重绑」串成一条路（2026-10-14 治理裁定）：
//
//	① keys generate   生成新密钥（本地，唯一写面 keys.WriteNodeKeys）
//	② keys sync       上传新公钥（PUT /nodes/{id}/keys，自动带 X-Device-Fingerprint
//	                   —— 后端在换钥成功后把 token 重绑到该指纹）+ 发布 bundle
//	③ config pull     补拉新修订 bundle（--download-only）
//	④ refresh-config  按新 bundle 渲染驱动配置
//
// 为什么③不能省（复核修正，原口径只给三环 generate→sync→refresh-config）：
// refresh-config 会校验「本机私钥 ↔ bundle 公钥」是一对（cmd_refresh.go:393-400），
// 而 sync 之后本地 var/bundle.json 还是**旧修订**（公钥是旧的）——不补拉就渲染，
// 第④环必然报「bundle 是旧修订」失败。③正是对端补拉的本机侧对应动作。
//
// 输出契约：stdout 只吐**一个** JSON（localapi 契约，同 config face 其余命令），
// 人读进度走 stderr。失败时 JSON 带 failed_step/step_index，stderr 明说卡在哪一环。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// rotateStep 轮换的一环。run 返回 (该环的结构化结果, 退出码)。
type rotateStep struct {
	key   string
	human string
	run   func(args []string) int
}

// rotateSteps 编排面（sync/pull 是网络环，测试可注入替身）。
type rotateSteps struct {
	generate func(args []string) int
	sync     func(args []string) int
	pull     func(args []string) int
	refresh  func(args []string) int
}

func defaultRotateSteps() rotateSteps {
	return rotateSteps{
		generate: configFaceKeysGenerate,
		sync:     func([]string) int { return configFaceKeysSync() },
		// --download-only：只拉新修订，不投影 —— 渲染是第④环（refresh-config）的事
		pull:    func([]string) int { return configFacePullOnly(true) },
		refresh: func([]string) int { return cmdRefresh([]string{"refresh-config", "--auto"}) },
	}
}

func configFaceKeysRotate(args []string) int {
	return configFaceKeysRotateRun(args, defaultRotateSteps())
}

func configFaceKeysRotateRun(args []string, st rotateSteps) int {
	steps := []rotateStep{
		{key: "generate", human: "生成新密钥（keys generate）", run: st.generate},
		{key: "sync", human: "上传新公钥并重绑凭证（keys sync，自动带新指纹）", run: st.sync},
		{key: "pull", human: "补拉新修订 bundle（config pull --download-only）", run: st.pull},
		{key: "refresh", human: "按新 bundle 渲染驱动配置（refresh-config）", run: st.refresh},
	}

	done := map[string]any{}
	total := len(steps)
	for i, s := range steps {
		fmt.Fprintf(os.Stderr, "→ [%d/%d] %s\n", i+1, total, s.human)
		raw, code := captureStepOutput(func() int { return s.run(args) })
		done[s.key] = stepResult(raw)
		if code != 0 {
			msg := fmt.Sprintf("轮换中止：卡在第 %d 环（%s）", i+1, s.human)
			fmt.Fprintf(os.Stderr, "✗ %s\n", msg)
			// 失败退出码沿用该环的非零码（打印 JSON 失败不影响归因）
			_ = printJSON(map[string]any{
				"error":       "rotate_failed",
				"message":     msg,
				"failed_step": s.key,
				"step_index":  i + 1,
				"steps":       done,
			})
			return code
		}
		fmt.Fprintf(os.Stderr, "✓ [%d/%d] %s\n", i+1, total, s.human)
	}

	fmt.Fprintln(os.Stderr, "✓ 密钥轮换完成：新公钥已上传（后端已失效重发），凭证已重绑，配置已重渲染")
	return printJSON(map[string]any{
		"rotated": true,
		"steps":   done,
	})
}

// captureStepOutput 跑一环并收回它写到 stdout 的输出（config face 各命令都对
// stdout 吐 JSON，refresh-config 吐的是人读行 —— 统一收进来再按形态收纳）。
func captureStepOutput(f func() int) (string, int) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", f()
	}
	os.Stdout = w
	code := f()
	_ = w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	_ = r.Close()
	return sb.String(), code
}

// stepResult 一环的结构化结果：能解析成 JSON 就按对象收纳，否则存原文行。
func stepResult(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
		return m
	}
	var lines []string
	for _, ln := range strings.Split(trimmed, "\n") {
		lines = append(lines, strings.TrimRight(ln, "\r"))
	}
	return map[string]any{"lines": lines}
}
