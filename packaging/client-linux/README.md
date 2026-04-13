# 1cat Tunnel Linux Client Package

这个目录是可直接交付的 Linux 客户端包。

## 包内文件

- `tunnel-client`
- `client-linux.json`
- `start-client.sh`
- `install-client-systemd.sh`
- `README.md`

## 运行要求

- Linux `amd64`
- 不需要安装 Go
- 只需要这台客户机器能够主动访问服务端控制地址

## 首次使用

如果 `client-linux.json` 里还是示例值，建议直接在终端运行：

```bash
sh ./start-client.sh
```

程序会进入首启向导，依次完成：

1. 粘贴服务端给你的节点专属 bootstrap token
2. 如果 token 已绑定节点名，程序会自动识别
3. 选择要暴露的预设
4. 确认本地地址

如果你已经提前写好了配置，也可以直接运行相同命令启动。

## 后台常驻

如果目标机器使用 `systemd`，可以执行：

```bash
sh ./install-client-systemd.sh
```

安装脚本会自动把当前目录注册为 `1cat-tunnel-client` 服务。
