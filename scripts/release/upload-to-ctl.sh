#!/bin/sh
# upload-to-ctl.sh — 把 make dist 的产物上传到 ctl（V2-8b / T4-1 的手工版）
#
# 为什么需要它：生产 https://ctl.mynet.club/d/apps.json 的 products 里
# **没有 mynet_agent**（只有 client/gnb/proxy/mynet/utc/lua），而那个叫 `mynet`
# 的是已废弃的 TUI。于是 `mynet update` 在生产无版本可拉 —— G4 的「可升级」不成立。
#
# 契约从代码读出（mynet_ctl ReleaseController.php:60-69）：
#   POST /api/v2/releases，中间件 auth.token:release:upload
#   multipart: product version platform arch [variant] [changelog] [is_stable] file(≤100MB)
#
# 安全约束：
#   · token **只从环境取**（CTL_UPLOAD_TOKEN），不接受命令行传参 —— 命令行会进
#     `ps` 与 shell history。推荐 `crt use <条目> -- CTL_UPLOAD_TOKEN=… sh 本脚本`
#   · 默认 **--dry-run**：只打印将要发的请求，不碰网络。真上传要显式 --upload
#   · 上传前**逐个核 sha256**（对 dist/checksums.txt）。核不过就不传 ——
#     传上去一个坏产物，客户端会在升级时拒装，而那比根本没上传更难查
#   · 默认拒绝 dev 版本（v0.0.0-<sha>）：ctl 上堆一堆 v0.0.0-* 会让语义比较失效
#
# 用法：
#   sh scripts/release/upload-to-ctl.sh                 # dry-run（默认）
#   sh scripts/release/upload-to-ctl.sh --upload        # 真上传（需 CTL_UPLOAD_TOKEN）
#   sh scripts/release/upload-to-ctl.sh --upload --allow-dev
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DIST="$ROOT/dist"
CTL_BASE_URL=${CTL_BASE_URL:-https://ctl.mynet.club}
PRODUCT=mynet_agent

MODE=dry
ALLOW_DEV=0
for a in "$@"; do
  case "$a" in
    --upload)    MODE=upload ;;
    --dry-run)   MODE=dry ;;
    --allow-dev) ALLOW_DEV=1 ;;
    -h|--help)   sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "未知参数: $a" >&2; exit 2 ;;
  esac
done

[ -d "$DIST" ] || { echo "✗ 没有 $DIST —— 先跑 make dist" >&2; exit 1; }
[ -f "$DIST/checksums.txt" ] || { echo "✗ 缺 dist/checksums.txt —— 先跑 make dist" >&2; exit 1; }
if [ "$MODE" = upload ] && [ -z "${CTL_UPLOAD_TOKEN:-}" ]; then
  echo "✗ --upload 需要 CTL_UPLOAD_TOKEN（只从环境取，不走命令行）" >&2
  echo "  推荐：crt use <ctl release token 条目> -- CTL_UPLOAD_TOKEN=\"\$<VAR>\" sh $0 --upload" >&2
  exit 1
fi

SHA_TOOL=""
if command -v sha256sum >/dev/null 2>&1; then SHA_TOOL=sha256sum
elif command -v shasum >/dev/null 2>&1; then SHA_TOOL="shasum -a 256"
else echo "✗ 找不到 sha256sum / shasum —— 无法核校验和，拒绝上传" >&2; exit 1; fi

echo "▸ 模式：$([ "$MODE" = upload ] && echo '真上传' || echo 'dry-run（不碰网络）')"
echo "▸ 目标：${CTL_BASE_URL}/api/v2/releases  product=${PRODUCT}"
echo

