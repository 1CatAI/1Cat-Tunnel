# Linux 客户端 0.5.3

本次只更新 Linux x86_64 客户端。Windows 保留 0.5.2-security，现有隧道服务端无需升级。

## 安装与使用

P400 安装命令保持不变：

```bash
curl http://dx.1catai.com:32083/install.sh | sh
1cattunnel -setup
```

首次安装只安装命令和程序，不启动客户端、不创建或启用 systemd 单元。下载密码和节点接入凭据继续分别输入。GitHub Release 的公开安装包不需要下载密码。

`-setup` 在终端询问接入凭据、节点名称、本机映射端口。确认保存后登记开机自启，然后直接在当前窗口运行。系统级安装可能要求客户本机 sudo 密码。连接成功时显示服务器域名、实际连接 IP:端口、服务端分配的公网地址；TCP 连接建立、关闭和流量持续显示。

```bash
1cattunnel                         # 停止同一配置的旧客户端并在当前终端运行
1cattunnel -config /路径/client.json # 使用指定配置，同样支持接管
1cattunnel status                  # 只查询状态
1cattunnel panel                   # 显示本机私有管理链接
1cattunnel monitor                 # 只观察状态和日志
1cattunnel desktop                 # 在当前 Linux 桌面打开观察窗口
```

`Ctrl+C` 停止当前前台客户端；不会取消已登记的开机自启。需要转回后台时执行 `sudo systemctl start 1cat-tunnel-client.service`。要取消自启，执行 `sudo systemctl disable --now 1cat-tunnel-client.service`。

重新执行运行命令会先停止对应 systemd 服务，再结束同一账户、同一配置的旧客户端。旧进程 5 秒未退出时强制结束；独立配置和只读观察进程继续运行。接管会中断已有业务连接，公网映射端口与节点独立凭据保留。

## 桌面窗口

完成 setup 后，XDG 桌面自启项在用户登录 Linux 桌面时启动观察器，客户端启动或重启时打开状态/日志终端。窗口可以关闭，不会停止客户端；客户端下次重启时观察器会再次打开窗口。

支持系统现有 GNOME Terminal、Konsole、XFCE Terminal、MATE Terminal、x-terminal-emulator 或 xterm，不额外安装图形依赖。纯 SSH、无桌面的服务器和桌面登录前无法弹出图形窗口，此时使用当前终端或 `1cattunnel monitor`。`--user` 安装后 setup 登记的是用户级服务，通常在该用户登录后运行；系统级安装才提供整机启动服务。

## 升级与回滚

重复执行安装命令保留现有配置和已登记自启状态；原后台服务正在运行时升级并重启，启动失败恢复旧二进制和单元。系统备份位于 `/usr/local/lib/1cat-tunnel/backups/`，安装器输出本次具体路径。

默认配置仍为 `~/.config/1cat-tunnel/client-linux.json`。配置、管理凭据、实例锁和配置旁的 `.log` 文件均为 0600；日志限制为 8 MiB。客户端核心为静态 Go 原生程序，不要求客户安装 Go、Node.js、npm 或 Python。

本次通过独立 `client/` 源码目录发布到 GitHub，并同步 P400 的 32083 分发源；生产主服务端的版本和配置不属于此发布范围。
