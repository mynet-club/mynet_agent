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
	@sh scripts/check-no-back-url.sh
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
# B4 版本戳：基线 / 补丁 / 构建者（mynet version 输出三句话）
# ⚠️ BUILD_INFO 不得含空格 —— -ldflags 会按空格拆参数（实测炸过）
GNB_BASELINE ?= 1.6.8-c411f14ee647
GNB_PATCHES  ?= p1-hot-restart-v2,p2-route-proxy
BUILD_INFO   ?= $(shell whoami)@$(shell hostname)_$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GO_LDFLAGS := -s -w \
	-X github.com/mynet-club/mynet_agent/shared/utils.AppVersion=$(VERSION) \
	-X github.com/mynet-club/mynet_agent/shared/utils.GnbBaseline=$(GNB_BASELINE) \
	-X github.com/mynet-club/mynet_agent/shared/utils.GnbPatchStack=$(GNB_PATCHES) \
	-X github.com/mynet-club/mynet_agent/shared/utils.BuildStamp=$(BUILD_INFO) \
	-X github.com/mynet-club/mynet_agent/mynet.Version=$(VERSION) \
	-X main.version=$(VERSION)

# 平台矩阵：platform:goos:goarch:ctlarch:tags
#   platform = 产物名与 packaging/<platform>/INSTALL.md 的平台段（对齐 ctl PLATFORMS）；
#     darwin 例外：安装文档归 mynet_desktop（H2），取 $(DESKTOP_INSTALL)；
#   goos/goarch = 交叉编译目标；
#   ctlarch  = ctl 侧 arch 名（Go mipsle → ctl mipsel）；
#   tags     = 追加的 -tags（openwrt = 选 procd/UCI 后端），空表示无。
#     ⚠️ tags 非空 = **瘦构建腿**（openwrt）：mynetd 只带 "$$tags"、**不带 embed** ——
#     WebUI 不进包，UI 由 LuCI 托管（SSOT §1.9 / build_openwrt.yml 同口径）。
#
# ⚠️ Go 的 mipsle 在 ctl 侧叫 mipsel —— 产物名必须用 ctl 的叫法，
#    否则 ctl 的 validateArch() 拒收、下载 404。
# 用冒号分隔而非空格：`for x in $(VAR)` 的展开会按 IFS 切词，变量里的引号是字面量
# 不被 shell 认，写成 "a b c" "d e f" 只会得到一堆单词而非多元组。
# openwrt 用 GOOS=linux + `-tags openwrt`：与通用 linux 包**同 GOOS 不同产物**，
# 产物名走 ctl 的 openwrt 平台（ext=tgz，见 packaging/openwrt/INSTALL.md）。
DIST_MATRIX := \
  darwin:darwin:arm64:arm64: \
  darwin:darwin:amd64:amd64: \
  linux:linux:amd64:amd64: \
  linux:linux:arm64:arm64: \
  linux:linux:mipsle:mipsel: \
  openwrt:linux:mipsle:mipsel:openwrt \
  freebsd:freebsd:amd64:amd64: \
  freebsd:freebsd:arm64:arm64: \
  openbsd:openbsd:amd64:amd64: \
  openbsd:openbsd:arm64:arm64: \
  windows:windows:amd64:amd64:

# 打包器：darwin/windows 用 zip，其余 tgz（对齐 ctl PLATFORMS 的 ext）
PACK = sh scripts/pack.sh
# ⚠️ webui.html 不进产物（G1-c 删除）：WebUI 由 embed 烘进 mynetd（webui_fs_embed.go），
#    拷贝零消费者（mynet/upgrade/install.go:34 不替换、desktop 侧 if -f / /nonfatal 容缺），
#    与安装文档「WebUI 已 embed，无 webui.html」口径一致（C1-S3）。
GUI_DIST ?= ../mynet_gui/dist/spa
GUI_SOURCE ?= ../mynet_gui
DESKTOP_SOURCE ?= ../mynet_desktop
# darwin 安装文档来源（旧 packaging/darwin 打包目录已随 H2 退役，文档归 mynet_desktop）
DESKTOP_INSTALL ?= ../mynet_desktop/docs/INSTALL.md

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
	@set -e; sh scripts/prepare-gui.sh "$(GUI_DIST)" mynetd/localapi/webui/gui; \
		trap 'rm -rf mynetd/localapi/webui/gui' EXIT; \
		go run -tags embed scripts/check-embedded-ui.go; \
		for entry in $(DIST_MATRIX); do \
		platform=$${entry%%:*}; r=$${entry#*:}; \
		goos=$${r%%:*}; r=$${r#*:}; \
		goarch=$${r%%:*}; r=$${r#*:}; \
		ctlarch=$${r%%:*}; tags=$${r#*:}; \
		name=mynet_agent_$${platform}_$${ctlarch}_$(VERSION); \
		out=dist/$$name; \
		mkdir -p $$out; \
		ext=""; if [ "$$goos" = windows ]; then ext=".exe"; fi; \
		if [ -n "$$tags" ]; then \
		  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -tags "$$tags" -o $$out/mynet$$ext ./cmd/mynet; \
		  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -tags "$$tags" -o $$out/mynetd$$ext ./cmd/mynetd; \
		else \
		  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -o $$out/mynet$$ext ./cmd/mynet; \
		  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -tags embed -o $$out/mynetd$$ext ./cmd/mynetd; \
		fi; \
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
		doc=packaging/$$platform/INSTALL.md; \
		if [ "$$platform" = darwin ]; then doc=$(DESKTOP_INSTALL); fi; \
		if [ -f "$$doc" ]; then \
		  cp "$$doc" $$out/INSTALL.md; \
		  source_desktop=-; if [ "$$platform" = darwin ]; then source_desktop="$(DESKTOP_SOURCE)"; fi; \
		  sh scripts/build-sources.sh . "$(GUI_SOURCE)" "$(GUI_DIST)" "$$source_desktop" "$$out/SOURCES.txt"; \
		else \
		  echo "  ✗ 缺 $$doc —— 分平台安装文档是产物一部分（darwin 归 mynet_desktop/docs/INSTALL.md）"; exit 1; \
		fi; \
		$(PACK) $$out; \
		echo "  $$name"; \
	done
	@$(MAKE) --no-print-directory checksums
	@echo "✓ dist/ 就绪：mynet_agent_{platform}_{arch}_$(VERSION).{tgz|zip} + checksums.txt"

# version 打印当前版本（供脚本与排障使用）
version:
	@echo $(VERSION)

# test-openwrt —— 跑 OpenWrt 后端（linux && openwrt）单测。
# ⚠️ 只能在 linux 目标上跑（OpenWrt 装配代码按 GOOS=linux 编译）；darwin/windows 只能交叉编译。
.PHONY: test-openwrt
test-openwrt:
	@if [ "$$(go env GOOS)" != linux ]; then \
		echo "· test-openwrt 跳过：当前 GOOS=$$(go env GOOS)，OpenWrt 后端只在 linux 上运行"; \
		echo "  仅编译校验：GOOS=linux GOARCH=mipsle go test -c -tags openwrt -o /dev/null ./mynet"; \
		GOOS=linux GOARCH=mipsle go test -c -tags openwrt -o /dev/null ./mynet; \
	else \
		go test -count=1 -tags openwrt ./mynet/; \
	fi

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