n=0; skipped=0; failed=0
# 同批要传：tgz 裸包 + 桌面安装器（exe/dmg/pkg）。zip 是 windows 裸包。
for f in "$DIST"/${PRODUCT}_*.tgz "$DIST"/${PRODUCT}_*.zip "$DIST"/${PRODUCT}_*.exe "$DIST"/${PRODUCT}_*.dmg "$DIST"/${PRODUCT}_*.pkg; do
  [ -f "$f" ] || continue
  base=$(basename "$f")
  ext="${base##*.}"

  # 文件名形状：mynet_agent_<platform>_<arch>_<version>.<ext>
  rest=${base#"${PRODUCT}_"}
  rest=${rest%."$ext"}
  platform=${rest%%_*}
  tmp=${rest#*_}
  arch=${tmp%%_*}
  version=${tmp#*_}

  if [ -z "$platform" ] || [ -z "$arch" ] || [ -z "$version" ] || [ "$version" = "$tmp" ]; then
    echo "✗ 文件名解析不出 platform/arch/version：$base"; failed=$((failed+1)); continue
  fi

  # dev 版本拦一道：v0.0.0-<sha> 是没打 tag 的构建
  case "$version" in
    v0.0.0-*)
      if [ "$ALLOW_DEV" -ne 1 ]; then
        echo "⚠ 跳过 ${base}：${version} 是 dev 版本（未打 tag）。"
        echo "    ctl 上堆一堆 v0.0.0-* 会让 mynet update 的语义比较失效。"
        echo "    确要传加 --allow-dev；正经做法是先打 tag（H11）。"
        skipped=$((skipped+1)); continue
      fi ;;
  esac

  # 校验和：与 dist/checksums.txt 比
  want=$(awk -v n="$base" '$2==n{print $1}' "$DIST/checksums.txt")
  if [ -z "$want" ]; then
    echo "✗ $base 不在 checksums.txt 里 —— 产物与清单不同批，拒绝上传"; failed=$((failed+1)); continue
  fi
  got=$($SHA_TOOL "$f" | awk '{print $1}')
  if [ "$want" != "$got" ]; then
    echo "✗ $base 校验和不符"; echo "    清单 $want"; echo "    实际 $got"; failed=$((failed+1)); continue
  fi

  size=$(wc -c < "$f" | tr -d ' ')
  if [ "$size" -gt 104857600 ]; then
    echo "✗ $base 是 ${size} 字节，超过服务端 100MB 上限"; failed=$((failed+1)); continue
  fi

  n=$((n+1))
  printf '── %s\n' "$base"
  printf '   product=%s version=%s platform=%s arch=%s  (%s 字节, sha256 %s…)\n' \
         "$PRODUCT" "$version" "$platform" "$arch" "$size" "$(printf '%s' "$got" | cut -c1-12)"

  if [ "$MODE" = dry ]; then
    printf '   curl -sS -X POST "%s/api/v2/releases" \\\n' "$CTL_BASE_URL"
    printf '     -H "Authorization: Bearer $CTL_UPLOAD_TOKEN" \\\n'
    printf '     -F product=%s -F version=%s -F platform=%s -F arch=%s \\\n' \
           "$PRODUCT" "$version" "$platform" "$arch"
    printf '     -F is_stable=1 -F "file=@dist/%s"\n' "$base"
    continue
  fi

  # 真上传。响应体落文件再判，避免把 token 或错误细节混进管道丢失
  resp=$(mktemp)
  code=$(curl -sS -o "$resp" -w '%{http_code}' -X POST "$CTL_BASE_URL/api/v2/releases" \
    -H "Authorization: Bearer $CTL_UPLOAD_TOKEN" \
    -F "product=$PRODUCT" -F "version=$version" \
    -F "platform=$platform" -F "arch=$arch" \
    -F "is_stable=1" -F "file=@$f")
  if [ "$code" = 201 ]; then
    printf '   ✓ 已上传（201）\n'
  else
    printf '   ✗ 上传失败 HTTP %s：%s\n' "$code" "$(head -c 200 "$resp")"
    failed=$((failed+1))
  fi
  rm -f "$resp"
done

echo
printf '合计：%s 个产物（%s 模式）· 跳过 %s · 失败 %s\n' "$n" "$MODE" "$skipped" "$failed"
if [ "$MODE" = dry ] && [ "$n" -gt 0 ]; then
  echo "  ℹ 这是 dry-run，什么都没传。确认后加 --upload 真跑。"
fi
if [ "$n" -eq 0 ] && [ "$skipped" -eq 0 ]; then
  echo "  ✗ dist/ 下没有 ${PRODUCT}_*.tgz —— 先跑 make dist"; exit 1
fi
[ "$failed" -eq 0 ] || exit 1
exit 0
