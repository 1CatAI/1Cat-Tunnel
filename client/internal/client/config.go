package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
	"tunnel/internal/common"
)

const (
	defaultReconnectIntervalSec = 5
	defaultPublicServerAddr     = "dx.1catai.com:50001"
	defaultBlockIPAPIListenAddr = "127.0.0.1:51888"
	defaultClientWebListenAddr  = "127.0.0.1:51888"
)

type Config struct {
	BootstrapToken       string          `json:"bootstrap_token,omitempty"`
	ServerAddr           string          `json:"server_addr,omitempty"`
	Token                string          `json:"token,omitempty"`
	EnrollmentPassword   string          `json:"enrollment_password,omitempty"`
	ProtectedBootstrap   string          `json:"bootstrap_token_dpapi,omitempty"`
	ProtectedToken       string          `json:"token_dpapi,omitempty"`
	ProtectedEnrollment  string          `json:"enrollment_password_dpapi,omitempty"`
	NodeName             string          `json:"node_name"`
	ReconnectIntervalSec int             `json:"reconnect_interval_sec"`
	BlockIPAPIListenAddr string          `json:"block_ip_api_listen_addr,omitempty"`
	WebListenAddr        string          `json:"web_listen_addr,omitempty"`
	TLSEnabled           bool            `json:"tls_enabled"`
	TLSServerName        string          `json:"tls_server_name,omitempty"`
	TLSCAFile            string          `json:"tls_ca_file,omitempty"`
	SelectedServerID     string          `json:"selected_server_id,omitempty"`
	HostedServers        []HostedServer  `json:"hosted_servers,omitempty"`
	Presets              []common.Preset `json:"presets"`

	configPath string
}

type presetOption struct {
	Key     string
	Title   string
	Details string
	Presets []common.Preset
}

func LoadConfig(path string) (Config, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return Config{}, err
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	cfg.configPath = path

	return cfg, nil
}

// LoadManagedConfig allows the loopback management panel to start before the
// enrollment credential has been configured. Connection attempts still use
// the same strict validation as the regular client path.
func LoadManagedConfig(path string) (Config, error) {
	cfg, err := readConfig(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		cfg = prepareBootstrapConfig(Config{
			ReconnectIntervalSec: defaultReconnectIntervalSec,
			TLSEnabled:           true,
		})
	}
	cfg.configPath = path
	return cfg, nil
}

func ValidateConfig(cfg Config) error {
	return validateConfig(cfg)
}

