//go:build windows

package winclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"tunnel/internal/client"
	"tunnel/internal/common"
)

const (
	tunnelServiceName        = "1CatTunnelClient"
	tunnelServiceDisplayName = "1CatTunnel 客户端服务"
	installedExecutableName  = "1cattunnel.exe"
	clientConfigName         = "client.json"
	clientLogName            = "client.log"
	installStateName         = "install-state.json"
)

type paths struct {
	InstallRoot     string
	DataRoot        string
	BackupRoot      string
	SupportKeysRoot string
	SupportKey      string
	Executable      string
	Config          string
	Log             string
	State           string
}

type installState struct {
	Version                 string `json:"version"`
	InstalledAt             string `json:"installed_at"`
	OpenSSHInstalledByUs    bool   `json:"openssh_installed_by_1cattunnel"`
	OpenSSHConfigManaged    bool   `json:"openssh_config_managed_by_1cattunnel"`
	OpenSSHConfigBackupPath string `json:"openssh_config_backup_path,omitempty"`
}

type installOptions struct {
	NonInteractive bool
	OfflineOpenSSH bool
	Monitor        bool
	SupportKeyPath string
}

func Main(args []string) error {
	enableUTF8Console()
	isService, err := svc.IsWindowsService()
	if err == nil && isService {
		return svc.Run(tunnelServiceName, &tunnelWindowsService{})
	}

	defaultLaunch := len(args) == 0
	command := "install"
	if len(args) > 0 {
		command = strings.ToLower(strings.TrimLeft(strings.TrimSpace(args[0]), "-/"))
		args = args[1:]
	}

	switch command {
	case "", "install", "setup", "安装":
		options, err := parseInstallOptions(args)
		if err != nil {
			return err
		}
		if defaultLaunch {
			options.Monitor = true
		}
		if options.NonInteractive && !windows.GetCurrentProcessToken().IsElevated() {
			return errors.New("--non-interactive 必须在管理员终端中运行，以免接入凭据在 UAC 提权过程中丢失或暴露")
		}
		relaunchArgs := []string{"install"}
		if options.NonInteractive {
			relaunchArgs = append(relaunchArgs, "--non-interactive")
		}
		if options.OfflineOpenSSH {
			relaunchArgs = append(relaunchArgs, "--offline-openssh")
		}
		if options.SupportKeyPath != "" {
			relaunchArgs = append(relaunchArgs, "--support-key", options.SupportKeyPath)
		}
		if options.Monitor {
			relaunchArgs = append(relaunchArgs, "--monitor")
		}
		elevated, err := ensureElevated(relaunchArgs)
		if err != nil || !elevated {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		installErr := install(ctx, os.Stdout, options)
		cancel()
		if installErr != nil {
			return installErr
		}
		if options.Monitor {
			return runMonitor(os.Stdout)
		}
		return nil
	case "monitor", "watch", "监视":
		elevated, err := ensureElevated([]string{"monitor"})
		if err != nil || !elevated {
			return err
		}
		return runMonitor(os.Stdout)
	case "support", "remote-support", "协助":
		return runSupportGUI(false)
	case "support-stop", "stop-support", "remote-support-stop", "结束协助":
		return runSupportGUI(true)
	case "support-authorized-keys":
		return runSupportAuthorizedKeys(args, os.Stdout)
	case "support-shell":
		return runSupportShell(args)
	case "repair", "修复":
		elevated, err := ensureElevated([]string{"repair"})
		if err != nil || !elevated {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return repair(ctx, os.Stdout)
	case "status", "状态":
		return printStatus(os.Stdout)
	case "uninstall", "卸载":
		elevated, err := ensureElevated([]string{"uninstall"})
		if err != nil || !elevated {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return uninstall(ctx, os.Stdout)
	case "run", "运行":
		return runForeground(args)
	case "version", "版本":
		fmt.Fprintln(os.Stdout, common.Version)
		return nil
	case "help", "h", "?", "帮助":
		printHelp(os.Stdout)
		return nil
	default:
		return fmt.Errorf("未知命令 %q；请运行 1cattunnel.exe help 查看帮助", command)
	}
}

func install(ctx context.Context, output io.Writer, options installOptions) (resultErr error) {
	p, err := systemPaths()
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "1CatTunnel Windows SSH 安装程序")
	fmt.Fprintln(output, "================================")
	fmt.Fprintf(output, "版本：%s\n", common.Version)
	fmt.Fprintf(output, "外部 SSH 登录账户：%s\n", sshLoginAccount())
	fmt.Fprintln(output, "安装完成后，即使 Windows 重启，SSH 仍会通过隧道自动保持可访问。")
	fmt.Fprintln(output)

	if err := prepareProtectedDirectories(p); err != nil {
		return err
	}
	if err := installSupportOperatorKey(p, options.SupportKeyPath); err != nil {
		return err
	}
	state, _ := loadInstallState(p.State)

	cfg, err := prepareClientConfig(p.Config, options)
	if err != nil {
		return err
	}
	if err := client.SaveConfig(p.Config, cfg); err != nil {
		return fmt.Errorf("保存本机客户端配置失败：%w", err)
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

	sshResult, err := ensureOpenSSH(ctx, p, state, options.OfflineOpenSSH)
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
	if err := restrictPathToSystemAndAdministrators(p.State, false); err != nil {
		return err
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "安装完成。")
	fmt.Fprintf(output, "客户端服务：%s（自动启动）\n", tunnelServiceName)
	fmt.Fprintln(output, "本地 SSH 目标：127.0.0.1:22")
	account := sshLoginAccount()
	fmt.Fprintf(output, "外部 SSH 登录账户：%s\n", account)
	if assignment := waitForSSHAssignment(ctx, p.Log, 35*time.Second); assignment != "" {
		fmt.Fprintf(output, "公网映射：%s\n", assignment)
		fmt.Fprintf(output, "连接命令：%s\n", sshConnectionCommandForAccount(assignment, account))
	} else {
		fmt.Fprintln(output, "公网映射仍在连接中，请稍后运行 '1cattunnel.exe status' 查看状态。")
	}
	fmt.Fprintln(output, "普通登录请使用该 Windows 账户的密码，或已授权的 SSH 公钥。")
	if supportOperatorKeyConfigured(p) {
		fmt.Fprintln(output, "如需免密码远程协助，请双击“开始远程协助.bat”。")
	}
	return nil
}

func prepareClientConfig(path string, options installOptions) (client.Config, error) {
	if _, statErr := os.Stat(path); statErr == nil {
		cfg, err := client.LoadConfig(path)
		if err != nil {
			return client.Config{}, fmt.Errorf("现有本机配置无效，为避免数据丢失，已拒绝覆盖 %s：%w", path, err)
		}
		return ensureSSHPreset(cfg), nil
	} else if !os.IsNotExist(statErr) {
		return client.Config{}, fmt.Errorf("检查现有本机配置失败：%w", statErr)
	}

	for _, candidate := range legacyConfigCandidates() {
		cfg, err := client.LoadConfig(candidate)
		if err == nil {
			fmt.Printf("正在从 %s 迁移现有客户端身份\n", candidate)
			return ensureSSHPreset(cfg), nil
		}
	}

	hostname, _ := os.Hostname()
	seed := client.Config{
		ServerAddr:           "dx.1catai.com:50001",
		NodeName:             hostname,
		ReconnectIntervalSec: 5,
		TLSEnabled:           true,
		TLSServerName:        "dx.1catai.com",
		Presets: []common.Preset{{
			Name:        "ssh",
			LocalAddr:   "127.0.0.1:22",
			Description: "通过 1CatTunnel 映射 Windows OpenSSH",
			Protocol:    common.NetworkTCP,
		}},
	}
	if options.NonInteractive {
		credential := strings.TrimSpace(os.Getenv("ONECAT_TUNNEL_ENROLLMENT_PASSWORD"))
		_ = os.Unsetenv("ONECAT_TUNNEL_ENROLLMENT_PASSWORD")
		if credential == "" {
			return client.Config{}, errors.New("使用 --non-interactive 时必须设置 ONECAT_TUNNEL_ENROLLMENT_PASSWORD")
		}
		seed.EnrollmentPassword = credential
		seed.Token = credential
		if nodeName := strings.TrimSpace(os.Getenv("ONECAT_TUNNEL_NODE_NAME")); nodeName != "" {
			seed.NodeName = nodeName
		}
		_ = os.Unsetenv("ONECAT_TUNNEL_NODE_NAME")
		return seed, nil
	}
	if err := client.SaveConfig(path, seed); err != nil {
		return client.Config{}, fmt.Errorf("创建客户端初始配置失败：%w", err)
	}
	return client.BootstrapConfig(path)
}

func parseInstallOptions(args []string) (installOptions, error) {
	options := installOptions{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "--non-interactive", "/non-interactive":
			options.NonInteractive = true
		case "--offline-openssh", "/offline-openssh":
			options.OfflineOpenSSH = true
		case "--monitor", "/monitor":
			options.Monitor = true
		case "--support-key", "/support-key":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return installOptions{}, fmt.Errorf("%s 需要提供公钥文件路径", arg)
			}
			index++
			options.SupportKeyPath = strings.TrimSpace(args[index])
		default:
			return installOptions{}, fmt.Errorf("未知安装选项 %q", arg)
		}
	}
	return options, nil
}

func ensureSSHPreset(cfg client.Config) client.Config {
	found := false
	for i := range cfg.Presets {
		if strings.EqualFold(strings.TrimSpace(cfg.Presets[i].Name), "ssh") {
			cfg.Presets[i].LocalAddr = "127.0.0.1:22"
			cfg.Presets[i].Protocol = common.NetworkTCP
			found = true
		}
	}
	if !found {
		cfg.Presets = append(cfg.Presets, common.Preset{
			Name:        "ssh",
			LocalAddr:   "127.0.0.1:22",
			Description: "通过 1CatTunnel 映射 Windows OpenSSH",
			Protocol:    common.NetworkTCP,
		})
	}
	return cfg
}

func legacyConfigCandidates() []string {
	executable, _ := os.Executable()
	exeDir := filepath.Dir(executable)
	candidates := []string{
		filepath.Join(exeDir, "client-windows.json"),
		filepath.Join(exeDir, "client.json"),
	}
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		candidates = append(candidates,
			filepath.Join(appData, "1cat-tunnel", "client-windows.json"),
			filepath.Join(appData, "1cat-tunnel", "client.json"),
		)
	}
	return candidates
}

