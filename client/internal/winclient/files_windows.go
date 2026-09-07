//go:build windows

package winclient

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func installExecutable(p paths) error {
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位安装程序失败：%w", err)
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(p.Executable)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(source), filepath.Clean(target)) {
		return nil
	}

	targetExists := false
	if _, err := os.Stat(target); err == nil {
		targetExists = true
		sourceHash, hashErr := fileSHA256(source)
		if hashErr != nil {
			return fmt.Errorf("计算安装程序校验值失败：%w", hashErr)
		}
		targetHash, hashErr := fileSHA256(target)
		if hashErr != nil {
			return fmt.Errorf("计算已安装程序校验值失败：%w", hashErr)
		}
		if sourceHash == targetHash {
			return nil
		}
		backupDir, mkdirErr := os.MkdirTemp(p.BackupRoot, "client-update-"+time.Now().Format("20060102-150405")+"-")
		if mkdirErr != nil {
			return mkdirErr
		}
		if err := copyFile(target, filepath.Join(backupDir, installedExecutableName), 0o700); err != nil {
			return fmt.Errorf("备份已安装客户端失败：%w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查已安装客户端失败：%w", err)
	}

	staged, err := os.CreateTemp(p.InstallRoot, ".1cattunnel-*.new")
	if err != nil {
		return fmt.Errorf("创建客户端更新暂存文件失败：%w", err)
	}
	temp := staged.Name()
	if err := staged.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	defer os.Remove(temp)
	if err := copyFile(source, temp, 0o700); err != nil {
		return fmt.Errorf("暂存客户端更新失败：%w", err)
	}
	if err := replaceFileAtomic(temp, target); err != nil {
		if targetExists {
			return fmt.Errorf("保留旧程序并原子替换已安装客户端失败：%w", err)
		}
		return fmt.Errorf("启用已安装客户端失败：%w", err)
	}
	_ = pruneBackupDirectories(p.BackupRoot, "client-update-", 3)
	return nil
}

func pruneBackupDirectories(root, prefix string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var directories []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			directories = append(directories, entry)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Name() > directories[j].Name()
	})
	for _, entry := range directories[min(keep, len(directories)):] {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".1cat-write-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFileAtomic(tempPath, path)
}

func restrictPathToSystemAndAdministrators(path string, directory bool) error {
	return restrictPathWithExtraSIDs(path, directory, nil)
}

func restrictPathToClientRuntime(path string, directory bool) error {
	installed, _, err := serviceState(tunnelServiceName)
	if err != nil {
		return fmt.Errorf("为 %s 设置访问权限前查询隧道服务失败：%w", path, err)
	}
	if !installed {
		return restrictPathToSystemAndAdministrators(path, directory)
	}
	return restrictPathToTunnelService(path, directory)
}

func restrictPathToTunnelService(path string, directory bool) error {
	sid, _, _, err := windows.LookupSID("", `NT SERVICE\`+tunnelServiceName)
	if err != nil {
		return fmt.Errorf("解析隧道服务 SID 失败：%w", err)
	}
	return restrictPathWithExtraSIDs(path, directory, []string{sid.String()})
}

func restrictPathToTunnelServiceRuntime(path string, directory bool) error {
	sid, _, _, err := windows.LookupSID("", `NT SERVICE\`+tunnelServiceName)
	if err != nil {
		return fmt.Errorf("解析隧道服务 SID 失败：%w", err)
	}
	return restrictPathWithExtraSIDsAndOwner(path, directory, []string{sid.String()}, false)
}

func restrictPathWithExtraSIDs(path string, directory bool, extraSIDs []string) error {
	return restrictPathWithExtraSIDsAndOwner(path, directory, extraSIDs, true)
}

func restrictPathWithExtraSIDsAndOwner(path string, directory bool, extraSIDs []string, setOwner bool) error {
	flags := ""
	if directory {
		flags = "OICI"
	}
	aces := []string{
		fmt.Sprintf("(A;%s;FA;;;SY)", flags),
		fmt.Sprintf("(A;%s;FA;;;BA)", flags),
	}
	for _, sid := range extraSIDs {
		aces = append(aces, fmt.Sprintf("(A;%s;FA;;;%s)", flags, sid))
	}
	sddl := "O:BAD:P" + strings.Join(aces, "")
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("为 %s 创建受保护访问权限失败：%w", path, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("读取 %s 的受保护访问权限失败：%w", path, err)
	}
	securityInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION |
		windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var owner *windows.SID
	if setOwner {
		owner, _, err = descriptor.Owner()
		if err != nil {
			return fmt.Errorf("读取 %s 的受保护所有者失败：%w", path, err)
		}
		securityInfo |= windows.OWNER_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInfo, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("替换 %s 的访问权限失败：%w", path, err)
	}
	return nil
}

func replaceFileAtomic(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
