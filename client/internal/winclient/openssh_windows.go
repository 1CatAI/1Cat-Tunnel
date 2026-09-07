//go:build windows

package winclient

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	winmgr "golang.org/x/sys/windows/svc/mgr"
)

const (
	openSSHServiceName     = "sshd"
	openSSHCapabilityName  = "OpenSSH.Server~~~~0.0.1.0"
	managedSSHConfigBegin  = "# BEGIN 1CatTunnel managed SSH settings"
	managedSSHConfigEnd    = "# END 1CatTunnel managed SSH settings"
	bundledOpenSSHFileName = "OpenSSH-Win64.zip"
	bundledOpenSSHSHA256   = "23f50f3458c4c5d0b12217c6a5ddfde0137210a30fa870e98b29827f7b43aba5"
)

type openSSHEnsureResult struct {
	InstalledByUs    bool
	ConfigManaged    bool
	ConfigBackupPath string
}

func ensureOpenSSH(ctx context.Context, p paths, previous installState, preferBundled bool) (openSSHEnsureResult, error) {
	result := openSSHEnsureResult{}
	installed, _, err := serviceState(openSSHServiceName)
	if err != nil {
		return result, fmt.Errorf("查询 OpenSSH 服务失败：%w", err)
	}
	if !installed {
		if err := installOpenSSH(ctx, preferBundled); err != nil {
			return result, err
		}
		result.InstalledByUs = true
		installed, _, err = serviceState(openSSHServiceName)
		if err != nil || !installed {
			return result, fmt.Errorf("OpenSSH 安装已结束，但没有创建 sshd 服务")
		}
	}
	if err := verifyInstalledOpenSSH(ctx); err != nil {
		return result, err
	}

	manageConfig := result.InstalledByUs || previous.OpenSSHConfigManaged || hasManagedOpenSSHConfig()
	if manageConfig {
		backupPath, err := configureLoopbackOpenSSH(ctx, p)
		if err != nil {
			return result, err
		}
		result.ConfigManaged = true
		result.ConfigBackupPath = backupPath
	}
	if err := configureAndStartOpenSSHService(ctx); err != nil {
		return result, err
	}
	banner, err := readSSHBanner("127.0.0.1:22", 8*time.Second)
	if err != nil {
		return result, fmt.Errorf("OpenSSH 未能在 127.0.0.1:22 上正常响应：%w", err)
	}
	if !strings.HasPrefix(banner, "SSH-") {
		return result, fmt.Errorf("22 端口返回了意外的服务标识 %q", banner)
	}
	return result, nil
}

func hasManagedOpenSSHConfig() bool {
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	data, err := os.ReadFile(filepath.Join(programData, "ssh", "sshd_config"))
	if err != nil {
		return false
	}
	foundBegin := false
	foundEnd := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		switch strings.TrimSpace(line) {
		case managedSSHConfigBegin:
			foundBegin = true
		case managedSSHConfigEnd:
			foundEnd = true
		}
	}
	return foundBegin && foundEnd
}

