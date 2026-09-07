//go:build windows

package winclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

const (
	supportPipeName          = "\\\\.\\pipe\\1CatTunnelRemoteSupport"
	supportPipeMessageBytes  = 8 * 1024
	supportPipeInstances     = 4
	supportKeyConfigPath     = "__PROGRAMDATA__/1CatTunnel/support-keys/1cattunnel-support-%u"
	supportKeysFilePrefix    = "1cattunnel-support"
	supportActionStatus      = "status"
	supportActionStart       = "start"
	supportActionStop        = "stop"
	supportActionConnected   = "connected"
	supportActionFinished    = "finished"
	supportStateWaiting      = "waiting"
	supportStateConnected    = "connected"
	supportStateStopping     = "stopping"
	supportStateCleanupError = "cleanup-error"
	supportDialogTitle       = "1CatTunnel 远程协助"
)

const (
	messageBoxOK          = 0x00000000
	messageBoxYesNo       = 0x00000004
	messageBoxIconError   = 0x00000010
	messageBoxIconInfo    = 0x00000040
	messageBoxIconWarning = 0x00000030
	messageBoxYes         = 6
	showWindowHide        = 0
)

var (
	supportAccountPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$")
	supportKernel32       = windows.NewLazySystemDLL("kernel32.dll")
	supportAdvapi32       = windows.NewLazySystemDLL("advapi32.dll")
	supportUser32         = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleWindow  = supportKernel32.NewProc("GetConsoleWindow")
	procShowWindow        = supportUser32.NewProc("ShowWindow")
	procMessageBoxW       = supportUser32.NewProc("MessageBoxW")
	procImpersonatePipe   = supportAdvapi32.NewProc("ImpersonateNamedPipeClient")
)

type supportRequest struct {
	Action    string
	Account   string
	SessionID string
	ProcessID uint32
}

type supportResponse struct {
	OK          bool
	Error       string
	Available   bool
	Account     string
	Fingerprint string
	SessionID   string
	State       string
}

type supportCaller struct {
	Account   string
	SID       string
	ProcessID uint32
}

type supportOperatorKey struct {
	authorizedLine string
	fingerprint    string
}

type supportSession struct {
	ID          string
	Account     string
	SID         string
	KeyPath     string
	Fingerprint string
	State       string
	ProcessID   uint32
	StartedAt   time.Time
}

type supportController struct {
	p paths

	ctx       context.Context
	cancel    context.CancelFunc
	pipeName  string
	serveDone chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	sessions map[string]*supportSession

	listenerMu sync.Mutex
	listener   windows.Handle
}

func installSupportOperatorKey(p paths, requestedPath string) error {
	source := strings.TrimSpace(requestedPath)
	if source == "" {
		executable, err := os.Executable()
		if err == nil {
			candidate := filepath.Join(filepath.Dir(executable), "support-operator.pub")
			if fileExists(candidate) {
				source = candidate
			}
		}
	}
	if source == "" {
		return nil
	}

	payload, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("读取远程协助公钥失败：%w", err)
	}
	operator, err := parseSupportOperatorKey(payload)
	if err != nil {
		return fmt.Errorf("验证远程协助公钥失败：%w", err)
	}
	if err := writeFileAtomic(p.SupportKey, []byte(operator.authorizedLine+"\n"), 0o600); err != nil {
		return fmt.Errorf("保存远程协助公钥失败：%w", err)
	}
	if err := restrictPathToClientRuntime(p.SupportKey, false); err != nil {
		return err
	}
	return nil
}

func supportOperatorKeyConfigured(p paths) bool {
	_, err := loadSupportOperatorKey(p)
	return err == nil
}

func loadSupportOperatorKey(p paths) (supportOperatorKey, error) {
	payload, err := os.ReadFile(p.SupportKey)
	if err != nil {
		return supportOperatorKey{}, err
	}
	return parseSupportOperatorKey(payload)
}

