-- luasrc/model/mynet/gnb_installer.lua
-- GNB 自动检测与安装模块
--
-- 学习自 mynet_tui 的启动检测逻辑：
--   internal/tui/startup_helper.go  autoInstallDrivers()
--   internal/application/gnb_service.go  UpgradeGNBToLatest()
--   internal/common/platform/arch.go  GetRuntimeArch()
--   internal/tools/updater/updater.go  SelectArtifactNew / SelectAssetSmart
--
-- 防重入：通过锁文件 /var/run/mynet_gnb_install.lock (存 PID)
-- 后台执行：shell 脚本 &，进度写入 LOG_FILE
-- 架构检测：uname -m + file /bin/busybox (soft/hardfloat) + /proc/cpuinfo

local M = {}

local util = require("luci.model.mynet.util")
local mynet_config = require("luci.model.mynet.config")

-- ─────────────────────────────────────────────────────────────
-- 常量
-- ─────────────────────────────────────────────────────────────
M.LOCK_FILE   = "/var/run/mynet_gnb_install.lock"
M.LOG_FILE    = "/var/log/mynet_gnb_install.log"
M.APPS_INDEX  = mynet_config.DEFAULT_CTL_URL .. "/d/apps.json"
M.SCRIPT_PATH = "/tmp/mynet_install_gnb.sh"

-- ─────────────────────────────────────────────────────────────
-- 依赖预检与预装
-- 在 start_auto_install 之前调用，确保环境就绪。
-- 参考 mynet_tui startup_checker.go + mynet-fix-curl 脚本
-- ─────────────────────────────────────────────────────────────

