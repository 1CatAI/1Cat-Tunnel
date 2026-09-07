//go:build windows

package winclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"tunnel/internal/client"
	"tunnel/internal/common"
)

func repair(ctx context.Context, output io.Writer) (resultErr error) {
	p, err := systemPaths()
	if err != nil {
		return err
	}
	if err := prepareProtectedDirectories(p); err != nil {
		return err
	}
	if err := installSupportOperatorKey(p, ""); err != nil {
		return err
	}
	state, _ := loadInstallState(p.State)

	cfg, err := client.LoadConfig(p.Config)
	if err != nil {
		return fmt.Errorf("加载已安装的客户端配置失败：%w", err)
	}
	cfg = ensureSSHPreset(cfg)
	if err := client.SaveConfig(p.Config, cfg); err != nil {
		return err
	}
	if err := restrictPathToClientRuntime(p.Config, false); err != nil {
		return err
	}
	stopState, err := stopTunnelServiceIfInstalled(ctx)
	if err != nil {
		return recoverTunnelServiceAfterFailure(stopState, err)
	}
	defer func() {
		resultErr = recoverTunnelServiceAfterFailure(stopState, resultErr)
	}()

	sshResult, err := ensureOpenSSH(ctx, p, state, false)
	if err != nil {
		return err
	}
	state.OpenSSHInstalledByUs = state.OpenSSHInstalledByUs || sshResult.InstalledByUs
	state.OpenSSHConfigManaged = state.OpenSSHConfigManaged || sshResult.ConfigManaged
	if sshResult.ConfigBackupPath != "" {
		state.OpenSSHConfigBackupPath = sshResult.ConfigBackupPath
	}

	if err := installExecutable(p); err != nil {
		return err
	}
	if err := installOrUpdateTunnelService(ctx, p); err != nil {
		return err
	}
	state.Version = common.Version
	state.InstalledAt = time.Now().Format(time.RFC3339)
	if err := saveInstallState(p.State, state); err != nil {
		return err
	}

	fmt.Fprintln(output, "修复完成。")
	return printStatus(output)
}

func uninstall(ctx context.Context, output io.Writer) error {
	p, err := systemPaths()
	if err != nil {
		return err
	}
	if err := deleteTunnelService(ctx); err != nil {
		return err
	}
	if err := protectPreservedClientData(p); err != nil {
		return fmt.Errorf("隧道服务已删除，但保留数据的权限清理失败：%w", err)
	}
	fmt.Fprintln(output, "1CatTunnel Windows 服务已删除。")
	fmt.Fprintln(output, "OpenSSH、Windows 账户、SSH 主机密钥和受保护的节点配置均已保留。")
	fmt.Fprintf(output, "保留的配置：%s\n", p.Config)

	current, _ := os.Executable()
	if !strings.EqualFold(filepath.Clean(current), filepath.Clean(p.Executable)) {
		if err := os.Remove(p.Executable); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(output, "已安装的程序仍保留在 %s（%v）\n", p.Executable, err)
		}
	} else {
		fmt.Fprintf(output, "如需彻底清理文件，请关闭此窗口后删除 %s。\n", p.InstallRoot)
	}
	return nil
}

