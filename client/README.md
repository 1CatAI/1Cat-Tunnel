# 1cattunnel Client

Linux 客户端 **0.5.3**，Windows 保持 **0.5.2-security**。本目录可独立编译，包含完整自有客户端源码、测试与打包脚本。本次仅发布 Linux 新包。

## Linux 一键安装

支持 Linux x86_64/amd64，默认系统安装需要本机 sudo 权限。首次安装只放置程序与命令，完成终端 setup 后才登记开机自启：

```bash
curl -fsSL https://github.com/1CatAI/1Cat-Tunnel/releases/latest/download/install.sh | sh -s -- --yes
1cattunnel -setup
```

脚本经 GitHub HTTPS 获取，安装包还必须匹配脚本固定的 SHA-256。先以当前用户下载与校验，只在执行已验证的本地服务安装器时提权。不需要 npm、Node.js、Go 或 Python，也不自动安装其他系统依赖。系统需已有 curl、CA 证书、tar、sha256sum 和常用 Linux 工具；缺失时会明确停止。不要使用 `sudo curl ... | sh`。

固定版本入口：将命令中的 `latest/download` 换成 `download/client-v0.5.3`。也可以先下载 `install.sh`，检查源码后运行 `sh install.sh --yes`。

无需下载密码。公开 GitHub Release 不受原服务器下载密码限制；**隧道接入凭据仍须自行输入**，下载不授予服务器访问权。生产服务器配置、下载站和 npm 状态不会因本次发布改变。

`-setup` 保存后直接在当前终端运行，显示服务器实际 IP:端口、已分配的公网入口和连接日志。重新执行 `1cattunnel` 会停止对应后台服务，结束同一账户、同一配置的旧客户端，随后在新终端运行。旧进程 5 秒未退出时强制结束，独立节点凭据和公网端口保留。`Ctrl+C` 停止前台客户端，开机自启仍保留。

只观察状态而不重启客户端：`1cattunnel monitor`。完成 setup 后桌面登录会启动观察器，在客户端启动或重启时打开系统已有终端显示状态/日志；关闭观察窗口不会停止隧道。无桌面或纯 SSH 环境使用当前终端。详见 [0.5.3 行为说明](RELEASE-0.5.3.md)。

需要使用可选的 Web 面板时，以同一用户执行以下命令获取私有登录链接：

```bash
/usr/local/lib/1cat-tunnel/tunnel-client -panel -config "$HOME/.config/1cat-tunnel/client-linux.json"
```

管理页面仅监听 `127.0.0.1:51888`。远程 Linux 请通过 SSH 本地端口转发访问，不要将面板绑定到公网。默认控制端口为 `dx.1catai.com:50001`，不是 WebUI 或下载端口。填写节点凭据，选择托管服务器、映射数量、TCP/UDP 和本机服务端口，公网端口由服务端分配。

重复执行安装命令会保留既有配置路径，并在替换前备份程序、配置与服务文件。服务启动或认证健康检查失败会回滚程序和服务。自定义配置使用 `--config /绝对路径/client.json`。升级会短暂重连，勿同时运行多个使用同一配置的实例。

普通用户安装：把末尾改成 `--yes --user`，然后运行 `~/.local/bin/1cattunnel -setup`。安装阶段不添加服务；完成 setup 后登记用户级服务，通常在该用户登录后自动运行。系统启动即运行请用系统级安装。若命令由其他安装器管理会拒绝覆盖。

停止开机自启：`sudo systemctl disable --now 1cat-tunnel-client.service`。完整移除服务可运行压缩包内的 `uninstall-client-systemd.sh --yes`，配置和回滚备份会保留。

## Windows 下载

在 [Releases](https://github.com/1CatAI/1Cat-Tunnel/releases) 下载：

- `1cattunnel-windows-amd64-0.5.2-security.zip`：通用客户端，解压后双击启动脚本，使用本机 Web 面板配置；关闭运行窗口就停止客户端。
- `1CatTunnel-Windows-SSH-0.5.2-security.zip`：带 Windows SSH 管理能力的独立交付包。只有明确运行安装并接受 UAC 才会注册服务；远程协助仍需要客户确认。

Windows 包不需要 Node.js 或 Go。自有程序未使用商业 Authenticode 证书签名，可能显示未知发布者；请校验 SHA256SUMS.txt。包内 OpenSSH 为单独分发的 Microsoft 组件，按其许可证提供，运行时验证固定哈希与 Microsoft 签名。

## 源码与构建

| 路径 | 内容 |
| --- | --- |
| `cmd/tunnel-client` | Windows / Linux 通用客户端入口 |
| `cmd/tunnel-windows-client` | Windows SSH 管理程序入口 |
| `internal/client` | 连接、TLS、动态映射、本机 WebUI、配置保护 |
| `internal/common` | TCP/UDP 协议、转发、旧格式凭据兼容 |
| `internal/launcher` | 控制台启动逻辑 |
| `internal/winclient` | Windows 服务、SSH 管理、用户确认协助 |
| `packaging` | 两端启动/安装脚本、npm 包装器源码 |
| `examples` | 无接入凭据的示例配置 |
| `third_party` | 外部依赖许可证及组件来源说明 |

开发机安装 Go **1.26.8**，在本目录执行 `sh build.sh`，Windows 执行 `powershell -File build.ps1`。脚本使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false`、`-ldflags="-s -w"`，输出三种自有原生程序。客户无需 Go。

```bash
go test -buildvcs=false -p=1 ./...
go vet -buildvcs=false ./...
```

Windows 专有单元测试必须在 Windows 上执行，Linux 文件权限测试必须在 Linux 上执行。交叉编译成功不等于已经完成对应系统的运行验收。

第三方代码不冒充自有源码：Go 依赖版本锁定于 go.mod/go.sum；OpenSSH 上游源码链接和固定组件哈希见 `third_party/README.md`，已编译组件仅随 SSH Release 包分发。npm 包装器源码保留供检查；本次不代表 npm 原包恢复发布。

## 安全边界与许可

不包含客户令牌、管理员密码、SSH 私钥、服务器状态、运维备份或 npm/GitHub 发布凭据。默认启用 TLS；Linux 私密配置由所属用户以 0600 保存，Windows 凭据使用当前用户 DPAPI。面板验证当前操作系统用户的独立管理令牌。

`1cat1.` 旧凭据编码中的固定 AES key 只是历史兼容格式，不是服务端认证密钥，也不能作为保密保证。任何获得完整 bootstrap token 的人都能读出其中的节点凭据，因此仍应将整个 token 当作密码保护；服务器认证依赖独立的随机节点凭据。

本次只公开自有源码，**未擅自新增 MIT、Apache 等开源许可证**，保持现有 npm 的 `UNLICENSED` 声明。第三方组件的原有许可证不变。
