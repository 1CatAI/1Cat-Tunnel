1CatTunnel Windows SSH 0.5.2-security
================================

用途
----
一键安装 Windows OpenSSH Server 和 1CatTunnel 自动服务。
安装完成后，即使 Windows 重启且无人登录，SSH 隧道仍会自动上线。

安装
----
1. 双击“Install 1CatTunnel SSH.bat”，接受 Windows 管理员授权；程序优先使用 Windows 系统 OpenSSH，可选功能不可用时自动回退到包内已校验的 Microsoft 签名组件。
2. 输入服务器接入密码；输入过程使用星号隐藏。
3. 确认节点名称和 SSH 映射。
4. 等待程序显示公网映射地址。

也可以在管理员终端运行：
  .\1cattunnel.exe install

在管理员终端中强制使用包内官方组件：
  .\1cattunnel.exe install --offline-openssh

连接与监视
----------
安装完成后，双击安装脚本或直接运行 1cattunnel.exe 会保持监视窗口常驻，
实时显示 TCP 连接建立、断开、活跃连接数和每条已结束连接的双向流量。
关闭监视窗口不会停止后台 1CatTunnel 或 OpenSSH 服务。

安装完成后在管理员终端运行：
  .\1cattunnel.exe status
  .\1cattunnel.exe monitor

程序会显示类似下面的命令：
  ssh -p 52743 Windows用户名@dx.1catai.com

公网端口由 1CatTunnel 服务端分配。本机 SSH 只连接
127.0.0.1:22，默认不会直接向局域网开放 22 端口。

同一节点、同一映射名称和协议重复启动时，服务端会优先恢复原来的公网端口；
只有旧端口已经不可用时才会分配新端口。

客户确认的免密码远程协助
--------------------------
本包中的 support-operator.pub 仅为远程协助方的公钥，不包含私钥或客户密码。
完成安装后，客户可以双击“Start Remote Support.bat”，在 Windows 确认窗口中
核对本机账户与显示的密钥指纹，然后自行选择“是”。

确认后，远程协助方使用其单独保管的私钥连接：
  ssh -i 运营方私钥路径 -p 公网端口 Windows用户名@dx.1catai.com

- 客户无需提供、更改或告诉任何人自己的 Windows 登录密码。
- 授权没有倒计时：在等待协助期间会一直有效。
- 远程 SSH 会话断开后，临时公钥会自动删除，无法再次连接。
- 客户随时可以双击“End Remote Support.bat”，立即中断远程会话并删除临时公钥。
- 该协助密钥只能建立一个交互式 SSH 会话，已禁止端口转发、代理转发和 X11 转发。
- 原有的 Windows 账户密码和客户自己的 SSH 公钥登录方式保持不变。

登录凭据
--------
普通 SSH 登录仍使用现有 Windows 账户密码或客户自己的公钥，
不使用 1CatTunnel 服务器接入密码。Windows Hello PIN 不能代替账户密码进行 SSH 登录。

维护
----
查看状态：  .\1cattunnel.exe status
打开监视：  .\1cattunnel.exe monitor
发起协助：  .\1cattunnel.exe support
结束协助：  .\1cattunnel.exe support-stop
自动修复：  .\1cattunnel.exe repair
卸载服务：  .\1cattunnel.exe uninstall

卸载默认保留 OpenSSH、Windows 账户、SSH 主机密钥和节点配置，
避免误删客户已有的远程访问能力。

安全设计
--------
- 当前 1cattunnel.exe 尚未使用商业 Authenticode 证书签名，Windows 可能显示“未知发布者”；分发前请核对 SHA256SUMS.txt。包内 OpenSSH 组件均会验证 Microsoft 数字签名。
- 1CatTunnel 节点凭据使用 Windows DPAPI 加密保存。
- 客户确认的远程协助只部署公钥，协助方私钥不在源码、安装包或客户设备上。
- 客户端服务使用独立的 NT SERVICE\1CatTunnelClient 虚拟账户，不以 LocalSystem 身份运行。
- 配置目录仅允许客户端服务、SYSTEM 和管理员访问；备份仅允许 SYSTEM 和管理员访问。
- 客户端日志达到 8 MiB 后自动轮换，最多保留 3 份历史日志。
- 新安装的 OpenSSH 默认只监听 127.0.0.1:22。
- SSH 未认证连接前 16 个全部接收，16-32 个逐步限流，32 个为硬上限；登录等待最多 30 秒，单连接最多尝试认证 4 次。
- 包内 OpenSSH 仅作系统功能不可用时的备用，其固定 SHA-256、Microsoft 数字签名和版本均会在安装前校验。
- 包内备用 OpenSSH 来自 Microsoft 官方 Win32-OpenSSH 预览发行版；正式环境优先使用 Windows 系统提供的稳定功能。
- SSH 主机私钥会清除多余账户权限，仅保留 SYSTEM 和管理员完全控制。
- 修改 sshd_config 前会备份并执行语法检查，失败自动恢复。
- payload 目录仅作为 Windows 功能安装失败时的离线备用。
- 0.4.4 客户端会对旧服务端临时丢弃的数据通道进行限速重试；仍建议配套 0.4.1 或更高版本服务端，从根源分离控制和数据握手额度。
