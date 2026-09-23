# MyNet repo 门禁 —— 统一入口（各 repo 一份，从 knowledge-base/templates/ 复制）
# 用法：make lint | make test | make guard | make check | make install-hooks
#
# 守护机制（AGENTS.md §2.6）：
#   make        = 显式入口，随时可跑
#   pre-commit  → make guard   （轻量：安全红线 + 文件名，秒级）
#   pre-push    → make check   （重量：lint + test）

.PHONY: lint test guard check install-hooks

# ── 语言适配：按 repo 类型放开对应块 ────────────────────────────
lint:
	@echo "▸ lint"
	@gofmt -l . | tee /tmp/.lint.out | grep . && { echo "✗ gofmt 未格式化"; exit 1; } || true
	@go vet ./...
	# PHP: vendor/bin/pint --test   || vendor/bin/php-cs-fixer fix --dry-run
	# Vue: npm run lint
	# Lua: luac -p *.lua

test:
	@echo "▸ test"
	@go test ./...
	# PHP: php vendor/bin/phpunit
	# Vue: npm run test:unit

guard:
	@echo "▸ guard（安全红线）"
	@sh .githooks/guard.sh

check: guard lint test
	@echo "✓ check 通过"

install-hooks:
	@git config core.hooksPath .githooks
	@chmod +x .githooks/*
	@echo "✓ hooks 已安装（core.hooksPath = .githooks）"
