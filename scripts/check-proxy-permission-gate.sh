#!/bin/sh
# L2 独立验收；这些行为测试也已进入默认 make check。
set -eu
TASK_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "${TASK_ROOT}" && go test ./mynet -run '^TestProxyExit' -count=1