func BootstrapConfig(path string) (Config, error) {
	cfg, err := readConfigSeed(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		cfg = Config{
			ReconnectIntervalSec: defaultReconnectIntervalSec,
			TLSEnabled:           true,
		}
	}

	cfg = prepareBootstrapConfig(cfg)
	reader := bufio.NewReader(os.Stdin)

	printWizardHeader(path)

	for {
		fmt.Println(userText("步骤 1/4：输入服务器接入凭据", "Step 1/4: enter the server credential"))
		tokenNodeName := ""
		var promptErr error
		cfg.BootstrapToken, cfg.ServerAddr, cfg.Token, tokenNodeName, cfg.EnrollmentPassword, promptErr = promptAccessCredential(reader, cfg)
		if promptErr != nil {
			return Config{}, fmt.Errorf(userText("读取服务器接入凭据失败：%w", "read server credential: %w"), promptErr)
		}
		if tokenNodeName != "" {
			cfg.NodeName = tokenNodeName
			fmt.Printf(userText("已识别节点名称：%s\n", "Detected node name: %s\n"), cfg.NodeName)
		} else {
			cfg.NodeName = promptRequired(
				reader,
				userText("节点名称", "Node name"),
				cfg.NodeName,
				userText("将显示在服务端管理页面中", "shown in the server console"),
			)
		}

		fmt.Println()
		fmt.Println(userText("步骤 2/4：选择需要映射的服务", "Step 2/4: choose presets"))
		cfg.Presets = promptPresetSelection(reader, cfg.Presets)

		fmt.Println()
		fmt.Println(userText("步骤 3/4：确认本地地址", "Step 3/4: confirm local addresses"))
		cfg.Presets = promptPresetAddresses(reader, cfg.Presets)

		fmt.Println()
		fmt.Println(userText("步骤 4/4：检查并保存配置", "Step 4/4: review and save"))
		printConfigSummary(cfg)
		if promptYesNo(reader, userText("保存此配置并立即启动客户端吗？", "Save this config and start the client now?"), true) {
			break
		}

		fmt.Println()
		fmt.Println(userText("好的，我们重新填写一次。", "No problem. Let's fill it in again."))
		fmt.Println()
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := SaveConfig(path, cfg); err != nil {
		return Config{}, err
	}
	cfg.configPath = path

	fmt.Println()
	fmt.Printf(userText("配置已保存到：%s\n", "Config saved to: %s\n"), path)
	fmt.Println(userText("客户端将继续启动，并在网络中断后自动重连。", "The client will continue starting and will reconnect automatically."))
	fmt.Println()
	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	output := cfg
	if strings.TrimSpace(output.BootstrapToken) != "" {
		output.ServerAddr = ""
		output.Token = ""
		output.EnrollmentPassword = ""
	}
	if strings.TrimSpace(output.EnrollmentPassword) != "" && output.Token == output.EnrollmentPassword {
		output.Token = ""
	}

	output, err := protectConfigSecretsForStorage(output)
	if err != nil {
		return fmt.Errorf(userText("保护客户端凭据失败：%w", "protect client credentials: %w"), err)
	}

	payload, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeSecureConfig(path, payload)
}

func writeSecureConfig(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if err := validatePrivatePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, ".1cat-config-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := restrictPrivateFile(temp); err != nil {
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

	if err := replaceFileAtomic(tempPath, path); err != nil {
		return err
	}
	return nil
}

func readConfig(path string) (Config, error) {
	cfg, err := readConfigSeed(path)
	if err != nil {
		return Config{}, err
	}

	cfg = prepareBootstrapConfig(cfg)
	resolved, err := resolveBootstrapToken(cfg.BootstrapToken)
	if err != nil {
		return Config{}, err
	}
	if resolved.ServerAddr != "" {
		cfg.ServerAddr = resolved.ServerAddr
	}
	if resolved.NodeName != "" {
		cfg.NodeName = resolved.NodeName
	}
	if resolved.AccessToken != "" {
		cfg.Token = resolved.AccessToken
	}
	if strings.TrimSpace(cfg.Token) == "" && strings.TrimSpace(cfg.EnrollmentPassword) != "" {
		cfg.Token = strings.TrimSpace(cfg.EnrollmentPassword)
	}

	return cfg, nil
}

func readConfigSeed(path string) (Config, error) {
	cfg := Config{
		ReconnectIntervalSec: defaultReconnectIntervalSec,
		TLSEnabled:           true,
	}

	data, err := readPrivateFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg, err = restoreConfigSecretsFromStorage(cfg)
	if err != nil {
		return Config{}, fmt.Errorf(userText("恢复受保护的客户端凭据失败：%w", "restore protected client credentials: %w"), err)
	}

	return cfg, nil
}

func validateConfig(cfg Config) error {
	if err := validateClientWebListenAddr(cfg.WebListenAddr); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ServerAddr) == "" {
		return errors.New(userText("必须配置 server_addr（服务器地址）", "server_addr is required"))
	}
	if containsPlaceholder(cfg.ServerAddr, "YOUR_SERVER_PUBLIC_IP") {
		return errors.New(userText("server_addr 仍包含示例占位值", "server_addr still contains the example placeholder"))
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New(userText("必须提供 token 或 enrollment_password（接入密码）", "token or enrollment_password is required"))
	}
	if containsPlaceholder(cfg.Token, "replace-with-a-strong-shared-token") {
		return errors.New(userText("token 仍包含示例占位值", "token still contains the example placeholder"))
	}
	if strings.TrimSpace(cfg.NodeName) == "" {
		return errors.New(userText("必须配置 node_name（节点名称）", "node_name is required"))
	}
	if cfg.ReconnectIntervalSec <= 0 {
		return errors.New(userText("reconnect_interval_sec（重连间隔）必须大于 0", "reconnect_interval_sec must be greater than 0"))
	}
	if len(cfg.Presets) == 0 {
		return errors.New(userText("至少需要配置一个映射预设", "at least one preset is required"))
	}

	seen := make(map[string]struct{})
	for _, preset := range cfg.Presets {
		if strings.TrimSpace(preset.Name) == "" {
			return errors.New(userText("每个映射预设都必须填写名称", "every preset requires a name"))
		}
		if strings.TrimSpace(preset.LocalAddr) == "" {
			return fmt.Errorf(userText("映射预设 %q 必须配置 local_addr（本地地址）", "preset %q requires local_addr"), preset.Name)
		}
		if !common.IsSupportedProtocol(preset.Protocol) {
			return fmt.Errorf(userText("映射预设 %q 使用了不支持的协议 %q", "preset %q uses unsupported protocol %q"), preset.Name, preset.Protocol)
		}
		name := strings.ToLower(strings.TrimSpace(preset.Name))
		if _, exists := seen[name]; exists {
			return fmt.Errorf(userText("映射预设 %q 重复", "preset %q is duplicated"), preset.Name)
		}
		seen[name] = struct{}{}
	}

	return nil
}

