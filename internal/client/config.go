package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"tunnel/internal/common"
)

const defaultReconnectIntervalSec = 5

type Config struct {
	BootstrapToken       string          `json:"bootstrap_token,omitempty"`
	ServerAddr           string          `json:"server_addr,omitempty"`
	Token                string          `json:"token,omitempty"`
	NodeName             string          `json:"node_name"`
	ReconnectIntervalSec int             `json:"reconnect_interval_sec"`
	Presets              []common.Preset `json:"presets"`
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

	return cfg, nil
}

func BootstrapConfig(path string) (Config, error) {
	cfg, err := readConfigSeed(path)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, err
	}

	cfg = prepareBootstrapConfig(cfg)
	reader := bufio.NewReader(os.Stdin)

	printWizardHeader(path)

	for {
		fmt.Println("步骤 1/4：输入服务器接入 Token")
		tokenNodeName := ""
		cfg.BootstrapToken, cfg.ServerAddr, cfg.Token, tokenNodeName = promptBootstrapToken(reader, cfg.BootstrapToken)
		if tokenNodeName != "" {
			cfg.NodeName = tokenNodeName
			fmt.Printf("已识别节点名称：%s\n", cfg.NodeName)
		} else {
			cfg.NodeName = promptRequired(reader, "节点名称", cfg.NodeName, "控制台里会显示这个名字")
		}

		fmt.Println()
		fmt.Println("步骤 2/4：选择首启预设")
		cfg.Presets = promptPresetSelection(reader, cfg.Presets)

		fmt.Println()
		fmt.Println("步骤 3/4：确认本地端口")
		cfg.Presets = promptPresetAddresses(reader, cfg.Presets)

		fmt.Println()
		fmt.Println("步骤 4/4：确认并保存")
		printConfigSummary(cfg)
		if promptYesNo(reader, "确认保存并立即启动客户端？", true) {
			break
		}

		fmt.Println()
		fmt.Println("好的，我们重新填写一次。")
		fmt.Println()
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := SaveConfig(path, cfg); err != nil {
		return Config{}, err
	}

	fmt.Println()
	fmt.Printf("配置已保存到：%s\n", path)
	fmt.Println("客户端将继续启动并自动重连服务端。")
	fmt.Println()
	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	output := cfg
	if strings.TrimSpace(output.BootstrapToken) != "" {
		output.ServerAddr = ""
		output.Token = ""
	}

	payload, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
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

	return cfg, nil
}

func readConfigSeed(path string) (Config, error) {
	cfg := Config{
		ReconnectIntervalSec: defaultReconnectIntervalSec,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.ServerAddr == "" {
		return fmt.Errorf("server_addr is required")
	}
	if containsPlaceholder(cfg.ServerAddr, "YOUR_SERVER_PUBLIC_IP") {
		return fmt.Errorf("server_addr still contains the example placeholder")
	}
	if cfg.Token == "" {
		return fmt.Errorf("token is required")
	}
	if containsPlaceholder(cfg.Token, "replace-with-a-strong-shared-token") {
		return fmt.Errorf("token still contains the example placeholder")
	}
	if cfg.NodeName == "" {
		return fmt.Errorf("node_name is required")
	}
	if cfg.ReconnectIntervalSec <= 0 {
		return fmt.Errorf("reconnect_interval_sec must be greater than 0")
	}
	if len(cfg.Presets) == 0 {
		return fmt.Errorf("at least one preset is required")
	}

	seen := make(map[string]struct{})
	for _, preset := range cfg.Presets {
		if preset.Name == "" {
			return fmt.Errorf("every preset requires a name")
		}
		if preset.LocalAddr == "" {
			return fmt.Errorf("preset %q requires local_addr", preset.Name)
		}
		if !common.IsSupportedProtocol(preset.Protocol) {
			return fmt.Errorf("preset %q uses unsupported protocol %q", preset.Name, preset.Protocol)
		}
		if _, exists := seen[preset.Name]; exists {
			return fmt.Errorf("preset %q is duplicated", preset.Name)
		}
		seen[preset.Name] = struct{}{}
	}

	return nil
}

func prepareBootstrapConfig(cfg Config) Config {
	if cfg.NodeName == "" {
		hostname, hostnameErr := os.Hostname()
		if hostnameErr == nil && strings.TrimSpace(hostname) != "" {
			cfg.NodeName = hostname
		}
	}

	cfg.BootstrapToken = sanitizePlaceholder(cfg.BootstrapToken, "PASTE_SERVER_CONNECT_TOKEN_HERE")
	cfg.ServerAddr = sanitizePlaceholder(cfg.ServerAddr, "YOUR_SERVER_PUBLIC_IP")
	cfg.Token = sanitizePlaceholder(cfg.Token, "replace-with-a-strong-shared-token")
	cfg.NodeName = strings.TrimSpace(cfg.NodeName)
	cfg.Presets = normalizePresets(cfg.Presets)
	if len(cfg.Presets) == 0 {
		cfg.Presets = defaultPresetsForCurrentOS()
	}
	if cfg.ReconnectIntervalSec <= 0 {
		cfg.ReconnectIntervalSec = defaultReconnectIntervalSec
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
				Description: "一键映射 Windows 远程桌面",
				Protocol:    common.NetworkTCP,
			},
		}
	}

	return []common.Preset{
		{
			Name:        "ssh",
			LocalAddr:   "0.0.0.0:22",
			Description: "一键映射 SSH",
			Protocol:    common.NetworkTCP,
		},
	}
}

