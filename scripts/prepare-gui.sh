#!/bin/sh
# SOURCE 是 .env.embed 构建的 SPA；DEST 是构建专用目录。
# 校验和复制均成功后才替换旧树，避免失败或增量构建残留陈旧资源。
set -eu
src=${1:?usage: prepare-gui.sh SOURCE DEST}
dest=${2:?usage: prepare-gui.sh SOURCE DEST}
test -f "$src/index.html" || { echo 'GUI 缺 index.html' >&2; exit 1; }
grep -q '/gui/assets/' "$src/index.html" || { echo 'GUI 必须使用 .env.embed 构建' >&2; exit 1; }
# Quasar 生成的 src/href 均为双引号；检查入口真正引用的每个本地资源。
refs=$(sed 's/"/\
/g' "$src/index.html" | sed -n '\|^/gui/assets/|p')
test -n "$refs" || { echo 'GUI 无入口资源' >&2; exit 1; }
printf '%s\n' "$refs" | while IFS= read -r ref; do
  asset=${ref#/gui/}
  case "$asset" in *..*|*\?*|*\#*) echo "GUI 非法资源路径: $asset" >&2; exit 1;; esac
  test -f "$src/$asset" || { echo "GUI 缺资源: $asset" >&2; exit 1; }
done
parent=$(dirname "$dest")
mkdir -p "$parent"
stage=$(mktemp -d "$parent/.gui-stage.XXXXXX")
backup="$stage.previous"
cleanup() {
  rm -rf "$stage"
  if [ -e "$backup" ]; then
    if [ ! -e "$dest" ]; then mv "$backup" "$dest"; else rm -rf "$backup"; fi
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
cp -R "$src/." "$stage/"
if [ -e "$dest" ]; then mv "$dest" "$backup"; fi
mv "$stage" "$dest"
echo 'GUI embed assets ready'