func prepareBootstrapConfig(cfg Config) Config {
	if strings.TrimSpace(cfg.NodeName) == "" {
		hostname, hostnameErr := os.Hostname()
		if hostnameErr == nil && strings.TrimSpace(hostname) != "" {
			cfg.NodeName = hostname
		}
	}

	cfg.BootstrapToken = sanitizePlaceholder(cfg.BootstrapToken, "PASTE_SERVER_CONNECT_TOKEN_HERE")
	cfg.ServerAddr = sanitizePlaceholder(cfg.ServerAddr, "YOUR_SERVER_PUBLIC_IP")
	cfg.Token = sanitizePlaceholder(cfg.Token, "replace-with-a-strong-shared-token")
	cfg.EnrollmentPassword = strings.TrimSpace(cfg.EnrollmentPassword)
	cfg.NodeName = strings.TrimSpace(cfg.NodeName)
	cfg.Presets = normalizePresets(cfg.Presets)
	if len(cfg.Presets) == 0 {
		cfg.Presets = defaultPresetsForCurrentOS()
	}
	if cfg.ReconnectIntervalSec <= 0 {
		cfg.ReconnectIntervalSec = defaultReconnectIntervalSec
	}
	cfg.BlockIPAPIListenAddr = strings.TrimSpace(cfg.BlockIPAPIListenAddr)
	cfg.WebListenAddr = strings.TrimSpace(cfg.WebListenAddr)
	if cfg.WebListenAddr == "" {
		if cfg.BlockIPAPIListenAddr != "" {
			cfg.WebListenAddr = cfg.BlockIPAPIListenAddr
		} else {
			cfg.WebListenAddr = defaultClientWebListenAddr
		}
	}
	cfg.TLSServerName = strings.TrimSpace(cfg.TLSServerName)
	cfg.TLSCAFile = strings.TrimSpace(cfg.TLSCAFile)
	cfg.SelectedServerID = strings.TrimSpace(cfg.SelectedServerID)
	cfg.HostedServers = normalizeHostedServers(cfg.HostedServers)
	if cfg.SelectedServerID == "" {
		cfg.SelectedServerID = selectedHostedServerID(cfg)
	}
	if runtime.GOOS == "linux" && cfg.BlockIPAPIListenAddr == "" {
		cfg.BlockIPAPIListenAddr = cfg.WebListenAddr
	}
	return cfg
}

