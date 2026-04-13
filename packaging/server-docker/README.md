# 1cat Tunnel Server Docker Compose Package

这个目录是给 Linux `amd64` 服务端准备的 Docker Compose 交付包。

## 包内文件

- `Dockerfile`
- `docker-compose.yml`
- `data/`
- `server.json`
- `tunnel-server`
- `README.md`

## 为什么默认用 host network

服务端会在 `50000-55000` 端口池里动态分配 TCP / UDP 公网入口。
在 Linux 服务器上直接使用 `network_mode: host` 最省事：

- 不需要手工把整段端口范围写成 `ports`
- TCP / UDP 都能直接按服务端配置监听
- 更适合这类动态端口池服务

## 首次部署

1. 编辑 `server.json`
2. 至少修改：
   - `public_host`
   - `admin_password`
   - `auto_port_start`
   - `auto_port_end`
3. 启动：

```bash
docker compose up -d --build
```

4. 查看日志：

```bash
docker compose logs -f
```

## 说明

- `server.json` 里的 `state_file` 已经改成容器内可写路径 `/var/lib/1cat-tunnel/server-state.json`
- Compose 会把宿主机当前目录下的 `./data` 挂载到容器里的 `/var/lib/1cat-tunnel`
- 控制端口、网页端口和动态分配端口都直接由宿主机网络栈承载

启动后访问：

```text
http://YOUR_SERVER_PUBLIC_IP:8080
```

登录控制台后，再为每个节点单独签发 bootstrap token。
