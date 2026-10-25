#!/bin/sh
# 真 Linux 内核规则验收，仅在 Docker 独立网络容器运行；不使用 host network。
set -eu
TASK_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TASK_PROBE="${TASK_ROOT}/mynet/proxy_exit_kernel_generated_test.go"
[ ! -e "${TASK_PROBE}" ] || { echo "拒绝覆盖 ${TASK_PROBE}" >&2; exit 1; }
TASK_TEMP=$(mktemp -d)
TASK_CONTAINER="mynet-exit-$(basename "${TASK_TEMP}")"
trap 'docker rm -f "${TASK_CONTAINER}" >/dev/null 2>&1 || true; rm -f "${TASK_PROBE}"; rm -rf "${TASK_TEMP}"' EXIT HUP INT TERM
cp "${TASK_ROOT}/testdata/proxy-permission-loop/kernel_test.go.txt" "${TASK_PROBE}"
TASK_ARCH=$(docker info --format '{{.Architecture}}')
case "${TASK_ARCH}" in aarch64|arm64) TASK_GOARCH=arm64;; x86_64|amd64) TASK_GOARCH=amd64;; *) echo "未知架构 ${TASK_ARCH}" >&2;exit 1;; esac
cd "${TASK_ROOT}" && GOOS=linux GOARCH="${TASK_GOARCH}" CGO_ENABLED=0 go test -tags proxy_exit_kernel_test -c ./mynet -o "${TASK_TEMP}/exit.test"
docker run --rm --name "${TASK_CONTAINER}" --platform "${MYNET_KERNEL_PLATFORM:-linux/${TASK_GOARCH}}" --privileged -v "${TASK_TEMP}:/probe:ro" alpine:3.20 sh -ec 'apk add --no-cache nftables iptables iproute2 dbus >/dev/null; uname -r; nft --version; iptables --version; /probe/exit.test -test.run "^Test(ProxyExit(Kernel|WANFlowKernel)|FirewalldReloadEventKernel)$" -test.v' # GUARD-OK 独立容器内核验收，无 host network；仅操作临时容器网络空间
