#!/bin/sh
# U1 过夜测试节点清单
HOSTS="192.168.0.32 192.168.0.16"
SSH_USER="${SSH_USER:-arthur}"
VM_SSH="${VM_SSH:-openwrt-qemu}"
# 用途：A=gnb主 B=gnb C=双驱动桥
export NODE_A=192.168.0.32
export NODE_B=192.168.0.16
export NODE_C=$VM_SSH