func protectPreservedClientData(p paths) error {
	var cleanupErrors []error
	for _, directory := range []string{p.DataRoot, p.BackupRoot, p.SupportKeysRoot} {
		if _, err := os.Stat(directory); err == nil {
			if err := restrictPathToSystemAndAdministrators(directory, true); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		} else if !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	files := []string{p.Config, p.State, p.Log, p.SupportKey}
	rotatedLogs, err := filepath.Glob(p.Log + ".*")
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else {
		files = append(files, rotatedLogs...)
	}
	for _, path := range files {
		if !fileExists(path) {
			continue
		}
		if err := restrictPathToSystemAndAdministrators(path, false); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func printStatus(output io.Writer) error {
	p, err := systemPaths()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "1CatTunnel 版本：%s\n", common.Version)

	installed, status, serviceErr := serviceState(tunnelServiceName)
	switch {
	case serviceErr != nil:
		fmt.Fprintf(output, "隧道服务：不可用（%s）\n", statusErrorText(serviceErr))
	case !installed:
		fmt.Fprintln(output, "隧道服务：未安装")
	default:
		fmt.Fprintf(output, "隧道服务：%s\n", serviceStateName(status.State))
	}

	sshInstalled, sshStatus, sshErr := serviceState(openSSHServiceName)
	switch {
	case sshErr != nil:
		fmt.Fprintf(output, "OpenSSH 服务：不可用（%s）\n", statusErrorText(sshErr))
	case !sshInstalled:
		fmt.Fprintln(output, "OpenSSH 服务：未安装")
	default:
		fmt.Fprintf(output, "OpenSSH 服务：%s\n", serviceStateName(sshStatus.State))
	}

	if banner, err := readSSHBanner("127.0.0.1:22", 2*time.Second); err == nil {
		fmt.Fprintf(output, "本地 SSH：运行正常（%s）\n", banner)
	} else {
		fmt.Fprintf(output, "本地 SSH：不可用（%s）\n", statusErrorText(err))
	}
	fmt.Fprintf(output, "外部 SSH 登录账户：%s\n", sshLoginAccount())

	if cfg, err := client.LoadConfig(p.Config); err == nil {
		fmt.Fprintf(output, "节点：%s\n", cfg.NodeName)
		tlsState := "未启用"
		if cfg.TLSEnabled {
			tlsState = "已启用"
		}
		fmt.Fprintf(output, "控制服务器：%s（TLS：%s）\n", cfg.ServerAddr, tlsState)
		for _, preset := range cfg.Presets {
			fmt.Fprintf(output, "映射预设：%s %s -> %s\n", strings.ToUpper(preset.Name), strings.ToUpper(preset.Protocol), preset.LocalAddr)
		}
	} else {
		fmt.Fprintf(output, "客户端配置：不可用（%s）\n", statusErrorText(err))
		if os.IsPermission(err) {
			fmt.Fprintln(output, "受保护的详细信息：请在管理员终端中运行 status 命令")
		}
	}

	assignment, assignmentErr := latestSSHAssignmentWithError(p.Log)
	if assignmentErr != nil {
		fmt.Fprintf(output, "公网 SSH 映射：不可用（%s）\n", statusErrorText(assignmentErr))
	} else if assignment == "" {
		fmt.Fprintln(output, "公网 SSH 映射：正在等待服务器分配")
	} else {
		fmt.Fprintf(output, "公网 SSH 映射：%s\n", assignment)
		if command := sshConnectionCommand(assignment); command != "" {
			fmt.Fprintf(output, "连接命令：%s\n", command)
		}
	}
	fmt.Fprintf(output, "本机凭据保护：%s\n", credentialStorageStatus(p.Config))

	if hash, err := fileSHA256(p.Executable); err == nil {
		fmt.Fprintf(output, "已安装程序 SHA-256：%s\n", hash)
	}
	return nil
}

func sshConnectionCommand(assignment string) string {
	return sshConnectionCommandForAccount(assignment, sshLoginAccount())
}

func sshConnectionCommandForAccount(assignment, account string) string {
	host, port, err := net.SplitHostPort(assignment)
	if err != nil {
		return ""
	}
	account = strings.TrimSpace(account)
	if account == "" {
		account = "user"
	}
	return fmt.Sprintf("ssh -p %s %s@%s", port, account, host)
}

func sshLoginAccount() string {
	candidates := make([]string, 0, 3)
	candidates = append(candidates, activeConsoleLoginAccount())
	if current, err := user.Current(); err == nil {
		candidates = append(candidates, current.Username)
	}
	candidates = append(candidates, os.Getenv("USERNAME"))

	for _, candidate := range candidates {
		if account := normalizeSSHLoginAccount(candidate); account != "" {
			return account
		}
	}
	return "user"
}

func normalizeSSHLoginAccount(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if slash := strings.LastIndexAny(candidate, `\/`); slash >= 0 {
		candidate = strings.TrimSpace(candidate[slash+1:])
	}
	switch {
	case candidate == "":
		return ""
	case strings.EqualFold(candidate, "SYSTEM"):
		return ""
	case strings.EqualFold(candidate, tunnelServiceName):
		return ""
	default:
		return candidate
	}
}

func credentialStorageStatus(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "不可用"
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return "配置无效"
	}
	for _, name := range []string{"token", "enrollment_password", "bootstrap_token"} {
		if value := raw[name]; len(value) > 0 && string(value) != `""` && string(value) != "null" {
			return "检测到明文凭据（不安全）"
		}
	}
	for _, name := range []string{"token_dpapi", "enrollment_password_dpapi", "bootstrap_token_dpapi"} {
		if value := raw[name]; len(value) > 0 && string(value) != `""` && string(value) != "null" {
			return "已使用 Windows DPAPI 加密保护"
		}
	}
	return "未保存凭据"
}