func systemPaths() (paths, error) {
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	if programFiles == "" || programData == "" {
		return paths{}, errors.New("无法读取 Windows 环境变量 ProgramFiles 或 ProgramData")
	}
	installRoot := filepath.Join(programFiles, "1CatTunnel")
	dataRoot := filepath.Join(programData, "1CatTunnel")
	return paths{
		InstallRoot:     installRoot,
		DataRoot:        dataRoot,
		BackupRoot:      filepath.Join(dataRoot, "backups"),
		SupportKeysRoot: filepath.Join(dataRoot, "support-keys"),
		SupportKey:      filepath.Join(dataRoot, "support-operator.pub"),
		Executable:      filepath.Join(installRoot, installedExecutableName),
		Config:          filepath.Join(dataRoot, clientConfigName),
		Log:             filepath.Join(dataRoot, clientLogName),
		State:           filepath.Join(dataRoot, installStateName),
	}, nil
}

func prepareProtectedDirectories(p paths) error {
	for _, path := range []string{p.InstallRoot, p.DataRoot, p.BackupRoot, p.SupportKeysRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("创建 %s 失败：%w", path, err)
		}
	}
	if err := restrictPathToClientRuntime(p.DataRoot, true); err != nil {
		return err
	}
	if err := restrictPathToSystemAndAdministrators(p.BackupRoot, true); err != nil {
		return err
	}
	return nil
}

