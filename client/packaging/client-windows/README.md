# 1Cat Tunnel 0.5.2-security Windows 客户端

本目录包含 Windows x64 可执行程序、启动脚本、默认配置和 TLS CA，无需安装 Go、Node.js 或 npm。

1. 解压整个目录，双击 `start-client.bat`。旧版更新前先关闭旧客户端窗口，保留原 `client-windows.json`。
2. 打开终端显示的专用登录链接，或运行 `tunnel-client.exe -panel -config client-windows.json` 获取。链接请勿分享，重启后失效。未登录首页不会显示配置。
3. 选择托管服务器，输入接入密码或独立 Token。默认主节点为 `dx.1catai.com:50001`，包内不含接入密码。
4. 选择映射数量，逐条填写 TCP/UDP、本机地址和本机端口，点击“保存并应用”。公网端口由服务端分配，可在页面查看和复制。

可在“管理托管服务器”添加后续 TLS 节点。已保存的密码不会回显，留空保持原凭据；注册成功后保存节点专属凭据。TLS 1.3、证书校验和 Windows DPAPI 凭据保护默认开启。

默认映射 RDP `127.0.0.1:3389`，可在页面改为网站、SSH 等本机服务。目标服务必须已经启动。普通客户端不安装 SSH；需要 SSH 自动安装、开机服务与客户确认远程协助时使用独立的 Windows SSH 交付包。

保持启动窗口运行；关闭窗口会停止普通客户端。运行 `tunnel-client.exe -setup` 可使用旧终端向导。