func resolveBootstrapToken(token string) (common.BootstrapTokenPayload, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return common.BootstrapTokenPayload{}, nil
	}
	return common.DecodeBootstrapToken(token)
}

func normalizePresets(presets []common.Preset) []common.Preset {
	out := make([]common.Preset, 0, len(presets))
	for _, preset := range presets {
		preset.Name = strings.TrimSpace(preset.Name)
		preset.LocalAddr = strings.TrimSpace(preset.LocalAddr)
		preset.Description = strings.TrimSpace(preset.Description)
		preset.Protocol = common.NormalizeProtocol(preset.Protocol)
		if preset.Name == "" || preset.LocalAddr == "" {
			continue
		}
		out = append(out, preset)
	}
	return out
}

func defaultPresetsForCurrentOS() []common.Preset {
	if runtime.GOOS == "windows" {
		return []common.Preset{
			{
				Name:        "rdp",
				LocalAddr:   "127.0.0.1:3389",
				Description: userText("映射 Windows 远程桌面", "Expose Windows Remote Desktop"),
				Protocol:    common.NetworkTCP,
			},
		}
	}

	return []common.Preset{
		{
			Name:        "ssh",
			LocalAddr:   "0.0.0.0:22",
			Description: userText("映射 SSH", "Expose SSH"),
			Protocol:    common.NetworkTCP,
		},
	}
}

func printWizardHeader(path string) {
	fmt.Println("========================================")
	fmt.Println(userText("  1CatTunnel 客户端首次运行向导", "  1cat Tunnel Client First-run Wizard"))
	fmt.Println("========================================")
	fmt.Println(userText("请粘贴 WebUI 连接 Token，或输入管理员提供的接入密码。", "Paste a WebUI connect token, or enter the enrollment password."))
	fmt.Printf(userText("配置文件：%s\n", "Config file: %s\n"), path)
	fmt.Println()
}

func promptAccessCredential(reader *bufio.Reader, cfg Config) (string, string, string, string, string, error) {
	current := firstNonEmpty(cfg.BootstrapToken, cfg.EnrollmentPassword, cfg.Token)
	credential, err := promptSecretRequired(reader, userText("连接 Token 或接入密码", "Access token or enrollment password"), current, userText("WebUI Token，或管理员提供的接入密码", "WebUI token, or the enrollment password from your administrator"))
	if err != nil {
		return "", "", "", "", "", err
	}
	credential = strings.TrimSpace(credential)

	payload, err := common.DecodeBootstrapToken(credential)
	if err == nil {
		fmt.Printf(userText("已识别服务器地址：%s\n", "Detected server address: %s\n"), payload.ServerAddr)
		return credential, payload.ServerAddr, payload.AccessToken, payload.NodeName, "", nil
	}

	serverAddr := promptRequired(reader, userText("服务器地址", "Server address"), firstNonEmpty(cfg.ServerAddr, defaultPublicServerAddr), userText("格式为 主机:端口，默认 dx.1catai.com:50001", "host:port, default dx.1catai.com:50001"))
	fmt.Printf(userText("已为 %s 启用自动注册模式\n", "Auto-enrollment mode enabled for %s\n"), serverAddr)
	return "", serverAddr, credential, "", credential, nil
}

func promptRequired(reader *bufio.Reader, label, currentValue, hint string) string {
	for {
		fmt.Print(formatPrompt(label, currentValue, hint, false))
		line, err := reader.ReadString('\n')
		if err != nil {
			line = ""
		}

		value := strings.TrimSpace(line)
		if value == "" {
			value = currentValue
		}
		if value != "" {
			return value
		}

		fmt.Printf(userText("%s不能为空，请重新输入。\n", "%s cannot be empty. Please try again.\n"), label)
	}
}

