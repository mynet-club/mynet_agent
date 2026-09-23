#!/usr/bin/env python3
"""U1-A6 跨驱动桥：双驱动节点显式 TCP 转发。

产品语义（2026-09-23 决策）：gnb-only ↔ wg-only **不直连**；
跨驱动互通 = 双驱动节点**显式转发**（本脚本），不是透明路由。

用法（在双驱动节点 C 上）：
  python3 cross_driver_bridge.py --listen 9080 --dest 10.23.0.2:8080

注意 OpenWrt Python 可能缺 idna：必须用 (ip, port) 元组 connect。
"""
import argparse, socket, threading

def pipe(src, dst):
    try:
        while True:
            d = src.recv(4096)
            if not d:
                break
            dst.sendall(d)
    except Exception:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except Exception:
            pass

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--listen', type=int, default=9080)
    ap.add_argument('--dest', default='10.23.0.2:8080')
    ap.add_argument('--bind', default='0.0.0.0')
    args = ap.parse_args()
    dip, dport = args.dest.rsplit(':', 1)
    dest = (dip, int(dport))

    def handle(cs, addr):
        try:
            ds = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            ds.settimeout(5)
            ds.connect(dest)  # 元组连接，避开 idna
        except Exception as e:
            print('dest fail', e, flush=True)
            cs.close()
            return
        threading.Thread(target=pipe, args=(cs, ds), daemon=True).start()
        pipe(ds, cs)
        try:
            cs.close(); ds.close()
        except Exception:
            pass

    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((args.bind, args.listen))
    srv.listen(16)
    print(f'bridge {args.bind}:{args.listen} -> {dest}', flush=True)
    while True:
        cs, addr = srv.accept()
        threading.Thread(target=handle, args=(cs, addr), daemon=True).start()

if __name__ == '__main__':
    main()
