//go:build windows

package winclient

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsCustomerTextIsChinese(t *testing.T) {
	var output bytes.Buffer
	printHelp(&output)
	rendered := output.String()

	for _, expected := range []string{"Windows SSH 客户端", "查看 SSH", "实时 TCP 连接", "远程协助", "检查并修复"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("帮助信息缺少 %q：%s", expected, rendered)
		}
	}
	for _, forbidden := range []string{"Install OpenSSH", "Show SSH", "Stay open", "Password-free remote-support"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("帮助信息仍包含英文客户提示 %q：%s", forbidden, rendered)
		}
	}
	if tunnelServiceDisplayName != "1CatTunnel 客户端服务" {
		t.Fatalf("Windows 服务显示名称未中文化：%q", tunnelServiceDisplayName)
	}
	if got := serviceStateName(svc.Running); got != "正在运行" {
		t.Fatalf("Windows 服务状态未中文化：%q", got)
	}
	if got := supportStateDisplay(supportStateWaiting); got != "等待远程协助方连接" {
		t.Fatalf("远程协助状态未中文化：%q", got)
	}
}

func TestServiceStartLogLocalizationKeepsUpgradeCompatibility(t *testing.T) {
	for _, line := range []string{tunnelServiceStartLogMarker, tunnelServiceLegacyStartLogMarker} {
		if !isTunnelServiceStartLogLine("2026/08/01 " + line) {
			t.Fatalf("无法识别服务启动日志：%q", line)
		}
	}
}