func promptSecretRequired(reader *bufio.Reader, label, currentValue, hint string) (string, error) {
	for {
		fmt.Print(formatPrompt(label, currentValue, hint, true))
		line, err := readSecretLine(reader)
		if err != nil {
			return "", err
		}

		value := strings.TrimSpace(line)
		if value == "" {
			value = currentValue
		}
		if value != "" {
			return value, nil
		}

		fmt.Printf(userText("%s不能为空，请重新输入。\n", "%s cannot be empty. Please try again.\n"), label)
	}
}

func formatPrompt(label, currentValue, hint string, secret bool) string {
	prompt := label
	if currentValue != "" {
		if secret {
			prompt += userText(" [已保存；直接按回车保留]", " [saved; press Enter to keep]")
		} else {
			prompt += " [" + currentValue + "]"
		}
	}
	if hint != "" {
		prompt += " - " + hint
	}
	return prompt + ": "
}

func readSecretLine(reader *bufio.Reader) (string, error) {
	stdinFD := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFD) {
		line, err := reader.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return "", err
		}
		return line, nil
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		value, err := term.ReadPassword(stdinFD)
		return string(value), err
	}

	state, err := term.MakeRaw(stdinFD)
	if err != nil {
		return "", err
	}
	value, readErr := readMaskedValue(os.Stdin, os.Stdout)
	restoreErr := term.Restore(stdinFD, state)
	fmt.Fprintln(os.Stdout)
	if readErr != nil {
		return "", readErr
	}
	if restoreErr != nil {
		return "", restoreErr
	}
	return value, nil
}