func parseSupportOperatorKey(payload []byte) (supportOperatorKey, error) {
	publicKey, _, _, rest, err := ssh.ParseAuthorizedKey(payload)
	if err != nil {
		return supportOperatorKey{}, err
	}
	if strings.TrimSpace(string(rest)) != "" {
		return supportOperatorKey{}, errors.New("公钥文件必须且只能包含一个密钥")
	}
	if strings.Contains(publicKey.Type(), "-cert-v01@openssh.com") {
		return supportOperatorKey{}, errors.New("远程协助暂不支持 SSH 证书")
	}
	return supportOperatorKey{
		authorizedLine: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey))),
		fingerprint:    ssh.FingerprintSHA256(publicKey),
	}, nil
}

func startSupportController(parent context.Context, p paths) (*supportController, error) {
	if err := os.MkdirAll(p.SupportKeysRoot, 0o700); err != nil {
		return nil, err
	}
	if err := restrictPathToTunnelServiceRuntime(p.SupportKeysRoot, true); err != nil {
		return nil, err
	}
	if err := clearStaleSupportKeys(p.SupportKeysRoot); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	controller := &supportController{
		p:         p,
		ctx:       ctx,
		cancel:    cancel,
		pipeName:  supportPipeName,
		serveDone: make(chan struct{}),
		sessions:  make(map[string]*supportSession),
	}
	go controller.serve()
	return controller, nil
}

func clearStaleSupportKeys(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), supportKeysFilePrefix+"-") {
			continue
		}
		if err := clearSupportKeyContents(filepath.Join(root, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func prepareSupportKeySlot(p paths, account string) error {
	keyPath, err := supportKeyFilePath(p, account)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建远程协助密钥槽失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭远程协助密钥槽失败：%w", err)
	}
	if err := clearSupportKeyContents(keyPath); err != nil {
		return fmt.Errorf("清空远程协助密钥槽失败：%w", err)
	}
	if err := restrictPathToTunnelService(keyPath, false); err != nil {
		return fmt.Errorf("保护远程协助密钥槽失败：%w", err)
	}
	return nil
}

func writeSupportKeyContents(path string, payload []byte) (resultErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("远程协助密钥槽缺失；请以管理员身份运行 1cattunnel.exe repair：%w", err)
		}
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	if len(payload) > 0 {
		if _, err := file.Write(payload); err != nil {
			return err
		}
	}
	return file.Sync()
}

func clearSupportKeyContents(path string) error {
	return writeSupportKeyContents(path, nil)
}

func (c *supportController) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		if err := c.wakeListener(); err != nil && c.ctx.Err() == nil {
			log.Printf("停止服务时唤醒远程协助监听器失败：%v", err)
		}
		if c.serveDone != nil {
			select {
			case <-c.serveDone:
			case <-time.After(3 * time.Second):
				// Never hold the Windows service in STOP_PENDING indefinitely.
				log.Printf("远程协助监听器未在停止期限内退出")
			}
		}

		c.mu.Lock()
		sessions := make([]*supportSession, 0, len(c.sessions))
		for _, session := range c.sessions {
			sessions = append(sessions, session)
		}
		c.mu.Unlock()
		for _, session := range sessions {
			if err := c.cleanupSession(session.SID, session.ID, true); err != nil {
				log.Printf("服务停止时清理远程协助失败：%v", err)
			}
		}
	})
}

func (c *supportController) serve() {
	if c.serveDone != nil {
		defer close(c.serveDone)
	}
	for {
		if c.ctx.Err() != nil {
			return
		}
		pipe, err := createSupportPipeNamed(c.pipeName)
		if err != nil {
			if c.ctx.Err() == nil {
				log.Printf("创建远程协助通信管道失败：%v", err)
				time.Sleep(500 * time.Millisecond)
			}
			continue
		}

		c.listenerMu.Lock()
		c.listener = pipe
		c.listenerMu.Unlock()
		err = windows.ConnectNamedPipe(pipe, nil)
		c.listenerMu.Lock()
		if c.listener == pipe {
			c.listener = 0
		}
		c.listenerMu.Unlock()
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(pipe)
			if c.ctx.Err() == nil {
				log.Printf("接受远程协助通信请求失败：%v", err)
			}
			continue
		}
		if c.ctx.Err() != nil {
			_ = windows.CloseHandle(pipe)
			return
		}
		go c.handlePipe(pipe)
	}
}

