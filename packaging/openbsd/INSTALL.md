# OpenBSD 安装与运行

包内包含 mynet、mynetd 和嵌入 WebUI，无需单独部署 HTML。浏览器访问本机 http://127.0.0.1:8096。
支持 amd64、arm64。需 root 权限执行路由和 DNS 操作；隧道驱动需另行安装匹配系统版本。

```sh
sudo mkdir -p /opt/mynet/bin
sudo cp mynet mynetd /opt/mynet/bin/
sudo chmod 755 /opt/mynet/bin/mynet /opt/mynet/bin/mynetd
sudo /opt/mynet/bin/mynet config form pc
sudo /opt/mynet/bin/mynet setup --online --tenant TENANT --node-id NODE_ID --runtime-mode pc
# 按现有接入流程导入/生成密钥并连接，gateway 地址使用所属网络的实际值。
sudo env MYNET_HOME=/opt/mynet MYNET_BIN=/opt/mynet/bin/mynet MYNET_GATEWAYS=wss://GATEWAY/v1/node/connect /opt/mynet/bin/mynetd
```

当前包提供前台 mynetd；`mynet install` 尚无 BSD 原生 rc 服务安装后端。需要开机启动时由管理员按本机 rc.d 方式管理上述进程。
PC 态使用 DNS hook 和 route 主表目的路由，退出时恢复原 resolv.conf 内容或符号链接。
`mynet config form` 查看运行形态；Linux/BSD 未显式选择时保留旧 GW 行为。
GW 态与 PC 态独立；本批没有新增 BSD PF 网关代理后端，GW 流量代理不能按 Linux nft 能力验收。
