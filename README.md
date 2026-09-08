# 1Cat Tunnel

## 最新客户端下载与源码

Linux 客户端 **0.5.3** 已发布，Windows 继续使用 **0.5.2-security**。完整自有客户端源码、测试和构建脚本位于 [client/](client/)，安装包见 [GitHub Releases](https://github.com/1CatAI/1Cat-Tunnel/releases)。

Linux x86_64 一键安装（首次仅安装命令，完成 setup 后登记开机自启并在当前终端运行）：

```bash
curl -fsSL https://github.com/1CatAI/1Cat-Tunnel/releases/latest/download/install.sh | sh -s -- --yes
1cattunnel -setup
```

不需要 npm、Node.js 或 Go。终端向导手动填写接入凭据；客户端显示实际服务器 IP、端口、映射和日志，再次启动会接管旧实例，桌面登录后自动显示状态窗口。WebUI 和显式配置文件仍可使用。详见 [客户端说明](client/README.md)。

**下方以及仓库根目录原有服务端/客户端代码是历史 0.1.2 内容，保留用于兼容和追溯，不是本次的生产服务端升级。新版客户端请在 `client/` 中编译，不要在根目录构建旧客户端。**

---

# 历史版本：1Cat Tunnel 0.1.2

`1Cat Tunnel` 是一个轻量级反向访问工具，适合这种场景：

- 你有一台长期在线的公网 Linux 服务器
- 客户现场机器没有公网 IP，或者不方便做入站端口开放
- 客户端只负责主动回连
- 服务端负责统一入口、动态分配公网端口、Web 管理和状态展示

当前版本的核心能力：

- 每节点独立凭据，不再使用全局共享 token
- 支持 `TCP + UDP`
- 映射和累计会话状态支持重启恢复
- Linux 服务端
- Linux / Windows 客户端
- Web 控制台
- 自动端口池分配
- Windows / Linux 首启向导

## 1. 核心工作方式

整体链路如下：

1. 服务端启动后提供控制端口和 Web 控制台
2. 管理员在控制台为某个节点签发专属 `bootstrap token`
3. 客户端首次启动时粘贴这个 token，自动完成接入配置
4. 客户端向服务端注册自己的节点名和本地预设端口
5. 服务端为这些预设自动分配公网端口并开始监听
6. 外部访问公网端口时，服务端通知对应客户端建立数据通道
7. 客户端再连接本地服务并完成双向转发

这套模式的优点是：

- 客户端不需要公网 IP
- 客户端不需要开放入站端口
- 同一台服务端可以长期托管多个客户节点
- 每个节点的凭据可以单独签发、单独轮换、单独删除

## 2. 0.1.2 版本重点

### 2.1 每节点独立凭据

服务端控制台现在按节点签发专属 `bootstrap token`。

这意味着：

- 一个节点泄露凭据，不会影响其他节点
- 可以只重置单个节点的接入凭据
- 更适合长期生产托管

### 2.2 TCP + UDP

`preset` 现在支持通过 `protocol` 字段声明协议：

- `tcp`
- `udp`

服务端控制台会显示每条映射的协议、端口、状态、连接数和流量。

### 2.3 重启恢复

服务端会持久化以下信息：

- 已签发节点
- 节点凭据
- 节点最近一次上报的预设
- 已分配的公网端口映射
- 累计连接数
- 累计流量
- 最近活动时间
- 最近错误

说明：

- 正在进行中的实时会话不会跨进程续活
- 服务端重启后，客户端会重新连接
- 已分配的公网端口和累计状态会恢复

## 3. 当前推荐的默认预设

### Linux 客户端

默认 SSH 预设为：

```json
{
  "name": "ssh",
  "local_addr": "0.0.0.0:22",
  "description": "一键映射本机 SSH",
  "protocol": "tcp"
}
```

### Windows 客户端

默认 RDP 预设为：

```json
{
  "name": "rdp",
  "local_addr": "127.0.0.1:3389",
  "description": "一键映射本机 RDP",
  "protocol": "tcp"
}
```

如果在首启向导里选择 `SSH` 或 `RDP + SSH`，SSH 默认值同样是 `0.0.0.0:22`。

## 4. 快速开始

### 4.1 启动服务端

先编辑 `examples/server.json`，至少确认这些字段：

- `public_host`
- `control_listen_addr`
- `http_listen_addr`
- `auto_port_start`
- `auto_port_end`
- `admin_username`
- `admin_password`
- `state_file`

启动服务端：

```bash
./tunnel-server -config ./server.json
```

服务端启动后会打印：

- Web 控制台地址
- 客户端控制地址
- 当前版本号
- 状态文件路径

### 4.2 控制台签发节点

登录 Web 控制台后：

1. 输入节点名
2. 点击签发节点凭据
3. 将返回的 `bootstrap token` 发给对应客户机

控制台现在支持：

- 节点签发
- 节点删除并停止服务
- 重新同步节点端口
- 重新分配随机端口
- 查看节点、端口、流量、会话状态

### 4.3 启动客户端

Linux：

```bash
./tunnel-client -config ./client-linux.json
```

Windows：

```powershell
.\tunnel-client.exe -config .\client-windows.json
```

如果配置缺失或仍然是模板，客户端会进入首启向导：

1. 粘贴节点专属 `bootstrap token`
2. 自动识别服务端地址和节点名
3. 选择要开放的预设
4. 确认本地地址
5. 自动保存配置并开始运行

### 4.4 访问客户节点

如果服务端为某 Linux 节点的 SSH 预设分配了 `54401/tcp`，你可以这样连接：

```bash
ssh -p 54401 user@YOUR_SERVER_PUBLIC_HOST
```

如果为某 Windows 节点的 RDP 预设分配了 `51788/tcp`，你可以这样连接：

```powershell
mstsc /v:YOUR_SERVER_PUBLIC_HOST:51788
```

## 5. 配置说明

### 5.1 服务端配置

参考文件：

- `examples/server.json`

关键字段：

- `control_listen_addr`
  客户端控制连接入口
- `http_listen_addr`
  Web 控制台监听地址
- `public_bind_addr`
  公网映射监听绑定地址，通常为 `0.0.0.0`
- `public_host`
  客户端和控制台展示使用的公网域名或公网 IP
- `admin_username`
  控制台管理员账号
- `admin_password`
  控制台管理员密码
- `state_file`
  服务端状态持久化文件
- `state_save_interval_sec`
  周期保存状态的间隔
- `auto_port_start` / `auto_port_end`
  自动分配的公网端口池
- `udp_session_timeout_sec`
  UDP 会话空闲超时

### 5.2 客户端配置

参考文件：

- `examples/client-linux.json`
- `examples/client-windows.json`

关键字段：

- `bootstrap_token`
  由服务端控制台为节点单独签发
- `node_name`
  节点名称
- `reconnect_interval_sec`
  自动重连间隔
- `presets`
  本地预设列表

每个 `preset` 支持：

- `name`
- `local_addr`
- `description`
- `protocol`

## 6. 构建与分发

在项目根目录执行：

```powershell
.\build.ps1
```

如果本机没有 Go，脚本会自动下载便携版 Go 到 `.toolcache/` 后继续构建。

当前构建输出包括：

- `dist/tunnel-server-linux-amd64`
- `dist/tunnel-client-linux-amd64`
- `dist/tunnel-client-windows-amd64.exe`
- `dist/packages/`
- `dist/linux-client-delivery/`
- `dist/windows-client-delivery/`
- `dist/server-docker-deployment/`

推荐交付方式：

- 发客户：优先使用 `dist/linux-client-delivery/` 或 `dist/windows-client-delivery/`
- 发运维：使用 `dist/packages/` 下的完整包
- Docker 部署服务端：使用 `dist/server-docker-deployment/`

## 7. Docker Compose 部署服务端

服务端 Docker 交付目录包含：

- `Dockerfile`
- `docker-compose.yml`
- Docker 专用 `server.json`
- 预编译 `tunnel-server`

使用方式：

```bash
cd dist/server-docker-deployment
mkdir -p data
docker compose up -d --build
```

当前 Compose 默认使用 `host` 网络，更适合动态端口池场景。

## 8. 仓库结构

```text
cmd/
  tunnel-server/        服务端入口
  tunnel-client/        客户端入口

internal/
  client/               客户端配置、首启向导、连接逻辑
  common/               协议、token、代理、UDP 帧处理
  launcher/             日志与配置查找
  server/               服务端核心、控制台、状态恢复、端口映射

examples/               示例配置
packaging/              分发脚本与交付模板
docs/                   额外技术文档
build.ps1               一键构建脚本
```

## 9. 当前限制

当前版本仍然是偏轻量交付的反向访问原型，已够用，但还不是完整零信任平台。

暂未内建：

- TLS 终端到终端加密
- 多租户权限模型
- 审批流
- 会话录像
- 文件传输管理
- 活跃会话跨重启续传

如果要长期放在生产环境，建议至少补齐：

- HTTPS 或反向代理
- 外层访问控制
- 管理员密码轮换
- 日志归档与审计

## 10. 额外说明

- `docs/REMOTE_DESKTOP_DEVELOPMENT.md` 保留了远程桌面能力的技术规划
- 客户端默认是“主动回连”模式，不需要在客户机开入站端口
- 节点删除后，会同步断开控制连接并释放该节点名下所有公网映射

## 11. 适合的使用方式

这套项目更适合：

- 一台公网服务端长期托管多个客户节点
- 以 SSH / RDP / 内网 TCP 服务 / UDP 服务为主的远程访问
- 需要低依赖、可直接分发、客户端尽量少安装东西的场景

如果你的目标是：

- 浏览器内远程桌面
- 多人协作审计
- 完整企业权限体系

那更适合把它作为基础隧道层继续往上扩展，而不是把当前版本直接当成最终形态。