func installOpenSSH(ctx context.Context, preferBundled bool) error {
	if preferBundled {
		return installBundledOpenSSH(ctx)
	}
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	dism := filepath.Join(systemRoot, "System32", "dism.exe")
	dismCtx, cancelDISM := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelDISM()
	command := exec.CommandContext(dismCtx, dism,
		"/Online",
		"/Add-Capability",
		"/CapabilityName:"+openSSHCapabilityName,
		"/NoRestart",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	dismError := fmt.Sprintf("%v (%s)", err, strings.TrimSpace(string(output)))
	if fallbackErr := installBundledOpenSSH(ctx); fallbackErr == nil {
		return nil
	} else {
		return fmt.Errorf("安装 Windows OpenSSH 系统功能失败：%s；离线备用安装也失败：%w", dismError, fallbackErr)
	}
}

func installBundledOpenSSH(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	payloadRoot := filepath.Join(filepath.Dir(executable), "payload", "openssh")
	archivePath := filepath.Join(payloadRoot, bundledOpenSSHFileName)
	checksumPath := archivePath + ".sha256"
	expectedRaw, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("包内 OpenSSH 备用组件不可用")
	}
	checksumFields := strings.Fields(string(expectedRaw))
	if len(checksumFields) == 0 || len(checksumFields[0]) != 64 {
		return errors.New("包内 OpenSSH 校验文件无效")
	}
	expected := strings.ToLower(checksumFields[0])
	if expected != bundledOpenSSHSHA256 {
		return errors.New("包内 OpenSSH 校验值与固定发行版不匹配")
	}
	actual, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("包内 OpenSSH 的 SHA-256 校验失败")
	}

	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	if programFiles == "" {
		return errors.New("无法读取 Windows 环境变量 ProgramFiles")
	}
	target := filepath.Join(programFiles, "OpenSSH")
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	reuseExisting := false
	if entries, readErr := os.ReadDir(target); readErr == nil && len(entries) > 0 {
		if err := verifyOpenSSHSignatures(ctx, powershell, target); err != nil {
			return fmt.Errorf("现有 OpenSSH 目录未经验证，已拒绝继续使用 %s：%w", target, err)
		}
		if err := verifyPinnedOpenSSHVersion(ctx, target); err != nil {
			return fmt.Errorf("发现意外的现有 OpenSSH 目录，已拒绝覆盖 %s：%w", target, err)
		}
		reuseExisting = true
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	if !reuseExisting {
		if err := extractOpenSSHArchive(archivePath, target); err != nil {
			return fmt.Errorf("解压包内 OpenSSH 组件失败：%w", err)
		}
	}
	installer := filepath.Join(target, "install-sshd.ps1")
	if _, err := os.Stat(installer); err != nil {
		return fmt.Errorf("包内 OpenSSH 缺少 install-sshd.ps1")
	}
	if err := verifyOpenSSHSignatures(ctx, powershell, target); err != nil {
		return err
	}
	if err := verifyPinnedOpenSSHVersion(ctx, target); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", installer)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("注册包内 OpenSSH 服务失败：%w（%s）", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyPinnedOpenSSHVersion(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, filepath.Join(target, "ssh.exe"), "-V")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取包内 OpenSSH 版本失败：%w（%s）", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "OpenSSH_for_Windows_10.0p2") {
		return fmt.Errorf("包内 OpenSSH 不是固定的 10.0p2 版本：%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyInstalledOpenSSH(ctx context.Context) error {
	root, err := openSSHServiceBinaryRoot()
	if err != nil {
		return fmt.Errorf("定位已安装的 OpenSSH 服务程序失败：%w", err)
	}
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if err := verifyOpenSSHSignatures(ctx, powershell, root); err != nil {
		return fmt.Errorf("已安装的 sshd 服务不是经过验证的 Microsoft OpenSSH：%w", err)
	}
	command := exec.CommandContext(ctx, filepath.Join(root, "sshd.exe"), "-V")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("读取已安装的 OpenSSH 版本失败：%w（%s）", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "OpenSSH_for_Windows_") {
		return fmt.Errorf("已安装的 sshd 服务返回了意外版本：%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func verifyOpenSSHSignatures(ctx context.Context, powershell, target string) error {
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	var files []string
	for _, pattern := range []string{"*.exe", "*.dll", "*.ps1", "*.psm1"} {
		matches, err := filepath.Glob(filepath.Join(target, pattern))
		if err != nil {
			return fmt.Errorf("枚举包内 OpenSSH 签名文件失败：%w", err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		return errors.New("包内 OpenSSH 不包含可验证签名的程序组件")
	}
	sort.Strings(files)
	quoted := make([]string, len(files))
	for i, file := range files {
		quoted[i] = quote(file)
	}
	script := "$ErrorActionPreference='Stop'; $files=@(" + strings.Join(quoted, ",") + "); " +
		"foreach($file in $files){ $signature=Get-AuthenticodeSignature -LiteralPath $file; " +
		"$subject=[string]$signature.SignerCertificate.Subject; " +
		"if($signature.Status -ne 'Valid' -or $subject -notmatch '^CN=Microsoft (Corporation|3rd Party Application Component), O=Microsoft Corporation,'){ " +
		"throw ('Microsoft 数字签名无效: '+$file) } }"
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "RemoteSigned", "-Command", script)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("验证包内 OpenSSH 的 Microsoft 数字签名失败：%w（%s）", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func extractOpenSSHArchive(archivePath, target string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		name := filepath.ToSlash(entry.Name)
		parts := strings.Split(name, "/")
		if len(parts) > 1 && strings.EqualFold(parts[0], "OpenSSH-Win64") {
			parts = parts[1:]
		}
		if len(parts) == 0 || strings.Join(parts, "") == "" {
			continue
		}
		relative := filepath.FromSlash(strings.Join(parts, "/"))
		destination := filepath.Join(target, relative)
		rel, err := filepath.Rel(target, destination)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("压缩包中包含不安全的文件路径 %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		targetFile, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(targetFile, source)
		closeErr := targetFile.Close()
		source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func configureLoopbackOpenSSH(ctx context.Context, p paths) (string, error) {
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	sshRoot := filepath.Join(programData, "ssh")
	configPath := filepath.Join(sshRoot, "sshd_config")
	sshdPath, keygenPath, err := locateOpenSSHBinaries()
	if err != nil {
		return "", err
	}
	defaultPath := filepath.Join(filepath.Dir(sshdPath), "sshd_config_default")
	if err := os.MkdirAll(sshRoot, 0o700); err != nil {
		return "", err
	}

	original, err := os.ReadFile(configPath)
	configExisted := true
	if os.IsNotExist(err) {
		configExisted = false
		original, err = os.ReadFile(defaultPath)
	}
	if err != nil {
		return "", fmt.Errorf("读取 OpenSSH 配置模板失败：%w", err)
	}
	managed, err := buildManagedSSHDConfigForExecutable(string(original), p.Executable)
	if err != nil {
		return "", err
	}
	configChanged := !configExisted || string(original) != managed
	backupPath := ""
	if configChanged && configExisted {
		backupDir, err := os.MkdirTemp(p.BackupRoot, "openssh-config-"+time.Now().Format("20060102-150405")+"-")
		if err != nil {
			return "", err
		}
		backupPath = filepath.Join(backupDir, "sshd_config")
		if err := copyFile(configPath, backupPath, 0o600); err != nil {
			return "", fmt.Errorf("备份 sshd_config 失败：%w", err)
		}
	}
	rollbackConfig := func(operationErr error) error {
		if !configChanged {
			return operationErr
		}
		if !configExisted {
			if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
				return errors.Join(operationErr, fmt.Errorf("删除失败的 OpenSSH 配置时出错：%w", err))
			}
			return operationErr
		}
		if err := copyFile(backupPath, configPath, 0o600); err != nil {
			return errors.Join(operationErr, fmt.Errorf("恢复 OpenSSH 配置备份失败：%w", err))
		}
		return operationErr
	}
	if configChanged {
		if err := writeFileAtomic(configPath, []byte(managed), 0o600); err != nil {
			return "", rollbackConfig(err)
		}
	}
	if err := restrictPathToSystemAndAdministrators(configPath, false); err != nil {
		return "", rollbackConfig(err)
	}

	if output, err := exec.CommandContext(ctx, keygenPath, "-A").CombinedOutput(); err != nil {
		return "", rollbackConfig(fmt.Errorf("生成 OpenSSH 主机密钥失败：%w（%s）", err, strings.TrimSpace(string(output))))
	}
	if err := secureOpenSSHHostKeys(sshRoot); err != nil {
		return "", rollbackConfig(err)
	}
	if output, err := exec.CommandContext(ctx, sshdPath, "-t", "-f", configPath).CombinedOutput(); err != nil {
		return "", rollbackConfig(fmt.Errorf("验证受管理的 sshd_config 失败：%w（%s）", err, strings.TrimSpace(string(output))))
	}
	_ = pruneBackupDirectories(p.BackupRoot, "openssh-config-", 5)
	return backupPath, nil
}

func secureOpenSSHHostKeys(sshRoot string) error {
	privateKeys, err := filepath.Glob(filepath.Join(sshRoot, "ssh_host_*_key"))
	if err != nil {
		return err
	}
	if len(privateKeys) == 0 {
		return errors.New("OpenSSH 主机密钥生成完成，但没有产生私钥")
	}
	for _, keyPath := range privateKeys {
		if err := restrictPathToSystemAndAdministrators(keyPath, false); err != nil {
			return fmt.Errorf("保护 OpenSSH 主机密钥 %s 失败：%w", keyPath, err)
		}
	}
	return nil
}

func buildManagedSSHDConfig(input string) (string, error) {
	p, err := systemPaths()
	if err != nil {
		return "", err
	}
	return buildManagedSSHDConfigForExecutable(input, p.Executable)
}

func buildManagedSSHDConfigForExecutable(input, executable string) (string, error) {
	executable = filepath.Clean(strings.TrimSpace(executable))
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\"\r\n") {
		return "", errors.New("远程协助的 AuthorizedKeysCommand 需要安全的程序绝对路径")
	}
	authorizedKeysCommand := "AuthorizedKeysCommand \"" + filepath.ToSlash(executable) + "\" support-authorized-keys %u"

	normalized := strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	cleaned := make([]string, 0, len(lines)+8)
	inManagedBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == managedSSHConfigBegin {
			if inManagedBlock {
				return "", errors.New("sshd_config 包含嵌套的 1CatTunnel 管理区块")
			}
			inManagedBlock = true
			continue
		}
		if trimmed == managedSSHConfigEnd {
			if !inManagedBlock {
				return "", errors.New("sshd_config 包含无法匹配的 1CatTunnel 管理区块结束标记")
			}
			inManagedBlock = false
			continue
		}
		if inManagedBlock {
			continue
		}
		fields := strings.Fields(trimmed)
		if !strings.HasPrefix(trimmed, "#") && len(fields) > 0 && strings.EqualFold(fields[0], "ListenAddress") {
			return "", fmt.Errorf("启用仅隧道模式前必须人工检查现有 ListenAddress：%s", trimmed)
		}
		if !strings.HasPrefix(trimmed, "#") && len(fields) > 0 &&
			(strings.EqualFold(fields[0], "AuthorizedKeysCommand") || strings.EqualFold(fields[0], "AuthorizedKeysCommandUser")) {
			return "", fmt.Errorf("启用远程协助前必须人工检查现有 %s 配置", fields[0])
		}
		cleaned = append(cleaned, line)
	}
	if inManagedBlock {
		return "", errors.New("sshd_config 包含不完整的 1CatTunnel 管理区块")
	}
	cleaned = removeLegacySupportAuthorizedKeyPaths(cleaned)

	block := []string{
		managedSSHConfigBegin,
		"ListenAddress 127.0.0.1",
		"LoginGraceTime 30",
		"MaxAuthTries 4",
		"MaxStartups 16:50:32",
		"PermitEmptyPasswords no",
		"PubkeyAuthentication yes",
		"AuthorizedKeysFile .ssh/authorized_keys",
		authorizedKeysCommand,
		"AuthorizedKeysCommandUser SYSTEM",
		"PasswordAuthentication yes",
		managedSSHConfigEnd,
		"",
	}
	insertAt := len(cleaned)
	for i, line := range cleaned {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if !strings.HasPrefix(trimmed, "#") && len(fields) > 0 && strings.EqualFold(fields[0], "Match") {
			insertAt = i
			break
		}
	}
	prefix := append([]string(nil), cleaned[:insertAt]...)
	for len(prefix) > 0 && strings.TrimSpace(prefix[len(prefix)-1]) == "" {
		prefix = prefix[:len(prefix)-1]
	}
	suffix := append([]string(nil), cleaned[insertAt:]...)
	for len(suffix) > 0 && strings.TrimSpace(suffix[0]) == "" {
		suffix = suffix[1:]
	}
	output := make([]string, 0, len(cleaned)+len(block))
	output = append(output, prefix...)
	if len(output) > 0 && strings.TrimSpace(output[len(output)-1]) != "" {
		output = append(output, "")
	}
	output = append(output, block...)
	output = append(output, suffix...)
	return strings.TrimRight(strings.Join(output, "\r\n"), "\r\n") + "\r\n", nil
}

func locateOpenSSHBinaries() (string, string, error) {
	if serviceRoot, err := openSSHServiceBinaryRoot(); err == nil {
		sshd := filepath.Join(serviceRoot, "sshd.exe")
		keygen := filepath.Join(serviceRoot, "ssh-keygen.exe")
		if fileExists(sshd) && fileExists(keygen) {
			return sshd, keygen, nil
		}
	}
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	roots := []string{
		filepath.Join(systemRoot, "System32", "OpenSSH"),
		filepath.Join(programFiles, "OpenSSH"),
	}
	for _, root := range roots {
		sshd := filepath.Join(root, "sshd.exe")
		keygen := filepath.Join(root, "ssh-keygen.exe")
		if fileExists(sshd) && fileExists(keygen) {
			return sshd, keygen, nil
		}
	}
	return "", "", errors.New("安装后未找到 OpenSSH 程序文件")
}

func openSSHServiceBinaryRoot() (string, error) {
	manager, err := winmgr.Connect()
	if err != nil {
		return "", err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(openSSHServiceName)
	if err != nil {
		return "", err
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		return "", err
	}
	arguments, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil {
		return "", fmt.Errorf("解析 sshd 服务路径 %q 失败：%w", config.BinaryPathName, err)
	}
	if len(arguments) == 0 {
		return "", fmt.Errorf("sshd 服务路径为空")
	}
	if !strings.EqualFold(filepath.Base(arguments[0]), "sshd.exe") {
		return "", fmt.Errorf("sshd 服务使用了意外的程序 %q", arguments[0])
	}
	return filepath.Dir(arguments[0]), nil
}

func configureAndStartOpenSSHService(ctx context.Context) error {
	manager, err := winmgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(openSSHServiceName)
	if err != nil {
		return err
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		return err
	}
	config.StartType = winmgr.StartAutomatic
	if err := service.UpdateConfig(config); err != nil {
		return fmt.Errorf("设置 sshd 自动启动失败：%w", err)
	}
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Running {
		if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
		status, err = waitForServiceState(ctx, service, 20*time.Second, svc.Stopped)
		if err != nil || status.State != svc.Stopped {
			return fmt.Errorf("sshd 未能停止，无法重新加载配置")
		}
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动 sshd 失败：%w", err)
	}
	status, err = waitForServiceState(ctx, service, 20*time.Second, svc.Running)
	if err != nil {
		return err
	}
	if status.State != svc.Running {
		return fmt.Errorf("sshd 未能启动；状态=%s，Win32=%d，服务=%d", serviceStateName(status.State), status.Win32ExitCode, status.ServiceSpecificExitCode)
	}
	return nil
}

func readSSHBanner(address string, timeout time.Duration) (string, error) {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReaderSize(connection, 4096).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