func printWizardHeader(path string) {
	fmt.Println("========================================")
	fmt.Println("  1cat Tunnel 客户端首次启动向导")
	fmt.Println("========================================")
	fmt.Println("现在只需要输入服务端给你的接入 Token。")
	fmt.Printf("配置文件位置：%s\n", path)
	fmt.Println()
}

func promptBootstrapToken(reader *bufio.Reader, currentToken string) (string, string, string, string) {
	for {
		token := promptRequired(reader, "接入 Token", currentToken, "直接粘贴服务端控制台里显示的整串 Token")
		payload, err := common.DecodeBootstrapToken(token)
		if err != nil {
			fmt.Println("Token 无法识别，请确认复制完整后再试。")
			continue
		}

		fmt.Printf("已识别服务端地址：%s\n", payload.ServerAddr)
		return token, payload.ServerAddr, payload.AccessToken, payload.NodeName
	}
}

func promptRequired(reader *bufio.Reader, label, currentValue, hint string) string {
	for {
		prompt := label
		if currentValue != "" {
			prompt += " [" + currentValue + "]"
		}
		if hint != "" {
			prompt += "，" + hint
		}
		prompt += "："

		fmt.Print(prompt)
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

		fmt.Printf("%s不能为空，请重新输入。\n", label)
	}
}

func promptPresetSelection(reader *bufio.Reader, existing []common.Preset) []common.Preset {
	options := presetOptionsForWizard(existing)
	defaultChoice := defaultPresetChoice(existing)

	fmt.Println("请选择要暴露给服务端的一键预设：")
	for _, option := range options {
		label := option.Title
		if option.Key == defaultChoice {
			label += "（推荐）"
		}
		fmt.Printf("  %s. %s\n", option.Key, label)
		fmt.Printf("     %s\n", option.Details)
	}

	for {
		fmt.Printf("请输入选项 [%s]：", defaultChoice)
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
				return clonePresets(option.Presets)
			}
		}

		fmt.Println("无效选项，请输入上面的数字。")
	}
}

func promptPresetAddresses(reader *bufio.Reader, presets []common.Preset) []common.Preset {
	out := clonePresets(presets)
	for i := range out {
		label := strings.ToUpper(out[i].Name) + " 本地地址"
		hint := "直接回车使用默认值"
		out[i].LocalAddr = promptRequired(reader, label, out[i].LocalAddr, hint)
	}
	return out
}

func printConfigSummary(cfg Config) {
	fmt.Println("配置摘要：")
	fmt.Printf("  服务端地址：%s\n", cfg.ServerAddr)
	fmt.Printf("  节点名称：%s\n", cfg.NodeName)
	fmt.Printf("  接入 Token：%s\n", maskToken(cfg.BootstrapToken))
	fmt.Println("  预设：")
	for _, preset := range cfg.Presets {
		fmt.Printf("    - %s (%s) -> %s\n", strings.ToUpper(preset.Name), strings.ToUpper(common.NormalizeProtocol(preset.Protocol)), preset.LocalAddr)
	}
}

func promptYesNo(reader *bufio.Reader, label string, defaultYes bool) bool {
	defaultText := "Y/n"
	if !defaultYes {
		defaultText = "y/N"
	}

	for {
		fmt.Printf("%s [%s]：", label, defaultText)
		line, err := reader.ReadString('\n')
		if err != nil {
			line = ""
		}

		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultYes
		}
		if value == "y" || value == "yes" {
			return true
		}
		if value == "n" || value == "no" {
			return false
		}

		fmt.Println("请输入 y 或 n。")
	}
}

func presetOptionsForWizard(existing []common.Preset) []presetOption {
	rdpPreset := common.Preset{
		Name:        "rdp",
		LocalAddr:   existingPresetAddr(existing, "rdp", "127.0.0.1:3389"),
		Description: "一键映射远程桌面",
		Protocol:    common.NetworkTCP,
	}
	sshPreset := common.Preset{
		Name:        "ssh",
		LocalAddr:   existingPresetAddr(existing, "ssh", "0.0.0.0:22"),
		Description: "一键映射 SSH",
		Protocol:    common.NetworkTCP,
	}

	options := []presetOption{
		{
			Key:     "1",
			Title:   "仅开启 RDP",
			Details: fmt.Sprintf("暴露 %s，适合直接远程桌面登录", rdpPreset.LocalAddr),
			Presets: []common.Preset{rdpPreset},
		},
		{
			Key:     "2",
			Title:   "仅开启 SSH",
			Details: fmt.Sprintf("暴露 %s，适合命令行维护或给 Codex / SSH 工具使用", sshPreset.LocalAddr),
			Presets: []common.Preset{sshPreset},
		},
		{
			Key:     "3",
			Title:   "同时开启 RDP + SSH",
			Details: fmt.Sprintf("同时暴露 %s 和 %s", rdpPreset.LocalAddr, sshPreset.LocalAddr),
			Presets: []common.Preset{rdpPreset, sshPreset},
		},
	}

	if len(existing) > 0 && !samePresetSet(existing, options) {
		options = append(options, presetOption{
			Key:     "4",
			Title:   "保留当前已有预设",
			Details: describePresets(existing),
			Presets: clonePresets(existing),
		})
	}

	return options
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
	return strings.Join(parts, "；")
}

func clonePresets(presets []common.Preset) []common.Preset {
	out := make([]common.Preset, len(presets))
	copy(out, presets)
	return out
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		if token == "" {
			return "(未设置)"
		}
		return "****"
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
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
