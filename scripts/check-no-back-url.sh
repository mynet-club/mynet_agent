#!/bin/sh
# 守护：后端 API 地址不得出现在任何出货面。
#
# 裁定（2026-10-12）：全面取消 api_base_url —— agent/desktop/gui 一律经 gateway
# 访问后端；后端地址不下发、不入参、不落配置。三仓皆 public，字面量进代码 = 暴露。
#
# 扫描面：出货源码/配置/文档（go/lua/html/js/json/md/sh）。
# 豁免：_test.go（要用字面量断言「不含」）、本守护自身、dist/（构建产物，make dist 再生）。
# 禁止项：back 主机字面量 + 三个端点标识符（新代码不得再引入）。
set -e

fail=0
hits=$(grep -rnE 'api\.mynet\.club|APIBaseURL|DefaultAPIBase|MYNET_API_BASE_URL' \
	--include='*.go' --include='*.lua' --include='*.html' --include='*.js' \
	--include='*.json' --include='*.md' --include='*.sh' \
	. 2>/dev/null \
	| grep -v '_test\.go:' \
	| grep -v 'scripts/check-no-back-url\.sh:' \
	| grep -v '^\./dist/' \
	| grep -v '^\./docs/' || true)

if [ -n "$hits" ]; then
	echo "✗ 后端 API 地址泄漏到出货面（云通信一律经 gateway，地址不下发）:"
	echo "$hits"
	fail=1
fi

# 旧参数面不得复活：--api / --api-base 只允许出现在「已取消」的报错与文档删改记录里
flaghits=$(grep -rnE '\-\-api-base|\-\-api[ =]' \
	--include='*.go' --include='*.lua' --include='*.html' --include='*.js' \
	--include='*.md' --include='*.sh' \
	. 2>/dev/null \
	| grep -v '已取消' \
	| grep -v 'scripts/check-no-back-url\.sh:' \
	| grep -v '^\./dist/' \
	| grep -v '^\./docs/' || true)

if [ -n "$flaghits" ]; then
	echo "✗ --api/--api-base 参数面复活（已取消：终端统一经 gateway 中继）:"
	echo "$flaghits"
	fail=1
fi

if [ "$fail" -eq 0 ]; then
	echo "✓ check-no-back-url：出货面无后端 API 地址/旧参数面"
fi
exit $fail
