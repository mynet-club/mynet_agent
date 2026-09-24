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

# ── R4-1 产物（命名 SSOT：mynet_agent_*）────────────────────
#
# 三个二进制：mynet（执行器）· mynetd（纯通信层，-tags embed 内嵌 WebUI）·
# mynet_gateway（边缘网关，V1-1 起才有可执行体）。
#
# 构建失败必须**炸出来**，不要 `|| true` / `|| continue` 吞掉：
# 静默失败会让 dist/ 看着齐全、实际缺件，正是「纸面达成」的典型形态（AGENTS.md §7.1 D1）。
.PHONY: dist
dist:
	@echo "▸ dist"
	@mkdir -p dist
	@for pair in "darwin arm64" "darwin amd64" "linux amd64" "linux arm64" "linux mipsle"; do \
		set -- $$pair; os=$$1; arch=$$2; \
		out=dist/mynet_agent_$${os}_$${arch}; \
		mkdir -p $$out; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -o $$out/mynet ./cmd/mynet || exit 1; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -tags embed -o $$out/mynetd ./cmd/mynetd || exit 1; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -o $$out/mynet_gateway ./cmd/mynet_gateway || exit 1; \
		cp mynetd/localapi/webui/index.html $$out/webui.html; \
		echo "  $$out"; \
	done
	@echo "✓ dist/ 就绪（mynet_agent_*：mynet + mynetd + mynet_gateway + webui.html）"