func readMaskedValue(input io.Reader, output io.Writer) (string, error) {
	value := make([]byte, 0, 64)
	buffer := make([]byte, 1)

	for {
		n, err := input.Read(buffer)
		if n > 0 {
			switch key := buffer[0]; key {
			case '\r', '\n':
				return string(value), nil
			case 3:
				return "", errors.New(userText("凭据输入已中断", "credential input interrupted"))
			case 4, 26:
				if len(value) == 0 {
					return "", io.EOF
				}
				return string(value), nil
			case '\b', 127:
				if len(value) == 0 {
					continue
				}
				_, size := utf8.DecodeLastRune(value)
				value = value[:len(value)-size]
				if _, writeErr := io.WriteString(output, strings.Repeat("\b \b", size)); writeErr != nil {
					return "", writeErr
				}
			default:
				if key < ' ' {
					continue
				}
				value = append(value, key)
				if _, writeErr := io.WriteString(output, "*"); writeErr != nil {
					return "", writeErr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(value) > 0 {
				return string(value), nil
			}
			return "", err
		}
	}
}

func promptPresetSelection(reader *bufio.Reader, existing []common.Preset) []common.Preset {
	options := presetOptionsForWizard(existing)
	defaultChoice := defaultPresetChoice(existing)

	fmt.Println(userText("请选择需要通过服务器映射的服务：", "Choose the presets to expose through the server:"))
	for _, option := range options {
		label := option.Title
		if option.Key == defaultChoice {
			label += userText("（推荐）", " (recommended)")
		}
		fmt.Printf("  %s. %s\n", option.Key, label)
		fmt.Printf("     %s\n", option.Details)
	}

	for {
		fmt.Printf(userText("请输入选项 [%s]：", "Enter choice [%s]: "), defaultChoice)
		line, err := reader.ReadString('\n')
		if err != nil {
			line = ""
		}

		choice := strings.TrimSpace(line)
		if choice == "" {
			choice = defaultChoice
		}

		for _, option := range options {
			if choice == option.Key {
				if option.Presets == nil {
					return promptCustomPresets(reader, existing)
				}
				return clonePresets(option.Presets)
			}
		}

		fmt.Println(userText("选项无效，请输入上方列出的数字。", "Invalid choice. Please enter one of the numbers above."))
	}
}

func promptPresetAddresses(reader *bufio.Reader, presets []common.Preset) []common.Preset {
	out := clonePresets(presets)
	for i := range out {
		label := strings.ToUpper(out[i].Name) + userText(" 本地地址", " local address")
		out[i].LocalAddr = promptRequired(reader, label, out[i].LocalAddr, userText("直接按回车保留默认值", "press Enter to keep the default"))
	}
	return out
}

func promptCustomPresets(reader *bufio.Reader, existing []common.Preset) []common.Preset {
	count := promptInt(reader, userText("需要映射多少个本地端口", "How many local ports to expose"), defaultCustomPresetCount(existing), 1, 20)
	out := make([]common.Preset, 0, count)

	for i := 0; i < count; i++ {
		var current common.Preset
		if i < len(existing) {
			current = existing[i]
		}

		existingHost, existingPort := splitLocalAddress(current.LocalAddr)
		protocol := promptProtocol(reader, fmt.Sprintf(userText("端口 %d 的协议", "Port %d protocol"), i+1), firstNonEmpty(current.Protocol, common.NetworkTCP))
		host := promptRequired(reader, fmt.Sprintf(userText("端口 %d 的本地主机/IP", "Port %d local host/IP"), i+1), firstNonEmpty(existingHost, defaultLocalHostForCurrentOS()), userText("Windows 本机服务通常使用 127.0.0.1", "for Windows local services usually 127.0.0.1"))
		port := promptPort(reader, fmt.Sprintf(userText("端口 %d 的本地端口号", "Port %d local port"), i+1), firstNonEmpty(existingPort, defaultPortForProtocol(protocol)))
		name := promptRequired(reader, fmt.Sprintf(userText("端口 %d 的映射名称", "Port %d preset name"), i+1), firstNonEmpty(current.Name, defaultPresetName(protocol, port, i+1)), userText("将显示在服务端管理页面中", "shown in the server console"))
		description := promptOptional(reader, fmt.Sprintf(userText("端口 %d 的说明", "Port %d description"), i+1), current.Description, userText("可选", "optional"))

		out = append(out, common.Preset{
			Name:        name,
			LocalAddr:   net.JoinHostPort(host, port),
			Description: description,
			Protocol:    protocol,
		})
	}

	return out
}

func promptInt(reader *bufio.Reader, label string, current, min, max int) int {
	if current < min {
		current = min
	}
	if current > max {
		current = max
	}

	for {
		value := promptRequired(reader, label, strconv.Itoa(current), fmt.Sprintf("%d-%d", min, max))
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && number >= min && number <= max {
			return number
		}
		fmt.Printf(userText("%s必须是 %d 到 %d 之间的数字。\n", "%s must be a number between %d and %d.\n"), label, min, max)
	}
}

func promptPort(reader *bufio.Reader, label, current string) string {
	for {
		value := promptRequired(reader, label, current, "1-65535")
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && port >= 1 && port <= 65535 {
			return strconv.Itoa(port)
		}
		fmt.Println(userText("端口号必须是 1 到 65535 之间的数字。", "Port must be a number between 1 and 65535."))
	}
}

func promptProtocol(reader *bufio.Reader, label, current string) string {
	current = common.NormalizeProtocol(current)
	for {
		value := strings.ToLower(promptRequired(reader, label, current, userText("输入 tcp 或 udp", "tcp or udp")))
		if common.IsSupportedProtocol(value) {
			return common.NormalizeProtocol(value)
		}
		fmt.Println(userText("协议必须是 tcp 或 udp。", "Protocol must be tcp or udp."))
	}
}

func promptOptional(reader *bufio.Reader, label, currentValue, hint string) string {
	prompt := label
	if currentValue != "" {
		prompt += " [" + currentValue + "]"
	}
	if hint != "" {
		prompt += " - " + hint
	}
	prompt += ": "

	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		line = ""
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return strings.TrimSpace(currentValue)
	}
	return value
}

func printConfigSummary(cfg Config) {
	credential := firstNonEmpty(cfg.BootstrapToken, cfg.EnrollmentPassword, cfg.Token)
	fmt.Println(userText("配置摘要：", "Config summary:"))
	fmt.Printf(userText("  服务器：%s\n", "  Server: %s\n"), cfg.ServerAddr)
	fmt.Printf(userText("  节点：%s\n", "  Node: %s\n"), cfg.NodeName)
	fmt.Printf(userText("  接入凭据：%s\n", "  Credential: %s\n"), maskToken(credential))
	if !blockIPAPIDisabled(cfg.BlockIPAPIListenAddr) {
		fmt.Printf(userText("  本地 IP 屏蔽 API：http://%s/api/block-ip\n", "  Local block-ip API: http://%s/api/block-ip\n"), cfg.BlockIPAPIListenAddr)
	}
	fmt.Println(userText("  映射预设：", "  Presets:"))
	for _, preset := range cfg.Presets {
		fmt.Printf("    - %s (%s) -> %s\n", strings.ToUpper(preset.Name), strings.ToUpper(common.NormalizeProtocol(preset.Protocol)), preset.LocalAddr)
	}
}

func promptYesNo(reader *bufio.Reader, label string, defaultYes bool) bool {
	defaultText := userText("是/否，默认是", "Y/n")
	if !defaultYes {
		defaultText = userText("是/否，默认否", "y/N")
	}

	for {
		fmt.Printf("%s [%s]: ", label, defaultText)
		line, err := reader.ReadString('\n')
		if err != nil {
			line = ""
		}

		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultYes
		}
		if value == "y" || value == "yes" || value == "是" {
			return true
		}
		if value == "n" || value == "no" || value == "否" {
			return false
		}

		fmt.Println(userText("请输入 y/是 或 n/否。", "Please enter y or n."))
	}
}

