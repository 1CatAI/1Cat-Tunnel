# 1cattunnel 0.5.2-security

Windows / Linux x64 客户端，内置原生核心和 TLS CA，无需 Go、Python 或额外运行库。npm 安装仅需要 Node.js / npm；独立压缩包连 Node.js 也不需要。

## 安装与启动

本版不包含任何 npm 安装钩子。安装不会自动启动程序、连接服务器或注册后台服务。JavaScript 脚本为可读源码，Linux / Windows 核心使用校验过的 Go 1.26.8 编译，不使用 Garble、加壳或脚本混淆。

```bash
npm install -g ./1cattunnel-0.5.2-security.tgz --ignore-scripts
1cattunnel
```

注册表发布与审核状态以 npm 官方记录为准；本地压缩包可独立安装。Linux 系统级 npm 目录可能需要 sudo，建议使用用户级 npm 目录。安装遇到“找不到命令”，检查 `npm prefix -g` 下的 `bin` 是否在 PATH 中。不要用关闭安全软件或放宽系统目录权限的方式解决。

## 本机管理

程序默认监听 `127.0.0.1:51888`，不会向局域网或公网开放管理页。

1. 前台启动会在终端显示专用管理登录链接。也可在另一个终端、使用同一系统账户执行 `1cattunnel panel`。
2. 打开完整链接后选择托管服务器，默认 `dx.1catai.com:50001`，TLS 校验证书。
3. 手动填写管理员提供的接入凭据，选择 1 至 20 条映射，设置 TCP / UDP 与本机服务端口。公网端口由服务端分配。
4. 保存后自动重连。旧节点的独立凭据、相同映射名称及协议继续用于恢复原端口。

管理登录链接相当于本机管理密码，不是服务器接入密码，切勿分享。链接密钥保存在配置旁的 `.web-auth.json` 文件中；Linux 权限 0600，Windows 使用当前用户 DPAPI 和私有 ACL。浏览器仅保存在当前标签页的 sessionStorage，不写入 URL 查询参数或服务日志。重启客户端后密钥失效，重新获取链接。“退出本页面登录”只退出当前页面，重启客户端可撤销所有页面。

已保存的接入密码不回显，注册成功后替换为节点独立凭据。Linux 配置为 0600；Windows 配置使用私有 ACL 和 DPAPI。自定义配置路径不允许符号链接或不安全权限。

```bash
1cattunnel --version
1cattunnel status
1cattunnel panel
1cattunnel -setup
1cattunnel -config /absolute/path/client-linux.json
```

远程 Linux 面板可通过 SSH 本地转发访问，然后使用该 Linux 服务所属账户生成的完整登录链接：

```bash
ssh -L 51888:127.0.0.1:51888 user@CLIENT_HOST
```

## Linux 后台服务

安装包和独立后台服务是两个明确分开的操作。系统级服务使用 sudo；不要同时安装同名用户级服务。

```bash
sudo 1cattunnel service install --yes
sudo 1cattunnel service upgrade --yes
sudo 1cattunnel service stop
sudo 1cattunnel service uninstall --yes
```

不加 sudo 则管理当前账户的用户级 systemd 服务，只随登录启动。需要无人登录启动时，由管理员明确配置 `loginctl enable-linger`。系统级安装保留已有服务所属用户；新装时使用 sudo 原用户。

升级保留原配置位置、独立节点身份和历史程序；先校验服务文件，再重启并检查带认证的管理接口，失败恢复旧服务。系统级备份在 `/usr/local/lib/1cat-tunnel/backups/`，用户级备份在 `~/.local/share/1cat-tunnel/backups/`。

**先卸载服务，再卸载 npm 包**：npm 7 及之后不执行卸载钩子，不能依赖 `npm uninstall` 停止独立服务。

```bash
sudo 1cattunnel service uninstall --yes
sudo npm uninstall -g 1cattunnel
```

卸载服务会停止连接并取消自启，保留配置、历史二进制和备份，供用户自行归档。更新 npm 包后，已经运行的旧服务不会隐式升级，需明确执行 `service upgrade --yes`。

Windows npm 通用版为前台程序，关闭窗口即停止；不安装 OpenSSH，也不修改防火墙或 Windows 服务。

## 屏蔽 IP

`POST /api/block-ip` 保留原管理员认证，用户名和密码与服务端 WebUI 相同。本机网页的登录凭据不能替代服务器管理员密码。

```bash
curl -u admin -H 'Content-Type: application/json' \
  -d '{"ip":"203.0.113.10","reason":"scan"}' http://127.0.0.1:51888/api/block-ip
```

本版沿用原控制协议，不要求生产服务端同步升级。安全修复内容及验证证据见项目的 `docs/RELEASE_0.5.2-security.md`。