func (c *supportController) wakeListener() error {
	c.listenerMu.Lock()
	listener := c.listener
	c.listenerMu.Unlock()
	if listener == 0 || listener == windows.InvalidHandle {
		return nil
	}

	name, err := windows.UTF16PtrFromString(c.pipeName)
	if err != nil {
		return err
	}
	client, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	return windows.CloseHandle(client)
}

func createSupportPipeNamed(pipeName string) (windows.Handle, error) {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)")
	if err != nil {
		return 0, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return 0, err
	}
	return windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
		supportPipeInstances,
		supportPipeMessageBytes,
		supportPipeMessageBytes,
		0,
		attributes,
	)
}

func (c *supportController) handlePipe(pipe windows.Handle) {
	file := os.NewFile(uintptr(pipe), "1CatTunnel remote support pipe")
	if file == nil {
		_ = windows.CloseHandle(pipe)
		return
	}
	defer file.Close()

	response := supportResponse{}
	caller, request, err := readAuthenticatedSupportRequest(pipe, file)
	if err != nil {
		response.Error = err.Error()
		_ = writeSupportResponse(file, response)
		return
	}
	response = c.handleRequest(caller, request)
	_ = writeSupportResponse(file, response)
}

func readAuthenticatedSupportRequest(pipe windows.Handle, file *os.File) (supportCaller, supportRequest, error) {
	// Windows only permits named-pipe impersonation after the server has read
	// data from that client. The request reader enforces the protocol size cap.
	request, err := readSupportRequest(file)
	if err != nil {
		return supportCaller{}, supportRequest{}, err
	}
	caller, err := supportCallerFromPipe(pipe)
	if err != nil {
		return supportCaller{}, supportRequest{}, err
	}
	return caller, request, nil
}

func supportCallerFromPipe(pipe windows.Handle) (supportCaller, error) {
	var processID uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &processID); err != nil {
		return supportCaller{}, fmt.Errorf("识别本地调用方失败：%w", err)
	}
	if processID == 0 {
		return supportCaller{}, errors.New("本地调用方没有有效的进程标识")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateSupportPipeClient(pipe); err != nil {
		return supportCaller{}, fmt.Errorf("模拟本地调用方身份失败：%w", err)
	}
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		_ = windows.RevertToSelf()
		return supportCaller{}, fmt.Errorf("读取本地调用方身份失败：%w", err)
	}
	defer token.Close()
	if err := windows.RevertToSelf(); err != nil {
		return supportCaller{}, fmt.Errorf("恢复远程协助服务身份失败：%w", err)
	}
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return supportCaller{}, fmt.Errorf("读取本地调用方 SID 失败：%w", err)
	}
	account, _, _, err := tokenUser.User.Sid.LookupAccount("")
	if err != nil {
		return supportCaller{}, fmt.Errorf("解析本地调用方账户失败：%w", err)
	}
	account = normalizeSSHLoginAccount(account)
	if !supportAccountPattern.MatchString(account) {
		return supportCaller{}, errors.New("远程协助要求本地账户名只能包含字母、数字以及 . _ @ -")
	}
	return supportCaller{
		Account:   account,
		SID:       tokenUser.User.Sid.String(),
		ProcessID: processID,
	}, nil
}

func impersonateSupportPipeClient(pipe windows.Handle) error {
	result, _, callErr := procImpersonatePipe.Call(uintptr(pipe))
	if result != 0 {
		return nil
	}
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return callErr
	}
	return windows.ERROR_ACCESS_DENIED
}

