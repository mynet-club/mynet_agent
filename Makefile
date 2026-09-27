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
	@sh scripts/check-route-writers.sh
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

# ── 版本与产物（V2-3 / V2-4）─────────────────────────────────
#
# VERSION 必须匹配 ctl 的 parseFilename 版本段 `v[0-9a-z.\-]+`（以小写 v 开头）。
# 有 tag 用 tag；无 tag 用 v0.0.0-<sha>；工作区脏则带 -dirty（如实反映，不掩盖）。
# 显式传 VERSION=… 可覆盖（CI 出正式版时用 tag）。
GIT_DESC := $(shell git describe --tags --always --dirty 2>/dev/null)
ifeq ($(GIT_DESC),)
VERSION ?= v0.0.0-dev
else ifeq ($(shell echo '$(GIT_DESC)' | grep -c '^v'),1)
VERSION ?= $(GIT_DESC)
else
VERSION ?= v0.0.0-$(GIT_DESC)
endif

# 版本注入：两个变量都在「构建时由 -ldflags 覆盖」的注释里承诺过，此前从未接上，
# 导致所有产物 `--version` 恒输出 dev。
# -s -w 是**链接器**标志，必须经 -ldflags 传（直接给 go build 会报
# "flag provided but not defined: -s"）。-s 去符号表、-w 去 DWARF，产物小一半。
GO_LDFLAGS := -s -w \
	-X github.com/mynet-club/mynet_agent-V2/shared/utils.AppVersion=$(VERSION) \
	-X github.com/mynet-club/mynet_agent-V2/mynet.Version=$(VERSION) \
	-X main.version=$(VERSION)

# 平台矩阵：goos goarch <ctl 侧 arch 名>
# ⚠️ Go 的 mipsle 在 ctl 侧叫 mipsel —— 产物名必须用 ctl 的叫法，
#    否则 ctl 的 validateArch() 拒收、下载 404。
# 用冒号分隔而非空格：`for x in $(VAR)` 的展开会按 IFS 切词，变量里的引号是字面量
# 不被 shell 认，写成 "a b c" "d e f" 只会得到一堆单词而非三元组。
DIST_MATRIX := darwin:arm64:arm64 darwin:amd64:amd64 linux:amd64:amd64 linux:arm64:arm64 linux:mipsle:mipsel windows:amd64:amd64

# 打包器：darwin/windows 用 zip，其余 tgz（对齐 ctl PLATFORMS 的 ext）
PACK = sh scripts/pack.sh

# ── R4-1 / V2-3 / V2-4 产物 ─────────────────────────────────
#
# 命名 SSOT：前缀 mynet_agent_*（AGENTS.md §1.5，guard C3 强制），
# 完整文件名对齐 ctl 的 ReleaseDomainService::generateFilename：
#
#   {prefix}_{platform}_{arch}_{version}.{ext}
#
# ⚠️ 两个跨仓约定，改这里必须同步核对 ctl：
#   1) version 段的正则是 `v[0-9a-z.\-]+` —— 必须**以小写 v 开头**，
#      否则 ctl 的 parseFilename 解析失败、下载 404。
#   2) **Go 的 mipsle 在 ctl 侧叫 mipsel**（ARCHITECTURES 表）。产物名用 ctl 的叫法。
#
# 构建失败必须**炸出来**，不要 `|| true` / `|| continue` 吞掉：
# 静默失败会让 dist/ 看着齐全、实际缺件，正是「纸面达成」的典型形态（§7.1 D1）。
.PHONY: dist version
dist:
	@echo "▸ dist（VERSION=$(VERSION)）"
	@rm -rf dist && mkdir -p dist
	@set -e; for triple in $(DIST_MATRIX); do \
		goos=$${triple%%:*}; rest=$${triple#*:}; goarch=$${rest%%:*}; ctlarch=$${rest#*:}; \
		name=mynet_agent_$${goos}_$${ctlarch}_$(VERSION); \
		out=dist/$$name; \
		mkdir -p $$out; \
		ext=""; if [ "$$goos" = windows ]; then ext=".exe"; fi; \
		GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -o $$out/mynet$$ext ./cmd/mynet; \
		GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -tags embed -o $$out/mynetd$$ext ./cmd/mynetd; \
		GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -o $$out/mynet_gateway$$ext ./cmd/mynet_gateway; \
		case "$$goos" in \
		  darwin) \
		    if [ "$$(uname -s)" = Darwin ]; then \
		      GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=1 go build -ldflags "$(GO_LDFLAGS)" -o $$out/mynet_tray$$ext ./cmd/mynet_tray; \
		    else \
		      echo "  ⚠ 跳过 $$goos mynet_tray（systray 需在 macOS 宿主编，见 mynet_desktop 的 package-macos）"; \
		    fi; \
		    ;; \
		  windows) \
		    GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -o $$out/mynet_tray$$ext ./cmd/mynet_tray; \
		    ;; \
		esac; \
		cp mynetd/localapi/webui/index.html $$out/webui.html; \
		$(PACK) $$out; \
		echo "  $$name"; \
	done
	@$(MAKE) --no-print-directory checksums
	@echo "✓ dist/ 就绪：mynet_agent_{platform}_{arch}_$(VERSION).{tgz|zip} + checksums.txt"

# version 打印当前版本（供脚本与排障使用）
version:
	@echo $(VERSION)

# checksums.txt —— 校验和 SSOT。ctl 上传时服务端会再算一遍（ReleaseService.php:34），
# 这里生成一份是为了：① 离线分发时客户端可核；② 上传前自检产物完整。
.PHONY: checksums
checksums:
	@cd dist && { \
		if command -v sha256sum >/dev/null 2>&1; then SH="sha256sum"; \
		else SH="shasum -a 256"; fi; \
		$$SH *.tgz *.zip 2>/dev/null > checksums.txt || true; \
	}
	@test -s dist/checksums.txt || { echo "✗ checksums.txt 为空 —— 产物打包失败？"; exit 1; }
	@echo "  checksums.txt: $$(wc -l < dist/checksums.txt | tr -d ' ') 个产物"
