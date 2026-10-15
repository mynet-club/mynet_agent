#!/bin/sh
# 把一个产物目录打成 ctl 认的压缩包（V2-4）。
#
#   sh scripts/pack.sh dist/mynet_agent_linux_amd64_v1.0.0
#   → dist/mynet_agent_linux_amd64_v1.0.0.tgz
#
# 扩展名由平台决定，对齐 ctl 的 PLATFORMS 表（ReleaseDomainService.php:34-41）：
#   darwin/windows → zip，其余 → tgz。
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
  *_windows_*|*_darwin_*) ext=zip ;;
  *)           ext=tgz ;;
esac

# 完成所有预检再创建归档，拒绝单件二进制/无来源记录的半包。
suffix=""
case "$base" in *_windows_*) suffix=.exe ;; esac
for required in "mynet$suffix" "mynetd$suffix" INSTALL.md SOURCES.txt; do
  if [ ! -s "$dir/$required" ]; then
    echo "pack: 缺少或为空: $required" >&2
    exit 1
  fi
done

out="$parent/$base.$ext"
# 先在同一文件系统构建临时归档，验证后再替换已有包。
staging=$(mktemp -d "$parent/.pack.XXXXXX")
trap 'rm -rf "$staging"' EXIT
archive="$staging/$base.$ext"
case "$ext" in
  tgz) tar -czf "$archive" -C "$parent" "$base" ;;
  zip) (cd "$parent" && zip -qr "$(basename "$staging")/$base.$ext" "$base") ;;
esac

# 空包必须炸出来：静默产出空包正是「看着齐全实际缺件」的纸面达成形态。
if [ ! -s "$archive" ]; then
  echo "pack: 产物为空: $out" >&2
  exit 1
fi

# 归档里至少要有一个二进制，否则打的是空壳。
# Windows 产物名带 .exe，两种后缀都要认；zip 用 unzip -l（tar 读不了 zip）。
# 客户端包**不含** mynet_gateway（服务端组件，不进用户安装包）。
count=0
if [ "$ext" = "zip" ]; then
  count=$(unzip -l "$archive" 2>/dev/null | grep -cE '(mynet|mynetd)(\.exe)?$' || true)
else
  count=$(tar -tzf "$archive" 2>/dev/null | grep -cE '/(mynet|mynetd)(\.exe)?$' || true)
fi
if [ "$count" -lt 1 ]; then
  echo "pack: $out 里没有 mynet/mynetd —— 打包前构建失败？" >&2
  exit 1
fi

# 每个平台包必须带自己的安装文档（用户旅程：解压即见怎么装）
if [ "$ext" = "zip" ]; then
  has_install=$(unzip -l "$archive" 2>/dev/null | grep -c 'INSTALL.md$' || true)
else
  has_install=$(tar -tzf "$archive" 2>/dev/null | grep -c 'INSTALL.md$' || true)
fi
if [ "$has_install" -lt 1 ]; then
  echo "pack: $out 缺 INSTALL.md —— 每个平台产物必须带分平台安装文档" >&2
  exit 1
fi

mv "$archive" "$out"

echo "    $(basename "$out") ($(wc -c < "$out" | tr -d ' ') bytes)"