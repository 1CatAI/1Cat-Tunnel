//go:build windows

package winclient

import (
	"os"
	"strings"
)

var statusErrorTranslations = strings.NewReplacer(
	"Access is denied.", "访问被拒绝。",
	"The system cannot find the file specified.", "系统找不到指定的文件。",
	"The system cannot find the path specified.", "系统找不到指定的路径。",
	"The service has not been started.", "服务尚未启动。",
	"The specified service does not exist as an installed service.", "指定服务尚未安装。",
	"The wait operation timed out.", "等待操作超时。",
	"The pipe has been ended.", "通信管道已关闭。",
	"The handle is invalid.", "系统句柄无效。",
)

func statusErrorText(err error) string {
	if err == nil {
		return ""
	}
	if os.IsPermission(err) {
		return "访问被拒绝"
	}
	if os.IsNotExist(err) {
		return "未找到所需文件"
	}

	translated := statusErrorTranslations.Replace(strings.TrimSpace(err.Error()))
	for _, r := range translated {
		if r >= '\u4e00' && r <= '\u9fff' {
			return translated
		}
	}
	return "系统错误；请查看日志或使用管理员终端重试"
}
