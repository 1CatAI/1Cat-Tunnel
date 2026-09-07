//go:build windows

package winclient

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStatusErrorTextIsChinese(t *testing.T) {
	permissionErr := &os.PathError{Op: "open", Path: `C:\ProgramData\1CatTunnel\client.json`, Err: os.ErrPermission}
	for _, err := range []error{permissionErr, errors.New("Access is denied."), errors.New("unrecognized system failure")} {
		got := statusErrorText(err)
		if !strings.ContainsAny(got, "访问拒绝系统错误") {
			t.Fatalf("状态错误未中文化：%q", got)
		}
		if strings.Contains(got, "Access is denied") || strings.Contains(got, "unrecognized system failure") {
			t.Fatalf("状态错误仍包含英文系统提示：%q", got)
		}
	}
}
