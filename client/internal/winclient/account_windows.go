//go:build windows

package winclient

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const wtsUserNameInfoClass = 5

var (
	wtsAPI32                       = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformation = wtsAPI32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory              = wtsAPI32.NewProc("WTSFreeMemory")
)

func activeConsoleLoginAccount() string {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == ^uint32(0) {
		return ""
	}

	var buffer *uint16
	var bytesReturned uint32
	ok, _, _ := procWTSQuerySessionInformation.Call(
		0,
		uintptr(sessionID),
		wtsUserNameInfoClass,
		uintptr(unsafe.Pointer(&buffer)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if ok == 0 || buffer == nil || bytesReturned < 2 {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buffer)))

	return windows.UTF16PtrToString(buffer)
}