func presetOptionsForWizard(existing []common.Preset) []presetOption {
	rdpPreset := common.Preset{
		Name:        "rdp",
		LocalAddr:   existingPresetAddr(existing, "rdp", "127.0.0.1:3389"),
		Description: userText("映射远程桌面", "Expose Remote Desktop"),
		Protocol:    common.NetworkTCP,
	}
	sshDefaultAddr := "0.0.0.0:22"
	if runtime.GOOS == "windows" {
		sshDefaultAddr = "127.0.0.1:22"
	}
	sshPreset := common.Preset{
		Name:        "ssh",
		LocalAddr:   existingPresetAddr(existing, "ssh", sshDefaultAddr),
		Description: userText("映射 SSH", "Expose SSH"),
		Protocol:    common.NetworkTCP,
	}

	options := []presetOption{
		{
			Key:     "1",
			Title:   userText("仅 RDP 远程桌面", "RDP only"),
			Details: fmt.Sprintf(userText("映射 %s，用于远程桌面登录", "Expose %s for Remote Desktop login"), rdpPreset.LocalAddr),
			Presets: []common.Preset{rdpPreset},
		},
		{
			Key:     "2",
			Title:   userText("仅 SSH", "SSH only"),
			Details: fmt.Sprintf(userText("映射 %s，用于命令行或 Codex 访问", "Expose %s for shell/Codex access"), sshPreset.LocalAddr),
			Presets: []common.Preset{sshPreset},
		},
		{
			Key:     "3",
			Title:   userText("RDP 远程桌面 + SSH", "RDP + SSH"),
			Details: fmt.Sprintf(userText("同时映射 %s 和 %s", "Expose both %s and %s"), rdpPreset.LocalAddr, sshPreset.LocalAddr),
			Presets: []common.Preset{rdpPreset, sshPreset},
		},
		{
			Key:     "4",
			Title:   userText("手动自定义端口", "Manual custom ports"),
			Details: userText("手动选择一个或多个 TCP/UDP 本地端口", "Choose one or more TCP/UDP local ports manually"),
			Presets: nil,
		},
	}

	if len(existing) > 0 && !samePresetSet(existing, options) {
		options = append(options, presetOption{
			Key:     "5",
			Title:   userText("保留当前映射预设", "Keep current presets"),
			Details: describePresets(existing),
			Presets: clonePresets(existing),
		})
	}

	return options
}