func readSupportRequest(file *os.File) (supportRequest, error) {
	var request supportRequest
	if err := readSupportJSON(file, &request); err != nil {
		return supportRequest{}, err
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Account = normalizeSSHLoginAccount(request.Account)
	request.SessionID = strings.TrimSpace(request.SessionID)
	return request, nil
}

func writeSupportResponse(file *os.File, response supportResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(payload)+1 > supportPipeMessageBytes {
		return errors.New("远程协助响应超过协议大小限制")
	}
	payload = append(payload, '\n')
	_, err = file.Write(payload)
	return err
}

func readSupportJSON(file *os.File, target any) error {
	reader := bufio.NewReaderSize(file, supportPipeMessageBytes+1)
	payload, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(payload) > supportPipeMessageBytes {
		return errors.New("远程协助请求超过协议大小限制")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	payload = bytesTrimSpace(payload)
	if len(payload) == 0 {
		return errors.New("远程协助请求为空")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("解析远程协助请求失败：%w", err)
	}
	return nil
}

func bytesTrimSpace(value []byte) []byte {
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\t' || value[start] == '\r' || value[start] == '\n') {
		start++
	}
	end := len(value)
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\r' || value[end-1] == '\n') {
		end--
	}
	return value[start:end]
}

func (c *supportController) handleRequest(caller supportCaller, request supportRequest) supportResponse {
	switch request.Action {
	case supportActionStatus:
		return c.status(caller)
	case supportActionStart:
		if request.Account == "" || !strings.EqualFold(request.Account, caller.Account) {
			return supportResponse{Error: "只能为当前 Windows 账户启用远程协助"}
		}
		return c.start(caller)
	case supportActionStop:
		return c.stop(caller, request.SessionID)
	case supportActionConnected:
		if request.ProcessID != caller.ProcessID {
			return supportResponse{Error: "远程协助命令行身份校验失败"}
		}
		return c.connected(caller, request.SessionID, request.ProcessID)
	case supportActionFinished:
		return c.finished(caller, request.SessionID)
	default:
		return supportResponse{Error: "不支持的远程协助操作"}
	}
}

func (c *supportController) status(caller supportCaller) supportResponse {
	response := supportResponse{Account: caller.Account}
	operator, err := loadSupportOperatorKey(c.p)
	if err != nil {
		if !os.IsNotExist(err) {
			response.Error = fmt.Sprintf("远程协助公钥无效：%v", err)
		}
		return response
	}
	response.Available = true
	response.Fingerprint = operator.fingerprint

	c.mu.Lock()
	session := c.sessions[caller.SID]
	if session != nil {
		response.SessionID = session.ID
		response.State = session.State
	}
	c.mu.Unlock()
	return response
}

func (c *supportController) start(caller supportCaller) supportResponse {
	response := c.status(caller)
	if !response.Available {
		if response.Error == "" {
			response.Error = "此设备尚未配置远程协助"
		}
		return response
	}

	c.mu.Lock()
	if existing := c.sessions[caller.SID]; existing != nil {
		c.mu.Unlock()
		response.SessionID = existing.ID
		response.State = existing.State
		response.Error = "此 Windows 账户的远程协助已经启用"
		return response
	}
	sessionID, err := newSupportSessionID()
	if err != nil {
		c.mu.Unlock()
		response.Error = err.Error()
		return response
	}
	keyPath, err := supportKeyFilePath(c.p, caller.Account)
	if err != nil {
		c.mu.Unlock()
		response.Error = err.Error()
		return response
	}
	operator, err := loadSupportOperatorKey(c.p)
	if err != nil {
		c.mu.Unlock()
		response.Error = fmt.Sprintf("加载远程协助公钥失败：%v", err)
		return response
	}
	keyLine, err := buildSupportAuthorizedKeyLine(c.p.Executable, sessionID, operator.authorizedLine)
	if err != nil {
		c.mu.Unlock()
		response.Error = err.Error()
		return response
	}
	if err := writeSupportKeyContents(keyPath, []byte(keyLine+"\n")); err != nil {
		c.mu.Unlock()
		response.Error = fmt.Sprintf("启用远程协助公钥失败：%v", err)
		return response
	}
	if err := restrictPathToTunnelServiceRuntime(keyPath, false); err != nil {
		_ = clearSupportKeyContents(keyPath)
		c.mu.Unlock()
		response.Error = fmt.Sprintf("保护远程协助公钥失败：%v", err)
		return response
	}
	session := &supportSession{
		ID:          sessionID,
		Account:     caller.Account,
		SID:         caller.SID,
		KeyPath:     keyPath,
		Fingerprint: operator.fingerprint,
		State:       supportStateWaiting,
		StartedAt:   time.Now(),
	}
	c.sessions[caller.SID] = session
	c.mu.Unlock()

	response.OK = true
	response.SessionID = sessionID
	response.State = supportStateWaiting
	response.Fingerprint = operator.fingerprint
	return response
}