func ensureElevated(relaunchArgs []string) (bool, error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return true, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(executable)
	params, _ := windows.UTF16PtrFromString(joinWindowsArgs(relaunchArgs))
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err := windows.ShellExecute(0, verb, file, params, cwd, windows.SW_SHOWNORMAL); err != nil {
		return false, fmt.Errorf("申请管理员权限失败：%w", err)
	}
	return false, nil
}

func joinWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	return strings.Join(quoted, " ")
}

func loadInstallState(path string) (installState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return installState{}, err
	}
	var state installState
	if err := json.Unmarshal(data, &state); err != nil {
		return installState{}, err
	}
	return state, nil
}

func saveInstallState(path string, state installState) error {
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(path, payload, 0o600)
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, "1CatTunnel Windows SSH 客户端")
	fmt.Fprintln(output, "  1cattunnel.exe install       安装 OpenSSH 和自动启动的隧道服务")
	fmt.Fprintln(output, "  1cattunnel.exe status        查看 SSH、服务和公网映射状态")
	fmt.Fprintln(output, "  1cattunnel.exe monitor       常驻显示实时 TCP 连接")
	fmt.Fprintln(output, "  1cattunnel.exe support       打开免密码远程协助确认窗口")
	fmt.Fprintln(output, "  1cattunnel.exe support-stop  结束当前远程协助会话")
	fmt.Fprintln(output, "  1cattunnel.exe repair        检查并修复受管理组件")
	fmt.Fprintln(output, "  1cattunnel.exe uninstall     删除隧道服务，保留 SSH 密钥和配置")
	fmt.Fprintln(output, "  1cattunnel.exe run           在前台运行已安装客户端")
	fmt.Fprintln(output, "  1cattunnel.exe version       显示版本号")
}