func defaultCustomPresetCount(existing []common.Preset) int {
	if len(existing) > 0 {
		return len(existing)
	}
	return 1
}

func defaultLocalHostForCurrentOS() string {
	if runtime.GOOS == "windows" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

func defaultPortForProtocol(protocol string) string {
	switch common.NormalizeProtocol(protocol) {
	case common.NetworkUDP:
		return "53"
	default:
		return "3389"
	}
}

func defaultPresetName(protocol, port string, index int) string {
	port = strings.TrimSpace(port)
	switch port {
	case "3389":
		return "rdp"
	case "22":
		return "ssh"
	case "80", "8080":
		return "web" + port
	case "443":
		return "https443"
	}
	return fmt.Sprintf("%s%s_%d", common.NormalizeProtocol(protocol), port, index)
}

func splitLocalAddress(localAddr string) (string, string) {
	localAddr = strings.TrimSpace(localAddr)
	if localAddr == "" {
		return "", ""
	}

	host, port, err := net.SplitHostPort(localAddr)
	if err == nil {
		return strings.Trim(host, "[]"), port
	}

	lastColon := strings.LastIndex(localAddr, ":")
	if lastColon > 0 && lastColon < len(localAddr)-1 {
		return localAddr[:lastColon], localAddr[lastColon+1:]
	}

	return localAddr, ""
}

func defaultPresetChoice(existing []common.Preset) string {
	hasRDP := hasPreset(existing, "rdp")
	hasSSH := hasPreset(existing, "ssh")

	switch {
	case hasRDP && hasSSH:
		return "3"
	case runtime.GOOS == "windows" && hasRDP:
		return "1"
	case runtime.GOOS == "windows" && hasSSH:
		return "2"
	case runtime.GOOS != "windows" && hasSSH:
		return "2"
	case runtime.GOOS != "windows" && hasRDP:
		return "1"
	case runtime.GOOS == "windows":
		return "1"
	default:
		return "2"
	}
}

func hasPreset(presets []common.Preset, name string) bool {
	for _, preset := range presets {
		if strings.EqualFold(preset.Name, name) {
			return true
		}
	}
	return false
}

func existingPresetAddr(presets []common.Preset, name, fallback string) string {
	for _, preset := range presets {
		if strings.EqualFold(preset.Name, name) && strings.TrimSpace(preset.LocalAddr) != "" {
			return strings.TrimSpace(preset.LocalAddr)
		}
	}
	return fallback
}

func samePresetSet(existing []common.Preset, options []presetOption) bool {
	for _, option := range options {
		if presetsEqual(existing, option.Presets) {
			return true
		}
	}
	return false
}

func presetsEqual(a, b []common.Preset) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !strings.EqualFold(a[i].Name, b[i].Name) {
			return false
		}
		if strings.TrimSpace(a[i].LocalAddr) != strings.TrimSpace(b[i].LocalAddr) {
			return false
		}
		if common.NormalizeProtocol(a[i].Protocol) != common.NormalizeProtocol(b[i].Protocol) {
			return false
		}
	}
	return true
}

func describePresets(presets []common.Preset) string {
	parts := make([]string, 0, len(presets))
	for _, preset := range presets {
		parts = append(parts, fmt.Sprintf("%s (%s) -> %s", strings.ToUpper(preset.Name), strings.ToUpper(common.NormalizeProtocol(preset.Protocol)), preset.LocalAddr))
	}
	return strings.Join(parts, "; ")
}

func clonePresets(presets []common.Preset) []common.Preset {
	out := make([]common.Preset, len(presets))
	copy(out, presets)
	return out
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return userText("（未设置）", "(not set)")
	}
	return "********"
}

func sanitizePlaceholder(value, marker string) string {
	if containsPlaceholder(value, marker) {
		return ""
	}
	return strings.TrimSpace(value)
}

func containsPlaceholder(value, marker string) bool {
	return strings.Contains(strings.ToUpper(value), strings.ToUpper(marker))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