func (c *supportController) stop(caller supportCaller, sessionID string) supportResponse {
	response := c.status(caller)
	if response.SessionID == "" {
		response.Error = "当前没有正在进行的远程协助会话"
		return response
	}
	if sessionID != "" && sessionID != response.SessionID {
		response.Error = "远程协助会话与本次请求不匹配"
		return response
	}
	if err := c.cleanupSession(caller.SID, response.SessionID, true); err != nil {
		response.Error = err.Error()
		return response
	}
	response.OK = true
	response.SessionID = ""
	response.State = ""
	return response
}

func (c *supportController) connected(caller supportCaller, sessionID string, processID uint32) supportResponse {
	c.mu.Lock()
	session := c.sessions[caller.SID]
	if session == nil || session.ID != sessionID {
		c.mu.Unlock()
		return supportResponse{Error: "远程协助会话已失效"}
	}
	if session.State != supportStateWaiting || session.ProcessID != 0 {
		c.mu.Unlock()
		return supportResponse{Error: "远程协助会话已有一个 SSH 连接"}
	}
	session.State = supportStateConnected
	session.ProcessID = processID
	response := supportResponse{
		OK:          true,
		Available:   true,
		Account:     session.Account,
		Fingerprint: session.Fingerprint,
		SessionID:   session.ID,
		State:       session.State,
	}
	c.mu.Unlock()

	go c.watchSupportShell(session.SID, session.ID, processID)
	return response
}

func (c *supportController) finished(caller supportCaller, sessionID string) supportResponse {
	if err := c.cleanupSession(caller.SID, sessionID, false); err != nil {
		return supportResponse{Error: err.Error()}
	}
	return supportResponse{OK: true, Available: supportOperatorKeyConfigured(c.p), Account: caller.Account}
}

func (c *supportController) watchSupportShell(sid, sessionID string, processID uint32) {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, processID)
	if err != nil {
		_ = c.cleanupSession(sid, sessionID, false)
		return
	}
	defer windows.CloseHandle(process)
	_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
	if err := c.cleanupSession(sid, sessionID, false); err != nil && c.ctx.Err() == nil {
		log.Printf("SSH 断开后清理远程协助失败：%v", err)
	}
}

func (c *supportController) cleanupSession(sid, sessionID string, terminate bool) error {
	c.mu.Lock()
	session := c.sessions[sid]
	if session == nil || session.ID != sessionID {
		c.mu.Unlock()
		return nil
	}
	if session.State == supportStateStopping {
		c.mu.Unlock()
		return nil
	}
	session.State = supportStateStopping
	processID := session.ProcessID
	keyPath := session.KeyPath
	c.mu.Unlock()

	if terminate && processID != 0 {
		terminateSupportProcessTree(processID)
	}
	if err := clearSupportKeyContents(keyPath); err != nil && !os.IsNotExist(err) {
		c.mu.Lock()
		if current := c.sessions[sid]; current == session {
			current.State = supportStateCleanupError
		}
		c.mu.Unlock()
		return fmt.Errorf("清除远程协助公钥失败：%w", err)
	}

	c.mu.Lock()
	if current := c.sessions[sid]; current == session {
		delete(c.sessions, sid)
	}
	c.mu.Unlock()
	return nil
}

