#!/bin/sh
# G3：真隧道三场景（U1 三机拓扑）
# A=192.168.0.32(10.31.0.1/10.23.0.1) B=192.168.0.16(10.31.0.2/10.23.0.2) C=VM(10.31.0.3/10.23.0.3)
A=192.168.0.32; B=192.168.0.16; C=openwrt-qemu
fail=0
ck() { r=$(eval "$2" 2>/dev/null); [ "$r" = "OK" ] && echo "  ✓ $1" || { echo "  ✗ $1"; fail=1; }; }

echo "== 场景1 gnb =="
ck "A→B gnb" "ssh arthur@$A 'ping -c 2 -w 2 10.31.0.2 >/dev/null && echo OK'"
ck "B→A gnb" "ssh arthur@$B 'ping -c 2 -w 2 10.31.0.1 >/dev/null && echo OK'"
ck "A→C gnb" "ssh arthur@$A 'ping -c 2 -w 2 10.31.0.3 >/dev/null && echo OK'"

echo "== 场景2 wireguard =="
ck "A→B wg" "ssh arthur@$A 'ping -c 2 -w 2 10.23.0.2 >/dev/null && echo OK'"
ck "B→C wg" "ssh arthur@$B 'ping -c 2 -w 2 10.23.0.3 >/dev/null && echo OK'"
ck "C→A wg" "ssh $C 'ping -c 2 -w 2 10.23.0.1 >/dev/null && echo OK'"

echo "== 场景3 跨驱动（C 显式桥）=="
ck "A(gnb)→C桥→B(wg) HTTP" "ssh arthur@$A 'curl -sS -m 3 http://10.31.0.3:9080/ | grep -q hello && echo OK'"

if [ $fail -eq 0 ]; then echo "G3 TUNNEL PASS"; else echo "G3 FAIL"; exit 1; fi
