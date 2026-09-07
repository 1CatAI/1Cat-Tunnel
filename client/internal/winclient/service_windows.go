//go:build windows

package winclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	winmgr "golang.org/x/sys/windows/svc/mgr"
	"tunnel/internal/client"
)

type tunnelWindowsService struct{}

const tunnelServiceAccount = `NT SERVICE\` + tunnelServiceName

const (
	tunnelServiceStartLogMarker       = "正在启动 1CatTunnel Windows 服务"
	tunnelServiceLegacyStartLogMarker = "starting 1CatTunnel Windows service"
)

type tunnelServiceStopState struct {
	Installed  bool
	WasRunning bool
}

func (service *tunnelWindowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	p, err := systemPaths()
	if err != nil {
		return true, 1
	}
	cleanup, err := setupServiceLogging(p.Log)
	if err != nil {
		return true, 2
	}
	defer cleanup()

	cfg, err := client.LoadConfig(p.Config)
	if err != nil {
		log.Printf("加载服务配置失败：%v", err)
		return true, 3
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	support, supportErr := startSupportController(ctx, p)
	if supportErr != nil {
		log.Printf("启动远程协助控制器失败：%v", supportErr)
	} else {
		defer support.Close()
	}

	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, cfg)
	}()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case request, ok := <-requests:
			if !ok {
				changes <- serviceStopPendingStatus(1)
				cancel()
				if err := waitForClientShutdownWithProgress(done, 15*time.Second, func(checkpoint uint32) {
					changes <- serviceStopPendingStatus(checkpoint)
				}); err != nil {
					log.Printf("服务通道关闭后客户端停止失败：%v", err)
					return true, 5
				}
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- serviceStopPendingStatus(1)
				cancel()
				if err := waitForClientShutdownWithProgress(done, 15*time.Second, func(checkpoint uint32) {
					changes <- serviceStopPendingStatus(checkpoint)
				}); err != nil {
					log.Printf("客户端停止失败：%v", err)
					return true, 5
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				log.Printf("客户端服务运行失败：%v", err)
			} else {
				log.Printf("客户端服务意外退出")
			}
			changes <- serviceStopPendingStatus(1)
			return true, 4
		}
	}
}

func isTunnelServiceStartLogLine(line string) bool {
	return strings.Contains(line, tunnelServiceStartLogMarker) ||
		strings.Contains(line, tunnelServiceLegacyStartLogMarker)
}

func serviceStopPendingStatus(checkpoint uint32) svc.Status {
	return svc.Status{
		State:      svc.StopPending,
		CheckPoint: checkpoint,
		WaitHint:   30_000,
	}
}

func waitForClientShutdown(done <-chan error, timeout time.Duration) error {
	return waitForClientShutdownWithProgress(done, timeout, nil)
}

func waitForClientShutdownWithProgress(done <-chan error, timeout time.Duration, progress func(uint32)) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	checkpoint := uint32(1)
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			checkpoint++
			if progress != nil {
				progress(checkpoint)
			}
		case <-timer.C:
			return fmt.Errorf("客户端停止时间超过 %s", timeout)
		}
	}
}

func setupServiceLogging(path string) (func(), error) {
	file, err := newRotatingFileWriter(path, serviceLogMaxBytes, serviceLogBackups)
	if err != nil {
		return nil, err
	}
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Print(tunnelServiceStartLogMarker)
	return func() {
		log.Printf("正在停止 1CatTunnel Windows 服务")
		_ = file.Close()
	}, nil
}

func installOrUpdateTunnelService(ctx context.Context, p paths) error {
	manager, err := winmgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 Windows 服务管理器失败：%w", err)
	}
	defer manager.Disconnect()

	config := winmgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        winmgr.StartAutomatic,
		ErrorControl:     winmgr.ErrorNormal,
		BinaryPathName:   quoteWindowsServiceBinary(p.Executable),
		DisplayName:      tunnelServiceDisplayName,
		Description:      "为 Windows OpenSSH 维持加密的 1CatTunnel 公网映射。",
		Dependencies:     []string{"sshd", "Tcpip"},
		ServiceStartName: tunnelServiceAccount,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
		DelayedAutoStart: true,
	}

	service, err := manager.OpenService(tunnelServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		service, err = manager.CreateService(tunnelServiceName, p.Executable, config)
		if err != nil {
			return fmt.Errorf("创建隧道服务失败：%w", err)
		}
	} else if err != nil {
		return fmt.Errorf("打开隧道服务失败：%w", err)
	} else {
		if err := stopOpenService(ctx, service, 20*time.Second); err != nil {
			service.Close()
			return err
		}
		if err := service.UpdateConfig(config); err != nil {
			service.Close()
			return fmt.Errorf("更新隧道服务失败：%w", err)
		}
	}
	defer service.Close()
	if err := restrictPathToTunnelService(p.DataRoot, true); err != nil {
		return fmt.Errorf("授予隧道服务运行目录访问权限失败：%w", err)
	}
	if err := restrictPathToTunnelService(p.SupportKeysRoot, true); err != nil {
		return fmt.Errorf("授予隧道服务远程协助密钥访问权限失败：%w", err)
	}
	if err := restrictPathToSystemAndAdministrators(p.BackupRoot, true); err != nil {
		return fmt.Errorf("保护客户端备份目录失败：%w", err)
	}
	if fileExists(p.Config) {
		if err := restrictPathToTunnelService(p.Config, false); err != nil {
			return fmt.Errorf("授予隧道服务配置文件访问权限失败：%w", err)
		}
	}
	if fileExists(p.SupportKey) {
		if err := restrictPathToTunnelService(p.SupportKey, false); err != nil {
			return fmt.Errorf("授予隧道服务远程协助公钥访问权限失败：%w", err)
		}
	}
	if fileExists(p.Log) {
		if err := restrictPathToTunnelService(p.Log, false); err != nil {
			return fmt.Errorf("授予隧道服务日志访问权限失败：%w", err)
		}
	}

	supportAccount := sshLoginAccount()
	if err := prepareSupportKeySlot(p, supportAccount); err != nil {
		return fmt.Errorf("为账户 %s 准备远程协助密钥槽失败：%w", supportAccount, err)
	}

	if err := configureServiceRecovery(service); err != nil {
		return err
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动隧道服务失败：%w", err)
	}
	status, err := waitForServiceState(ctx, service, 25*time.Second, svc.Running)
	if err != nil {
		return err
	}
	if status.State != svc.Running {
		return fmt.Errorf("隧道服务未能启动；状态=%s，Win32=%d，服务=%d", serviceStateName(status.State), status.Win32ExitCode, status.ServiceSpecificExitCode)
	}
	return nil
}

func quoteWindowsServiceBinary(path string) string {
	return syscall.EscapeArg(path)
}

func stopTunnelServiceIfInstalled(ctx context.Context) (tunnelServiceStopState, error) {
	result := tunnelServiceStopState{}
	manager, err := winmgr.Connect()
	if err != nil {
		return result, fmt.Errorf("连接 Windows 服务管理器失败：%w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(tunnelServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("打开隧道服务失败：%w", err)
	}
	defer service.Close()
	result.Installed = true
	status, err := service.Query()
	if err != nil {
		return result, fmt.Errorf("维护前查询隧道服务失败：%w", err)
	}
	result.WasRunning = status.State != svc.Stopped
	if !result.WasRunning {
		return result, nil
	}
	return result, stopOpenService(ctx, service, 20*time.Second)
}

func startTunnelServiceIfInstalled(ctx context.Context) error {
	manager, err := winmgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 Windows 服务管理器失败：%w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(tunnelServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开隧道服务失败：%w", err)
	}
	defer service.Close()
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("重新启动隧道服务失败：%w", err)
	}
	status, err := waitForServiceState(ctx, service, 20*time.Second, svc.Running)
	if err != nil {
		return err
	}
	if status.State != svc.Running {
		return fmt.Errorf("隧道服务未恢复；状态=%s，Win32=%d，服务=%d", serviceStateName(status.State), status.Win32ExitCode, status.ServiceSpecificExitCode)
	}
	return nil
}

func recoverTunnelServiceAfterFailure(state tunnelServiceStopState, operationErr error) error {
	if operationErr == nil || !state.Installed || !state.WasRunning {
		return operationErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := startTunnelServiceIfInstalled(ctx); err != nil {
		return errors.Join(operationErr, fmt.Errorf("操作失败后恢复原先运行的隧道服务失败：%w", err))
	}
	return operationErr
}

func deleteTunnelService(ctx context.Context) error {
	manager, err := winmgr.Connect()
	if err != nil {
		return fmt.Errorf("连接 Windows 服务管理器失败：%w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(tunnelServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开隧道服务失败：%w", err)
	}
	if err := stopOpenService(ctx, service, 20*time.Second); err != nil {
		service.Close()
		return err
	}
	if err := service.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		service.Close()
		return fmt.Errorf("删除隧道服务失败：%w", err)
	}
	return service.Close()
}

func configureServiceRecovery(service *winmgr.Service) error {
	actions := []winmgr.RecoveryAction{
		{Type: winmgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: winmgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: winmgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := service.SetRecoveryActions(actions, 24*60*60); err != nil {
		return fmt.Errorf("配置服务故障恢复失败：%w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("配置服务非崩溃故障恢复失败：%w", err)
	}
	return nil
}

func stopOpenService(ctx context.Context, service *winmgr.Service, timeout time.Duration) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("查询 Windows 服务失败：%w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := service.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("停止 Windows 服务失败：%w", err)
	}
	status, err = waitForServiceState(ctx, service, timeout, svc.Stopped)
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		return fmt.Errorf("等待 Windows 服务停止失败；状态=%s", serviceStateName(status.State))
	}
	return nil
}

func waitForServiceState(ctx context.Context, service *winmgr.Service, timeout time.Duration, wanted svc.State) (svc.Status, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := service.Query()
		if err != nil {
			return svc.Status{}, fmt.Errorf("查询 Windows 服务失败：%w", err)
		}
		if status.State == wanted {
			return status, nil
		}
		if time.Now().After(deadline) {
			return status, nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}

func serviceState(serviceName string) (bool, svc.Status, error) {
	managerHandle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, svc.Status{}, err
	}
	defer windows.CloseServiceHandle(managerHandle)
	serviceNamePointer, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return false, svc.Status{}, err
	}
	serviceHandle, err := windows.OpenService(managerHandle, serviceNamePointer, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, svc.Status{}, nil
	}
	if err != nil {
		return false, svc.Status{}, err
	}
	service := &winmgr.Service{Name: serviceName, Handle: serviceHandle}
	defer service.Close()
	status, err := service.Query()
	return true, status, err
}

func serviceStateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "已停止"
	case svc.StartPending:
		return "正在启动"
	case svc.StopPending:
		return "正在停止"
	case svc.Running:
		return "正在运行"
	case svc.Paused:
		return "已暂停"
	default:
		return fmt.Sprintf("未知状态-%d", state)
	}
}

func runForeground(args []string) error {
	p, err := systemPaths()
	if err != nil {
		return err
	}
	configPath := p.Config
	if len(args) == 2 && (args[0] == "--config" || args[0] == "-config") {
		configPath = args[1]
	} else if len(args) != 0 {
		return errors.New("run 命令只接受 --config <路径>")
	}
	cfg, err := client.LoadConfig(configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return client.Run(ctx, cfg)
}

func waitForSSHAssignment(ctx context.Context, logPath string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if assignment := latestSSHAssignment(logPath); assignment != "" {
			return assignment
		}
		if time.Now().After(deadline) {
			return ""
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
}

func latestSSHAssignment(logPath string) string {
	assignment, _ := latestSSHAssignmentWithError(logPath)
	return assignment
}

func latestSSHAssignmentWithError(logPath string) (string, error) {
	var firstErr error
	readAny := false
	for index := 0; index <= serviceLogBackups; index++ {
		path := logPath
		if index > 0 {
			path = fmt.Sprintf("%s.%d", logPath, index)
		}
		file, err := os.Open(path)
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		readAny = true
		assignment, sawServiceStart, scanErr := scanAssignmentLog(file)
		closeErr := file.Close()
		if scanErr != nil || closeErr != nil {
			return "", errors.Join(scanErr, closeErr)
		}
		if assignment != "" {
			return assignment, nil
		}
		if sawServiceStart {
			return "", nil
		}
	}
	if !readAny && firstErr != nil {
		return "", firstErr
	}
	return "", nil
}

func scanAssignmentLog(reader io.Reader) (string, bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	assignment := ""
	sawServiceStart := false
	for scanner.Scan() {
		line := scanner.Text()
		if isTunnelServiceStartLogLine(line) {
			assignment = ""
			sawServiceStart = true
			continue
		}
		if value := sshAssignmentFromLogLine(line); value != "" {
			assignment = value
		}
	}
	return assignment, sawServiceStart, scanner.Err()
}

func sshAssignmentFromLogLine(line string) string {
	if !strings.Contains(line, "preset=SSH") {
		return ""
	}
	index := strings.Index(line, "public=")
	if index < 0 {
		return ""
	}
	value := strings.TrimSpace(line[index+len("public="):])
	if fields := strings.Fields(value); len(fields) > 0 {
		return fields[0]
	}
	return ""
}