func terminateSupportProcessTree(processID uint32) {
	if processID == 0 {
		return
	}
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		systemRoot = "C:\\Windows"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(
		ctx,
		filepath.Join(systemRoot, "System32", "taskkill.exe"),
		"/PID",
		strconv.FormatUint(uint64(processID), 10),
		"/T",
		"/F",
	).Run()
}

func supportKeyFilePath(p paths, account string) (string, error) {
	account = strings.TrimSpace(account)
	if strings.ContainsAny(account, "\\/") {
		return "", errors.New("远程协助账户名不能包含路径分隔符")
	}
	account = normalizeSSHLoginAccount(account)
	if !supportAccountPattern.MatchString(account) {
		return "", errors.New("远程协助账户名不适用于受管理的 OpenSSH 密钥路径")
	}
	path := filepath.Join(p.SupportKeysRoot, supportKeysFilePrefix+"-"+strings.ToLower(account))
	relative, err := filepath.Rel(p.SupportKeysRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("远程协助密钥路径超出了受保护目录")
	}
	return path, nil
}

func buildSupportAuthorizedKeyLine(executable, sessionID, authorizedKey string) (string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("已安装的客户端程序不可用")
	}
	if len(sessionID) != 32 {
		return "", errors.New("远程协助会话标识无效")
	}
	command := fmt.Sprintf("\"%s\" support-shell --session %s", filepath.ToSlash(executable), sessionID)
	escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(command)
	options := "no-port-forwarding,no-agent-forwarding,no-X11-forwarding,command=\"" + escaped + "\""
	return options + " " + strings.TrimSpace(authorizedKey), nil
}

func newSupportSessionID() (string, error) {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload); err != nil {
		return "", err
	}
	return hex.EncodeToString(payload), nil
}

