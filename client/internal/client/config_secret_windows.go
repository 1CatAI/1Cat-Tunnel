//go:build windows

package client

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var configSecretEntropy = []byte("1CatTunnel client config v1")

func protectConfigSecretsForStorage(cfg Config) (Config, error) {
	output := cfg
	output.ProtectedBootstrap = ""
	output.ProtectedToken = ""
	output.ProtectedEnrollment = ""

	var err error
	if output.BootstrapToken != "" {
		output.ProtectedBootstrap, err = protectWindowsSecret(output.BootstrapToken)
		if err != nil {
			return Config{}, err
		}
		output.BootstrapToken = ""
	}
	if output.Token != "" {
		output.ProtectedToken, err = protectWindowsSecret(output.Token)
		if err != nil {
			return Config{}, err
		}
		output.Token = ""
	}
	if output.EnrollmentPassword != "" {
		output.ProtectedEnrollment, err = protectWindowsSecret(output.EnrollmentPassword)
		if err != nil {
			return Config{}, err
		}
		output.EnrollmentPassword = ""
	}
	return output, nil
}

func restoreConfigSecretsFromStorage(cfg Config) (Config, error) {
	output := cfg
	var err error
	if output.ProtectedBootstrap != "" {
		output.BootstrapToken, err = unprotectWindowsSecret(output.ProtectedBootstrap)
		if err != nil {
			return Config{}, fmt.Errorf("保护连接 Token 失败：%w", err)
		}
	}
	if output.ProtectedToken != "" {
		output.Token, err = unprotectWindowsSecret(output.ProtectedToken)
		if err != nil {
			return Config{}, fmt.Errorf("保护节点 Token 失败：%w", err)
		}
	}
	if output.ProtectedEnrollment != "" {
		output.EnrollmentPassword, err = unprotectWindowsSecret(output.ProtectedEnrollment)
		if err != nil {
			return Config{}, fmt.Errorf("保护接入密码失败：%w", err)
		}
	}
	output.ProtectedBootstrap = ""
	output.ProtectedToken = ""
	output.ProtectedEnrollment = ""
	return output, nil
}

func protectWindowsSecret(secret string) (string, error) {
	return protectWindowsSecretWithFlags(secret, true)
}

func protectWindowsSecretWithFlags(secret string, machine bool) (string, error) {
	plain := []byte(secret)
	defer clear(plain)
	in := bytesToDataBlob(plain)
	entropy := bytesToDataBlob(configSecretEntropy)
	var out windows.DataBlob
	name, err := windows.UTF16PtrFromString("1CatTunnel 客户端凭据")
	if err != nil {
		return "", err
	}
	flags := uint32(windows.CRYPTPROTECT_UI_FORBIDDEN)
	if machine {
		flags |= windows.CRYPTPROTECT_LOCAL_MACHINE
	}
	if err := windows.CryptProtectData(&in, name, &entropy, 0, nil, flags, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	protected := unsafe.Slice(out.Data, int(out.Size))
	return base64.StdEncoding.EncodeToString(protected), nil
}

func unprotectWindowsSecret(encoded string) (string, error) {
	protected, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解析 Windows DPAPI 数据失败：%w", err)
	}
	defer clear(protected)
	in := bytesToDataBlob(protected)
	entropy := bytesToDataBlob(configSecretEntropy)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := unsafe.Slice(out.Data, int(out.Size))
	return string(plain), nil
}

func bytesToDataBlob(value []byte) windows.DataBlob {
	if len(value) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
}
