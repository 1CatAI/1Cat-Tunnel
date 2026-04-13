# 1cat Tunnel Windows Client Package

这个目录是可直接交付的 Windows 客户端包。

## 包内文件

- `tunnel-client.exe`
- `client-windows.json`
- `start-client.bat`
- `start-client.ps1`
- `README.md`

## 运行要求

- Windows `amd64`
- 不需要安装 Go
- 只需要这台机器能主动访问服务端控制地址

## 首次使用

双击 `start-client.bat` 即可。

如果配置还是示例值，程序会自动进入首启向导，让用户依次：

1. 粘贴服务端给这个节点单独签发的 bootstrap token
2. 如果 token 已绑定节点名称，程序会自动识别
3. 选择要暴露的预设
4. 确认本地地址

配置会自动保存到当前目录的 `client-windows.json`。

## 日志

运行日志默认写在当前目录的：

- `tunnel-client.log`
