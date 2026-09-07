//go:build windows

package winclient

import "golang.org/x/sys/windows"

func enableUTF8Console() {
	const utf8CodePage = 65001
	_ = windows.SetConsoleCP(utf8CodePage)
	_ = windows.SetConsoleOutputCP(utf8CodePage)
}
