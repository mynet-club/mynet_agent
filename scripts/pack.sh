#!/bin/sh
# 把一个产物目录打成 ctl 认的压缩包（V2-4）。
#
#   sh scripts/pack.sh dist/mynet_agent_linux_amd64_v1.0.0
#   → dist/mynet_agent_linux_amd64_v1.0.0.tgz
#
# 扩展名由平台决定，对齐 ctl 的 PLATFORMS 表（ReleaseDomainService.php:34-41）：
#   windows → zip，其余 → tgz。
# ctl 的 parseFilename 只接受 tgz|zip|tar.gz|ipk，写错扩展名会导致下载 404。
#
# 归档内保留顶层目录（与产物同名），解包后目录自解释，不必猜里面是什么。
set -eu

if [ $# -ne 1 ]; then
  echo "用法: sh scripts/pack.sh <产物目录>" >&2
  exit 2
fi

dir=$1
if [ ! -d "$dir" ]; then
  echo "pack: 目录不存在: $dir" >&2
  exit 1
fi

base=$(basename "$dir")
parent=$(dirname "$dir")

case "$base" in
  *_windows_*) ext=zip ;;
  *)           ext=tgz ;;
esac

out="$parent/$base.$ext"
rm -f "$out"

case "$ext" in
  tgz) tar -czf "$out" -C "$parent" "$base" ;;
  zip) (cd "$parent" && zip -qr "$base.$ext" "$base") ;;
esac

# 空包必须炸出来：静默产出空包正是「看着齐全实际缺件」的纸面达成形态。
if [ ! -s "$out" ]; then
  echo "pack: 产物为空: $out" >&2
  exit 1
fi

# 归档里至少要有一个二进制，否则打的是空壳。
# Windows 产物名带 .exe，两种后缀都要认；zip 用 unzip -l（tar 读不了 zip）。
count=0
if [ "$ext" = "zip" ]; then
  count=$(unzip -l "$out" 2>/dev/null | grep -cE '(mynet|mynetd|mynet_gateway)(\.exe)?$' || true)
else
  count=$(tar -tzf "$out" 2>/dev/null | grep -cE '/(mynet|mynetd|mynet_gateway)(\.exe)?$' || true)
fi
if [ "$count" -lt 1 ]; then
  echo "pack: $out 里没有 mynet/mynetd/mynet_gateway —— 打包前构建失败？" >&2
  exit 1
fi

echo "    $(basename "$out") ($(wc -c < "$out" | tr -d ' ') bytes)"