func callSupportService(request supportRequest) (supportResponse, error) {
	name, err := windows.UTF16PtrFromString(supportPipeName)
	if err != nil {
		return supportResponse{}, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pipe, openErr := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if openErr == nil {
			file := os.NewFile(uintptr(pipe), "1CatTunnel remote support pipe")
			if file == nil {
				_ = windows.CloseHandle(pipe)
				return supportResponse{}, errors.New("打开远程协助服务通信管道失败")
			}
			defer file.Close()
			payload, err := json.Marshal(request)
			if err != nil {
				return supportResponse{}, err
			}
			if len(payload)+1 > supportPipeMessageBytes {
				return supportResponse{}, errors.New("远程协助请求超过协议大小限制")
			}
			payload = append(payload, '\n')
			if _, err := file.Write(payload); err != nil {
				return supportResponse{}, err
			}
			var response supportResponse
			if err := readSupportJSON(file, &response); err != nil {
				return supportResponse{}, err
			}
			return response, nil
		}
		if !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(openErr, windows.ERROR_PIPE_BUSY) {
			return supportResponse{}, openErr
		}
		if time.Now().After(deadline) {
			return supportResponse{}, errors.New("1CatTunnel 服务尚未准备好处理远程协助请求")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func supportStateDisplay(state string) string {
	switch state {
	case supportStateWaiting:
		return "等待远程协助方连接"
	case supportStateConnected:
		return "远程会话已连接"
	case supportStateStopping:
		return "正在结束"
	case supportStateCleanupError:
		return "清理临时授权失败"
	case "":
		return "未启动"
	default:
		return "未知状态"
	}
}

func runSupportGUI(forceStop bool) error {
	hideSupportConsole()
	account := sshLoginAccount()
	status, err := callSupportService(supportRequest{Action: supportActionStatus, Account: account})
	if err != nil {
		showSupportMessage(supportDialogTitle, "1CatTunnel 后台服务不可用，请先启动或修复客户端。\n\n"+err.Error(), messageBoxOK|messageBoxIconError)
		return nil
	}
	if !status.Available {
		message := "此设备尚未配置免密码远程协助。"
		if status.Error != "" {
			message += "\n\n" + status.Error
		}
		message += "\n\n分发方必须在安装时提供 support-operator.pub。本功能不会读取或使用您的 Windows 密码。"
		showSupportMessage(supportDialogTitle, message, messageBoxOK|messageBoxIconWarning)
		return nil
	}

	if status.SessionID != "" {
		message := fmt.Sprintf(
			"Windows 账户 %s 的远程协助已启用。\n\n状态：%s\n密钥指纹：%s\n\n授权会一直有效，直到远程 SSH 连接断开或您选择立即结束。",
			status.Account,
			supportStateDisplay(status.State),
			status.Fingerprint,
		)
		if forceStop {
			message = "现在结束远程协助吗？当前远程 SSH 连接将立即断开。\n\n" + message
		} else {
			message = "远程协助已经启用，是否现在结束？\n\n" + message
		}
		if showSupportMessage(supportDialogTitle, message, messageBoxYesNo|messageBoxIconWarning) == messageBoxYes {
			response, stopErr := callSupportService(supportRequest{Action: supportActionStop, Account: account, SessionID: status.SessionID})
			if stopErr != nil || (!response.OK && response.Error != "") {
				detail := ""
				if stopErr != nil {
					detail = stopErr.Error()
				} else {
					detail = response.Error
				}
				showSupportMessage(supportDialogTitle, "无法结束远程协助会话。\n\n"+detail, messageBoxOK|messageBoxIconError)
				return nil
			}
			showSupportMessage(supportDialogTitle, "远程协助已结束，临时 SSH 授权已清除。", messageBoxOK|messageBoxIconInfo)
		}
		return nil
	}

	if forceStop {
		showSupportMessage(supportDialogTitle, "此 Windows 账户当前没有正在进行的远程协助会话。", messageBoxOK|messageBoxIconInfo)
		return nil
	}
	message := fmt.Sprintf(
		"允许为 Windows 账户 %s 开启免密码 SSH 远程协助吗？\n\n远程协助方密钥指纹：%s\n\n此操作不会泄露您的 Windows 密码。授权没有自动到期时间，会一直保持，直到您在此结束或已批准的远程 SSH 连接断开。",
		status.Account,
		status.Fingerprint,
	)
	if showSupportMessage(supportDialogTitle, message, messageBoxYesNo|messageBoxIconWarning) != messageBoxYes {
		return nil
	}
	response, startErr := callSupportService(supportRequest{Action: supportActionStart, Account: account})
	if startErr != nil || response.Error != "" {
		detail := ""
		if startErr != nil {
			detail = startErr.Error()
		} else {
			detail = response.Error
		}
		showSupportMessage(supportDialogTitle, "无法启动远程协助。\n\n"+detail, messageBoxOK|messageBoxIconError)
		return nil
	}
	showSupportMessage(
		supportDialogTitle,
		fmt.Sprintf("已为 %s 启用远程协助。\n\n远程协助方现在可以使用其专用 SSH 密钥连接。此授权不会自动到期；您可随时双击“结束远程协助.bat”撤销授权。已批准的 SSH 连接断开后，临时授权会自动清除。", response.Account),
		messageBoxOK|messageBoxIconInfo,
	)
	return nil
}

func runSupportAuthorizedKeys(args []string, output io.Writer) error {
	if len(args) != 1 {
		return errors.New("support-authorized-keys 必须提供且只能提供一个 Windows 账户")
	}
	p, err := systemPaths()
	if err != nil {
		return err
	}
	return emitSupportAuthorizedKey(p, args[0], output)
}

func emitSupportAuthorizedKey(p paths, account string, output io.Writer) error {
	account = normalizeSSHLoginAccount(account)
	if !supportAccountPattern.MatchString(account) {
		return errors.New("support-authorized-keys 收到的 Windows 账户无效")
	}
	keyPath, err := supportKeyFilePath(p, account)
	if err != nil {
		return err
	}
	file, err := os.Open(keyPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开远程协助授权失败：%w", err)
	}
	defer file.Close()

	payload, err := io.ReadAll(io.LimitReader(file, supportPipeMessageBytes+1))
	if err != nil {
		return fmt.Errorf("读取远程协助授权失败：%w", err)
	}
	if len(payload) > supportPipeMessageBytes {
		return errors.New("远程协助授权超过协议大小限制")
	}
	payload = bytesTrimSpace(payload)
	if len(payload) == 0 {
		return nil
	}
	_, _, options, rest, err := ssh.ParseAuthorizedKey(payload)
	if err != nil || len(bytesTrimSpace(rest)) != 0 {
		return errors.New("远程协助授权格式错误")
	}
	required := map[string]bool{
		"no-port-forwarding":  false,
		"no-agent-forwarding": false,
		"no-X11-forwarding":   false,
	}
	hasForcedSession := false
	for _, option := range options {
		if _, ok := required[option]; ok {
			required[option] = true
			continue
		}
		if strings.HasPrefix(option, "command=") && strings.Contains(option, " support-shell --session ") {
			hasForcedSession = true
			continue
		}
		return fmt.Errorf("远程协助授权包含意外选项 %q", option)
	}
	for option, present := range required {
		if !present {
			return fmt.Errorf("远程协助授权缺少 %s", option)
		}
	}
	if !hasForcedSession {
		return errors.New("远程协助授权缺少强制会话限制")
	}
	_, err = fmt.Fprintln(output, string(payload))
	return err
}

func runSupportShell(args []string) error {
	if len(args) != 2 || args[0] != "--session" {
		return errors.New("support-shell 必须提供 --session <会话标识>")
	}
	sessionID := strings.TrimSpace(args[1])
	if len(sessionID) != 32 {
		return errors.New("support-shell 会话标识无效")
	}
	response, err := callSupportService(supportRequest{
		Action:    supportActionConnected,
		SessionID: sessionID,
		ProcessID: uint32(os.Getpid()),
	})
	if err != nil {
		return fmt.Errorf("确认远程协助会话失败：%w", err)
	}
	if !response.OK {
		if response.Error != "" {
			return errors.New(response.Error)
		}
		return errors.New("远程协助会话不可用")
	}
	defer func() {
		_, _ = callSupportService(supportRequest{
			Action:    supportActionFinished,
			SessionID: sessionID,
			ProcessID: uint32(os.Getpid()),
		})
	}()

	originalCommand := strings.TrimSpace(os.Getenv("SSH_ORIGINAL_COMMAND"))
	commandShell := strings.TrimSpace(os.Getenv("ComSpec"))
	if commandShell == "" {
		commandShell = "C:\\Windows\\System32\\cmd.exe"
	}
	var command *exec.Cmd
	if originalCommand == "" {
		command = exec.Command(commandShell, "/D", "/Q")
	} else {
		command = exec.Command(commandShell, "/D", "/S", "/C", originalCommand)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = os.Environ()
	return command.Run()
}

func hideSupportConsole() {
	window, _, _ := procGetConsoleWindow.Call()
	if window != 0 {
		procShowWindow.Call(window, showWindowHide)
	}
}

func showSupportMessage(title, message string, flags uintptr) int {
	titlePointer, _ := windows.UTF16PtrFromString(title)
	messagePointer, _ := windows.UTF16PtrFromString(message)
	result, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(messagePointer)), uintptr(unsafe.Pointer(titlePointer)), flags)
	return int(result)
}