-- check_deps: 检查并预装所有必要依赖
-- 返回 { ok=bool, steps=[{name,status,msg}], errors=[] }
function M.check_deps()
    local result = { ok = true, steps = {}, errors = {} }

    local function step(name, ok_flag, msg)
        result.steps[#result.steps+1] = { name = name, status = ok_flag and "ok" or "warn", msg = msg }
        if not ok_flag then result.errors[#result.errors+1] = name .. ": " .. msg end
    end

    -- 1. bash（GNB 安装脚本需要）
    local bash_path = util.trim(util.exec("which bash 2>/dev/null") or "")
    if bash_path == "" then
        util.exec("opkg install bash -q 2>/dev/null")
        bash_path = util.trim(util.exec("which bash 2>/dev/null") or "")
    end
    step("bash", bash_path ~= "", bash_path ~= "" and bash_path or "安装失败")

    -- 2. kmod-tun（VPN tun 设备）
    local tun_ok = util.trim(util.exec("lsmod 2>/dev/null | grep -c '^tun'") or "0") ~= "0"
    if not tun_ok then
        util.exec("opkg install kmod-tun -q 2>/dev/null; modprobe tun 2>/dev/null")
        tun_ok = util.trim(util.exec("lsmod 2>/dev/null | grep -c '^tun'") or "0") ~= "0"
    end
    step("kmod-tun", tun_ok, tun_ok and "已加载" or "安装/加载失败（VPN 可能无法使用 tun 设备）")

    -- 3. curl TLS 修复（mbedTLS → GnuTLS，解决 Let's Encrypt 握手失败）
    -- 注意：OpenWrt 24.x 上 libcurl-gnutls4 ABI 可能与 curl binary 不兼容，
    -- 因此先检测 curl 是否已经可用，可用则跳过替换。
    local curl_lib = "/usr/lib/libcurl.so.4"
    local gnutls_lib = "/usr/lib/libcurl-gnutls.so.4"
    -- 先测试 curl 能否正常运行（无符号冲突）
    local curl_works = util.trim(util.exec(
        "curl --version 2>/dev/null | grep -c 'curl'") or "0") ~= "0"
    if curl_works then
        step("curl-tls", true, "curl 可用（跳过 gnutls 替换）")
    else
        local gnutls_installed = util.trim(util.exec(
            "opkg list-installed 2>/dev/null | grep -c 'libcurl-gnutls'") or "0") ~= "0"
        if not gnutls_installed then
            util.exec("opkg install libcurl-gnutls4 -q 2>/dev/null")
            gnutls_installed = io.open(gnutls_lib, "r") ~= nil
        end
        if gnutls_installed then
            -- 检查 symlink 是否已指向 gnutls
            local link_target = util.trim(util.exec("readlink " .. curl_lib .. " 2>/dev/null") or "")
            if not link_target:find("gnutls") then
                util.exec("[ ! -f " .. curl_lib .. ".mbedtls.bak ] && cp -p " .. curl_lib
                    .. " " .. curl_lib .. ".mbedtls.bak 2>/dev/null; true")
                util.exec("ln -sf " .. gnutls_lib .. " " .. curl_lib)
            end
            -- 验证替换后 curl 是否正常
            local curl_ok_after = util.trim(util.exec(
                "curl --version 2>/dev/null | grep -c 'curl'") or "0") ~= "0"
            if curl_ok_after then
                step("curl-tls", true, "GnuTLS backend 已启用")
            else
                -- 替换后 curl 坏了（ABI 不兼容），回滚到原始
                util.exec("ln -sf /usr/lib/libcurl.so.4.8.0 " .. curl_lib .. " 2>/dev/null; true")
                step("curl-tls", false, "libcurl-gnutls4 ABI 不兼容，已回滚（HTTPS 可能受影响）")
            end
        else
            step("curl-tls", false, "libcurl-gnutls4 安装失败（HTTPS 可能受影响）")
        end
    end

    -- 4. ca-bundle
    local ca_ok = util.trim(util.exec(
        "[ -d /etc/ssl/certs ] && ls /etc/ssl/certs/*.pem 2>/dev/null | wc -l") or "0") ~= "0"
    if not ca_ok then
        util.exec("opkg install ca-bundle -q 2>/dev/null")
        ca_ok = util.trim(util.exec(
            "opkg list-installed 2>/dev/null | grep -c 'ca-bundle'") or "0") ~= "0"
    end
    step("ca-bundle", ca_ok, ca_ok and "CA 证书包就绪" or "安装失败（HTTPS CA 验证可能受影响）")

    -- 5. gnb_ctl 二进制（在 start_auto_install 里处理，这里仅状态记录）
    local gnb_ok = M.check_gnb_exists()
    step("gnb_ctl", gnb_ok, gnb_ok and (util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl") or "未安装，将自动下载")

    return result
end

-- ─────────────────────────────────────────────────────────────
-- 平台 / 架构检测
-- 参考 arch.go GetRuntimeArch() + detectFPU()
-- ─────────────────────────────────────────────────────────────

-- detect_fpu_from_file: 通过 file(1) 命令检测二进制 FPU 类型
-- 返回 "hard" | "soft" | nil
local function detect_fpu_from_file()
    local targets = { "/bin/busybox", "/bin/sh", "/usr/bin/awk" }
    for _, t in ipairs(targets) do
        local out = util.trim(util.exec("file '" .. t .. "' 2>/dev/null | head -1") or "")
        if out ~= "" then
            local low = out:lower()
            if low:find("hard.?float") or low:find("hardfp") then
                return "hard"
            elseif low:find("soft.?float") or low:find("softfp") then
                return "soft"
            end
        end
    end
    return nil
end

-- detect_fpu_from_cpuinfo: 通过 /proc/cpuinfo Features 行检测
-- 返回 "hard" | "soft"
local function detect_fpu_from_cpuinfo()
    local cpuinfo = util.read_file("/proc/cpuinfo") or ""
    local features_line = cpuinfo:match("[Ff]eatures%s*:%s*([^\n]+)")
    if features_line then
        local low = features_line:lower()
        if low:find("vfpv") or low:find("neon") then
            return "hard"
        end
    end
    -- MIPS FPU 行
    if cpuinfo:lower():find("fpu%s*:%s*yes") then
        return "hard"
    end
    return "soft"
end

-- detect_openwrt_target: 检测 OpenWrt 运行目标，返回 { uname_m, fpu, gnb_arch, os_name }
-- gnb_arch 格式与 GNB release 包名匹配（如 aarch64、armv7-hardfp、mipsel-softfp）
function M.detect_openwrt_target()
    local res = { uname_m = "", fpu = "none", gnb_arch = "", os_name = "linux" }

    res.uname_m = util.trim(util.exec("uname -m 2>/dev/null") or "")
    local um = res.uname_m

    res.os_name = "openwrt"

    -- x86/x64
    if um == "x86_64" then
        res.fpu = "none"; res.gnb_arch = "x86_64"; return res
    elseif um == "i686" or um == "i386" then
        res.fpu = "none"; res.gnb_arch = "i386"; return res

    -- ARM64 / aarch64
    elseif um == "aarch64" or um == "arm64" then
        res.fpu = "hard"; res.gnb_arch = "aarch64"; return res

    -- ARM 32-bit
    elseif um:match("^arm") then
        local fpu = detect_fpu_from_file() or detect_fpu_from_cpuinfo()
        res.fpu = fpu
        if um:match("^armv6") then
            res.gnb_arch = (fpu == "soft") and "armv6-softfp" or "armv6-hardfp"
        else
            -- armv7 及未知低版本默认 armv7
            res.gnb_arch = (fpu == "soft") and "armv7-softfp" or "armv7-hardfp"
        end
        return res

    -- MIPS little-endian
    elseif um == "mipsel" or um == "mipsle" then
        local fpu = detect_fpu_from_file() or detect_fpu_from_cpuinfo()
        res.fpu = fpu
        res.gnb_arch = (fpu == "soft") and "mipsel-softfp" or "mipsel"
        return res

    -- MIPS big-endian
    elseif um == "mips" then
        local fpu = detect_fpu_from_file() or detect_fpu_from_cpuinfo()
        res.fpu = fpu
        res.gnb_arch = (fpu == "soft") and "mips-softfp" or "mips"
        return res

    -- MIPS64
    elseif um == "mips64el" or um == "mips64le" then
        res.fpu = "none"; res.gnb_arch = "mips64el"; return res

    -- RISC-V 64
    elseif um == "riscv64" then
        res.fpu = "none"; res.gnb_arch = "riscv64"; return res
    end

    -- 未知架构 fallback
    res.fpu = "none"
    res.gnb_arch = um
    return res
end

-- arch_aliases: 返回 gnb_arch 的所有别名（与 GNB release 文件名对齐）
-- 参考 updater.go SelectAssetSmart archAliases
local function arch_aliases(gnb_arch)
    local a = { gnb_arch }
    if gnb_arch == "aarch64" then
        a[#a+1] = "arm64"
    elseif gnb_arch == "arm64" then
        a[#a+1] = "aarch64"
    elseif gnb_arch == "x86_64" then
        a[#a+1] = "amd64"
    elseif gnb_arch == "amd64" then
        a[#a+1] = "x86_64"
    elseif gnb_arch == "armv7-hardfp" then
        a[#a+1] = "arm"; a[#a+1] = "armhf"; a[#a+1] = "armv7"
    elseif gnb_arch == "armv7-softfp" then
        a[#a+1] = "arm"; a[#a+1] = "armsf"; a[#a+1] = "armv7sf"; a[#a+1] = "armv7_softfp"
    elseif gnb_arch == "armv6-hardfp" then
        a[#a+1] = "arm"; a[#a+1] = "armv6hf"; a[#a+1] = "armv6"
    elseif gnb_arch == "armv6-softfp" then
        a[#a+1] = "arm"; a[#a+1] = "armv6sf"; a[#a+1] = "armv6_softfp"
    elseif gnb_arch == "mipsel" then
        a[#a+1] = "mipsle"
    elseif gnb_arch == "mipsel-softfp" then
        a[#a+1] = "mipsel-softfloat"; a[#a+1] = "mipsel-muslsf"
        a[#a+1] = "mipsel_softfp";    a[#a+1] = "mipsle-softfloat"
    elseif gnb_arch == "mips-softfp" then
        a[#a+1] = "mips-softfloat"; a[#a+1] = "mips-muslsf"; a[#a+1] = "mips_softfp"
    elseif gnb_arch == "mips64el" then
        a[#a+1] = "mips64le"
    end
    return a
end

-- os_aliases: openwrt 允许 fallback 到 linux
local function os_aliases(os_name)
    return { "openwrt", "linux" }
end

-- ─────────────────────────────────────────────────────────────
-- 状态检测
-- ─────────────────────────────────────────────────────────────

-- is_install_running: 锁文件 + 进程存活检测（防重入）
function M.is_install_running()
    local pid_str = util.trim(util.read_file(M.LOCK_FILE) or "")
    if pid_str == "" then return false end
    local _, code = util.exec_status("kill -0 " .. pid_str .. " 2>/dev/null")
    if code == 0 then return true end
    -- 进程已死，清除残留
    os.remove(M.LOCK_FILE)
    return false
end

-- check_gnb_exists: gnb_ctl 是否存在
function M.check_gnb_exists()
    return util.file_exists(util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl")
end

-- get_status: 返回安装状态（供 api_gnb_install_status 使用）
function M.get_status()
    local plat    = M.detect_openwrt_target()
    local gnb_ok  = M.check_gnb_exists()
    local running = M.is_install_running()
    local log_out = util.trim(util.exec("tail -40 " .. M.LOG_FILE .. " 2>/dev/null") or "")

    local done, failed = false, false
    if log_out ~= "" then
        -- 扫描最后几行判断完成/失败
        for line in (log_out .. "\n"):gmatch("([^\n]*)\n") do
            local upper = line:upper()
            if upper:match("^DONE") then done  = true; failed = false end
            if upper:match("^ERROR:") then failed = true; done = true end
        end
    end

    return {
        gnb_exists = gnb_ok,
        running    = running,
        done       = done and not running,
        failed     = failed,
        log_tail   = log_out,
        gnb_arch   = plat.gnb_arch,
        os_name    = plat.os_name,
        uname_m    = plat.uname_m,
        gnb_bin    = gnb_ok and (util.GNB_DRIVER_ROOT .. "/bin/gnb_ctl") or nil,
    }
end

-- ─────────────────────────────────────────────────────────────
-- manifest / asset 解析
-- 参考 gnb_service.go UpgradeGNBToLatest + updater.go SelectArtifactNew
-- ─────────────────────────────────────────────────────────────

-- fetch_url: 获取 URL 内容，返回 (str, nil) 或 (nil, err)
-- 策略：
--   1) curl（CA 验证，retry 3）
--   2) curl -k（跳过证书，用于 mbedTLS/Let's Encrypt 兼容问题）
--   3) wget --no-check-certificate
-- 根因：OpenWrt 默认 curl 链接 mbedTLS，与 Let's Encrypt 证书存在兼容问题
-- 参考：mynet_tui updater.go + OpenWrt ca-bundle issue
local function fetch_url(url, timeout_s)
    timeout_s = timeout_s or 15
    local safe_url = url:gsub("'", "'\\''")

    -- 尝试 1: curl 标准（CA 验证）
    local out = util.trim(util.exec(string.format(
        "curl -fsSL --connect-timeout 5 -m %d --retry 3 --retry-delay 2 '%s' 2>/dev/null",
        timeout_s, safe_url
    )) or "")
    if out ~= "" then return out, nil end

    -- 尝试 2: curl -k（跳过 TLS 验证，解决 mbedTLS/certbot 兼容问题）
    out = util.trim(util.exec(string.format(
        "curl -fsSLk --connect-timeout 5 -m %d '%s' 2>/dev/null",
        timeout_s, safe_url
    )) or "")
    if out ~= "" then return out, nil end

    -- 尝试 3: wget --no-check-certificate
    out = util.trim(util.exec(string.format(
        "wget -q --no-check-certificate -T %d -O - '%s' 2>/dev/null",
        timeout_s, safe_url
    )) or "")
    if out ~= "" then return out, nil end

    return nil, "fetch failed (curl + curl-k + wget all failed): " .. url
end

-- pcall_safe: 包装 pcall，返回结果或 nil
local function pcall_safe(f, ...)
    local ok, res = pcall(f, ...)
    if ok then return res end
    return nil
end

-- fetch_apps_index: 下载并解析 apps.json
-- 返回 (table, nil) 或 (nil, errstr)
function M.fetch_apps_index(apps_url)
    apps_url = apps_url or M.APPS_INDEX
    local body, err = fetch_url(apps_url, 15)
    if not body then return nil, err end
    local data = util.json_decode(body)
    if not data or type(data.apps) ~= "table" then
        return nil, "apps.json missing 'apps' field"
    end
    return data, nil
end

-- fetch_gnb_manifest: 下载并解析 gnb manifest.json
function M.fetch_gnb_manifest(manifest_url)
    local body, err = fetch_url(manifest_url, 15)
    if not body then return nil, err end
    local data = util.json_decode(body)
    if not data then return nil, "failed to parse manifest" end
    return data, nil
end

-- select_asset: 从 version_data 中选择最匹配当前 plat 的资源包
-- 按优先级：artifacts → assets → targets+template
-- 返回 { url, checksum, method } 或 nil, errstr
function M.select_asset(version_data, plat)
    if not version_data or not plat then
        return nil, "missing args"
    end

    local arch_list = arch_aliases(plat.gnb_arch)
    local os_list   = os_aliases(plat.os_name)

    -- arch 匹配辅助（支持字符串或数组）
    local function arch_match(a_val)
        if type(a_val) == "table" then
            for _, av in ipairs(a_val) do
                for _, alias in ipairs(arch_list) do
                    if av == alias then return true end
                end
            end
        elseif type(a_val) == "string" then
            for _, alias in ipairs(arch_list) do
                if a_val == alias then return true end
            end
        end
        return false
    end

    -- os 匹配辅助
    local function os_match(o_val)
        for _, alias in ipairs(os_list) do
            if o_val == alias then return true end
        end
        return false
    end

    -- 1. artifacts（v2 新格式）
    if type(version_data.artifacts) == "table" and #version_data.artifacts > 0 then
        -- 精确 OS 优先
        for _, a in ipairs(version_data.artifacts) do
            if a.os == plat.os_name and arch_match(a.arch) then
                return {
                    url      = a.url or a.download_url or "",
                    checksum = a.sha256 or a.checksum or "",
                    method   = "artifact",
                }
            end
        end
        -- openwrt fallback → linux
        if plat.os_name == "openwrt" then
            for _, a in ipairs(version_data.artifacts) do
                if a.os == "linux" and arch_match(a.arch) then
                    return {
                        url      = a.url or a.download_url or "",
                        checksum = a.sha256 or a.checksum or "",
                        method   = "artifact-linux-fallback",
                    }
                end
            end
        end
    end

    -- 2. assets（v1 旧格式）
    if type(version_data.assets) == "table" and #version_data.assets > 0 then
        for _, a in ipairs(version_data.assets) do
            if os_match(a.os) and arch_match(a.arch) then
                return {
                    url      = a.url or a.download_url or "",
                    checksum = a.sha256 or a.checksum or "",
                    method   = "asset",
                }
            end
        end
    end

    -- 3. app_url_template + targets（简化版 SelectAssetSmart）
    local tpl = version_data.app_url_template or version_data.app_url_tpl or ""
    if tpl ~= "" and type(version_data.targets) == "table" then
        for _, tgt in ipairs(version_data.targets) do
            if os_match(tgt.os) and arch_match(tgt.arch) then
                return {
                    url_template = tpl,
                    target       = tgt,
                    method       = "template",
                }
            end
        end
    end

    return nil, string.format(
        "no asset found for os=%s arch=%s (tried: %s)",
        plat.os_name, plat.gnb_arch, table.concat(arch_list, ",")
    )
end

-- ─────────────────────────────────────────────────────────────
-- 安装 shell 脚本生成
-- ─────────────────────────────────────────────────────────────

-- _build_install_script: 生成 sh 脚本内容
-- 参考 gnb_service.go：下载、SHA256 校验、临时解压、规范化布局、复制到 bin/
M._build_install_script = function(dl_url, checksum, gnb_root, plat)
    local safe_url  = dl_url:gsub("'", "'\\''")
    local lock_file = M.LOCK_FILE
    local log_file  = M.LOG_FILE
    return string.format(
[[#!/bin/sh
# mynet GNB 自动安装脚本  OS=%s ARCH=%s
LOCK='%s'
LOG='%s'
GNB_ROOT='%s'
BINDIR="$GNB_ROOT/bin"
URL='%s'
CHECKSUM='%s'
TMPF="/tmp/gnb_dl_$$.tgz"
TMPD="/tmp/gnb_ext_$$"

log() { echo "[install_gnb] $*"; }
cleanup() { rm -f "$TMPF"; rm -rf "$TMPD"; }
trap cleanup EXIT

log "=============================="
log "开始安装 GNB  OS=%s ARCH=%s"
log "URL: $URL"
mkdir -p "$BINDIR"

# 0. 解决 OpenWrt mbedTLS + Let's Encrypt TLS 握手失败（-0x7780）
# curl 链接 mbedTLS 2.28.x 对 certbot/ISRG Root X1 有已知兼容问题
# 方案：安装 libcurl-gnutls4，将 libcurl.so.4 symlink 指向 gnutls 版（ABI 兼容）
# 注意：OpenWrt 24.x 上 libcurl-gnutls4 ABI 可能不兼容，先验证 curl 是否已可用
GNUTLS_LIB="/usr/lib/libcurl-gnutls.so.4"
CURL_LIB="/usr/lib/libcurl.so.4"
if curl --version >/dev/null 2>&1; then
    log "curl 可用，跳过 gnutls 替换"
else
    if ! opkg list-installed 2>/dev/null | grep -q '^libcurl-gnutls'; then
        log "安装 libcurl-gnutls4 (修复 mbedTLS TLS握手失败)..."
        opkg install libcurl-gnutls4 -q 2>/dev/null
    fi
    if [ -f "$GNUTLS_LIB" ]; then
        [ ! -f "${CURL_LIB}.mbedtls.bak" ] && cp -p "$CURL_LIB" "${CURL_LIB}.mbedtls.bak" 2>/dev/null
        ln -sf "$GNUTLS_LIB" "$CURL_LIB"
        # 验证替换后 curl 是否正常，不正常则回滚
        if ! curl --version >/dev/null 2>&1; then
            log "gnutls ABI 不兼容，回滚 libcurl..."
            ln -sf /usr/lib/libcurl.so.4.8.0 "$CURL_LIB" 2>/dev/null || \
                ln -sf "${CURL_LIB}.mbedtls.bak" "$CURL_LIB" 2>/dev/null || true
        else
            log "libcurl.so.4 → gnutls 版 (TLS修复已应用)"
        fi
    fi
fi

# 1. 下载（三级 fallback：curl → curl-k → wget-nochk）
log "正在下载..."
DL_OK=0
if command -v curl >/dev/null 2>&1; then
    curl -fsSL -m 180 --retry 2 -o "$TMPF" "$URL" 2>/dev/null && DL_OK=1
    if [ $DL_OK -eq 0 ]; then
        log "curl CA 验证失败，尝试 -k 模式..."
        curl -fsSLk -m 180 --retry 2 -o "$TMPF" "$URL" 2>/dev/null && DL_OK=1
    fi
fi
if [ $DL_OK -eq 0 ] && command -v wget >/dev/null 2>&1; then
    log "curl 失败，尝试 wget --no-check-certificate..."
    wget -q --no-check-certificate -T 180 -O "$TMPF" "$URL" 2>/dev/null && DL_OK=1
fi
if [ $DL_OK -eq 0 ]; then
    log "ERROR: 所有下载方式均失败（curl/curl-k/wget）"
    exit 1
fi
log "下载完成  大小=$(ls -lh "$TMPF" 2>/dev/null | awk '{print $5}')"

# 2. SHA256 校验（可选）
if [ -n "$CHECKSUM" ] && command -v sha256sum >/dev/null 2>&1; then
    log "校验 SHA256..."
    ACTUAL=$(sha256sum "$TMPF" | awk '{print $1}')
    if [ "$ACTUAL" != "$CHECKSUM" ]; then
        log "ERROR: SHA256 校验失败  期望=$CHECKSUM  实际=$ACTUAL"
        exit 1
    fi
    log "SHA256 OK"
fi

# 3. 解压
log "解压中..."
mkdir -p "$TMPD"
tar -xzf "$TMPF" -C "$TMPD" 2>/dev/null || tar -xf "$TMPF" -C "$TMPD" || { log "ERROR: 解压失败"; exit 1; }

# 4. 规范化布局并安装（参考 gnb_service.go ExtractArchive → NormalizeExtractedLayout）
BINSUBDIR=$(find "$TMPD" -maxdepth 3 -type d -name "bin" 2>/dev/null | head -1)
if [ -n "$BINSUBDIR" ]; then
    log "从 $BINSUBDIR 安装到 $BINDIR..."
    cp -rf "$BINSUBDIR/." "$BINDIR/"
else
    log "无 bin/ 子目录，直接复制可执行文件..."
    find "$TMPD" -maxdepth 4 -type f \( -name "gnb" -o -name "gnb_ctl" -o -name "gnb_crypto" -o -name "gnb_es" \) 2>/dev/null | while read f; do
        cp "$f" "$BINDIR/" && log "复制: $(basename "$f")"
    done
fi
chmod +x "$BINDIR/"* 2>/dev/null || true

# 5. 验证
if [ -x "$BINDIR/gnb_ctl" ]; then
    VER=$("$BINDIR/gnb_ctl" --version 2>/dev/null | head -1 || echo "unknown")
    log "安装成功: $BINDIR/gnb_ctl  版本: $VER"
elif [ -x "$BINDIR/gnb" ]; then
    VER=$("$BINDIR/gnb" --version 2>/dev/null | head -1 || echo "unknown")
    log "安装成功: $BINDIR/gnb  版本: $VER  (gnb_ctl 未找到)"
else
    log "ERROR: 安装失败，$BINDIR 中未找到可执行文件"
    ls -la "$BINDIR/" 2>&1 || true
    exit 1
fi

rm -f "$LOCK"
log "=============================="
echo "DONE"
]],
        plat.os_name, plat.gnb_arch,
        lock_file, log_file, gnb_root,
        safe_url, checksum or "",
        plat.os_name, plat.gnb_arch
    )
end

-- ─────────────────────────────────────────────────────────────
-- 主入口：start_auto_install
-- 参考 startup_helper.go autoInstallDrivers()
-- ─────────────────────────────────────────────────────────────

-- start_auto_install: 检测 → 获取 manifest → 选资源 → 后台安装
-- 返回 { ok, status, message, [arch, os_name, version, url] }
--   status: "already_ok" | "already_running" | "started" | "error"
function M.start_auto_install(opts)
    opts = opts or {}

    -- 1. 已安装，无需操作
    if M.check_gnb_exists() then
        return { ok = true, status = "already_ok", message = "gnb_ctl already installed" }
    end

    -- 2. 防重入：安装进程已在运行
    if M.is_install_running() then
        return { ok = true, status = "already_running", message = "install already in progress" }
    end

    -- 3. 检测 OpenWrt 运行目标
    local plat = M.detect_openwrt_target()
    if plat.gnb_arch == "" then
        return { ok = false, status = "error", message = "could not detect OpenWrt target arch (uname_m=" .. (plat.uname_m or "") .. ")" }
    end

    -- 3.5 依赖预检与预装
    --   检查 bash / kmod-tun / curl-gnutls / ca-bundle，按需静默安装
    M.check_deps()

    -- 4. 获取 apps index
    local apps_url  = opts.apps_url or M.APPS_INDEX
    local apps_data, err = M.fetch_apps_index(apps_url)
    if not apps_data then
        return { ok = false, status = "error", message = "fetch apps.json failed: " .. (err or "network error") }
    end

    local gnb_manifest_url = apps_data.apps and apps_data.apps["gnb"]
    if not gnb_manifest_url then
        return { ok = false, status = "error", message = "gnb not in apps index" }
    end

    -- 5. 获取 GNB manifest
    local manifest, merr = M.fetch_gnb_manifest(gnb_manifest_url)
    if not manifest then
        return { ok = false, status = "error", message = "fetch gnb manifest failed: " .. (merr or "network error") }
    end

    -- 解析最新稳定版本号
    local latest_ver = ""
    if manifest.latest and manifest.latest.stable and manifest.latest.stable ~= "" then
        latest_ver = manifest.latest.stable
    elseif manifest.latest_stable and manifest.latest_stable ~= "" then
        latest_ver = manifest.latest_stable
    elseif type(manifest.versions) == "table" then
        -- 扫描 versions 取字典序最大（通常等同于语义版本最大）
        for v in pairs(manifest.versions) do
            if latest_ver == "" or v > latest_ver then latest_ver = v end
        end
    end
    if latest_ver == "" then
        return { ok = false, status = "error", message = "could not determine latest gnb version" }
    end

    local version_data = manifest.versions and manifest.versions[latest_ver]
    if not version_data then
        return { ok = false, status = "error", message = "version data not found for " .. latest_ver }
    end

    -- 6. 选择资源包
    local asset, aerr = M.select_asset(version_data, plat)
    if not asset then
        return { ok = false, status = "error", message = "asset selection failed: " .. (aerr or "no match") }
    end

    -- 处理 template URL（substitution）
    local dl_url = asset.url or ""
    if (dl_url == "") and asset.url_template then
        local tgt = asset.target or {}
        dl_url = asset.url_template
            :gsub("{ver}",      latest_ver)
            :gsub("{version}",  latest_ver)
            :gsub("{arch}",     plat.gnb_arch)
            :gsub("{platform}", plat.os_name)
            :gsub("{ext}",      tgt.ext or "tgz")
            :gsub("{{ver}}",     latest_ver)
            :gsub("{{version}}", latest_ver)
            :gsub("{{arch}}",    plat.gnb_arch)
    end
    if dl_url == "" then
        return { ok = false, status = "error", message = "empty download URL after resolution" }
    end

    -- 7. 准备目录
    local gnb_root = util.GNB_DRIVER_ROOT
    os.execute("mkdir -p '" .. gnb_root .. "/bin'")

    -- 8. 写安装脚本
    local script_content = M._build_install_script(dl_url, asset.checksum or "", gnb_root, plat)
    local sf = io.open(M.SCRIPT_PATH, "w")
    if not sf then
        return { ok = false, status = "error", message = "could not write install script to " .. M.SCRIPT_PATH }
    end
    sf:write(script_content)
    sf:close()
    os.execute("chmod +x '" .. M.SCRIPT_PATH .. "'")

    -- 重置日志
    os.execute(": > " .. M.LOG_FILE)

    -- 9. 后台执行，获取 PID
    local cmd = "sh '" .. M.SCRIPT_PATH .. "' >" .. M.LOG_FILE .. " 2>&1 & echo $!"
    local pid_raw = util.exec(cmd) or ""
    local pid_str = pid_raw:match("^%s*(%d+)%s*$")
    if not pid_str then
        return { ok = false, status = "error", message = "failed to start install process (got: " .. pid_raw .. ")" }
    end

    -- 10. 写锁文件
    local lf = io.open(M.LOCK_FILE, "w")
    if lf then lf:write(pid_str); lf:close() end

    return {
        ok      = true,
        status  = "started",
        message = string.format("installing gnb %s for %s/%s (pid=%s)", latest_ver, plat.os_name, plat.gnb_arch, pid_str),
        arch    = plat.gnb_arch,
        os_name = plat.os_name,
        version = latest_ver,
        url     = dl_url,
    }
end

return M
