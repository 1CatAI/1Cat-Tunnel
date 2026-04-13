# 1cat Tunnel Server Package

这个目录是可直接交付的 Linux 服务端包。

## 包内文件

- `tunnel-server`
- `server.json`
- `start-server.sh`
- `install-server-systemd.sh`
- `README.md`

## 运行要求

- Linux `amd64`
- 不需要安装 Go
- 只需要保证防火墙放行：
  - 控制端口，例如 `7000`
  - 控制台端口，例如 `8080`
  - 公网端口池，例如 `50000-55000`

## 首次使用

1. 编辑 `server.json`
2. 至少修改：
   - `public_host`
   - `admin_password`
   - `state_file`
   - `auto_port_start`
   - `auto_port_end`
3. 启动：

```bash
sh ./start-server.sh
```

启动后登录控制台，为每个节点单独签发 bootstrap token，再把对应 token 发给对应客户端。

0.1.2 起不再使用全局共享 token。

## 后台常驻

如果目标机器使用 `systemd`，可以执行：

```bash
sh ./install-server-systemd.sh
```

安装脚本会自动把当前目录注册为 `1cat-tunnel-server` 服务。
