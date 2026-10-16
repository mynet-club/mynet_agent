package main

// 文案清洗（playbook §5.2 数据与权限、P3 的 D8/D20）。
//
// 三条硬规则：
//   - one_line / detail **只解释事实**，去换行与控制字符；
//   - 菜单文本 ≤ MaxMenuRunes(48) 个 Unicode 字符，超长以省略号结尾；
//   - 完整原始文本（body / stderr / 路径 / argv）不进 tooltip、通知或日志。
//
// 为什么托盘侧要自己再挡一层密钥（D20，实测取证见 TRAY_T0_BASELINE §2.4）：
// daemon 的 redactSecrets 只认**英文**关键字（mynetd/localapi/wizard.go:376
// secretKeywords = token/refresh/priv/private/password/secret），
// 中文「私钥指纹 <64位hex>」原样透出。所以托盘侧遮蔽不是冗余，是承重的。
// 另外 runMynet 那条本地 exec 路径（CombinedOutput → tooltip）**完全没有**脱敏。
// 纵深防御：不假设上游一定挡得住。

import (
	"regexp"
	"strings"
	"unicode"
)

// MaxMenuRunes 菜单/tooltip 单行文本的 Unicode 字符上限。
const MaxMenuRunes = 48

// patShape 形如 FAKE-SECRET-PAT-aaaa… / GH_PAT-… 的带前缀令牌。无条件遮。
var patShape = regexp.MustCompile(`[A-Za-z0-9_]{3,}(?:-[A-Za-z0-9_]{3,})*-[A-Za-z0-9]{16,}`)

// longHex 16 位以上连续十六进制 —— 私钥/指纹的形状。
var longHex = regexp.MustCompile(`[0-9a-fA-F]{16,}`)

// secretKeyword 出现这些词才遮 longHex。
//
// 与 daemon 同规则（handlers.go:159「PAT 形状无条件遮，长 hex 只在带 secret
// 关键字时遮，公钥不误杀」）。不加这道闸门会把 16 位的十进制 node_id
// （如 4147069894021999）也遮掉，那是给人核对的，不是秘密。
var secretKeyword = regexp.MustCompile(`(?i)secret|token|password|passwd|private|priv[^a-z]|apikey|api_key|密钥|私钥|口令|凭据`)

// redacted 遮蔽占位。
const redacted = "⟨已隐藏⟩"

// ellipsis 截断结尾。
const ellipsis = "…"

// redactSecrets 遮住疑似凭据。
func redactSecrets(s string) string {
	s = patShape.ReplaceAllString(s, redacted)
	if secretKeyword.MatchString(s) {
		s = longHex.ReplaceAllString(s, redacted)
	}
	return s
}

// stripControl 去掉换行、制表与全部控制字符（含 ANSI 转义），压成单行。
// 注入面取证见 testdata/status_secret_injection.json：one_line 里塞了
// 真实换行 + BEL(\x07) + ESC(\x1b[31m)。
func stripControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// collapseSpaces 连续空白压成一个空格。
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			prevSpace = true
			continue
		}
		if prevSpace && b.Len() > 0 {
			b.WriteRune(' ')
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// clampRunes 按 **rune** 截断（§3.2 说的是 Unicode 字符，不是字节），超长加省略号。
func clampRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return ellipsis
	}
	return string(r[:max-1]) + ellipsis
}

// safeText 一次做完：去控制字符 → 遮密钥 → 压空白 → 截断。
// 任何要进菜单/tooltip/通知的文本都必须过它。
func safeText(s string, max int) string {
	s = stripControl(s)
	s = redactSecrets(s)
	s = strings.TrimSpace(collapseSpaces(s))
	return clampRunes(s, max)
}
