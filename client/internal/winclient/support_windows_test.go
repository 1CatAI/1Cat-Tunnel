//go:build windows

package winclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

const testRemoteSupportPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKZyT7WF+SRUjas5jUCxjC8GpkBh3u06M78xctsh5Ju6 test@1cattunnel"

func TestParseSupportOperatorKeyNormalizesSingleKey(t *testing.T) {
	operator, err := parseSupportOperatorKey([]byte(testRemoteSupportPublicKey + "\n"))
	if err != nil {
		t.Fatalf("parseSupportOperatorKey: %v", err)
	}
	if operator.fingerprint == "" {
		t.Fatal("operator fingerprint is empty")
	}
	if !strings.HasPrefix(operator.authorizedLine, "ssh-ed25519 ") {
		t.Fatalf("unexpected normalized key: %q", operator.authorizedLine)
	}
}

func TestParseSupportOperatorKeyRejectsMultipleKeys(t *testing.T) {
	_, err := parseSupportOperatorKey([]byte(testRemoteSupportPublicKey + "\n" + testRemoteSupportPublicKey + "\n"))
	if err == nil {
		t.Fatal("expected multiple public keys to be rejected")
	}
}

func TestSupportKeyFilePathIsScopedToServiceData(t *testing.T) {
	p := paths{SupportKeysRoot: "C:\\ProgramData\\1CatTunnel\\support-keys"}
	path, err := supportKeyFilePath(p, "remote.user@example.com")
	if err != nil {
		t.Fatalf("supportKeyFilePath: %v", err)
	}
	if !strings.Contains(strings.ToLower(path), "1cattunnel-support-remote.user@example.com") {
		t.Fatalf("unexpected key path %q", path)
	}
	for _, account := range []string{"..", "../admin", "domain\\admin", "bad name"} {
		if _, err := supportKeyFilePath(p, account); err == nil {
			t.Fatalf("expected unsafe account %q to be rejected", account)
		}
	}
}

func TestBuildSupportAuthorizedKeyLineUsesForcedSessionWrapper(t *testing.T) {
	sessionID := "0123456789abcdef0123456789abcdef"
	line, err := buildSupportAuthorizedKeyLine("C:\\Program Files\\1CatTunnel\\1cattunnel.exe", sessionID, testRemoteSupportPublicKey)
	if err != nil {
		t.Fatalf("buildSupportAuthorizedKeyLine: %v", err)
	}
	for _, expected := range []string{
		"no-port-forwarding",
		"no-agent-forwarding",
		"no-X11-forwarding",
		"support-shell --session " + sessionID,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("authorized key line is missing %q: %s", expected, line)
		}
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line + "\n")); err != nil {
		t.Fatalf("generated authorized key is not parseable: %v\n%s", err, line)
	}
}

func TestBuildManagedSSHDConfigUsesAuthorizedKeysCommand(t *testing.T) {
	input := "Match Group administrators\n    AuthorizedKeysFile " + supportKeyConfigPath + " __PROGRAMDATA__/ssh/administrators_authorized_keys\n"
	output, err := buildManagedSSHDConfigForExecutable(input, "C:\\Program Files\\1CatTunnel\\1cattunnel.exe")
	if err != nil {
		t.Fatalf("buildManagedSSHDConfigForExecutable: %v", err)
	}
	for _, expected := range []string{
		"AuthorizedKeysCommand \"C:/Program Files/1CatTunnel/1cattunnel.exe\" support-authorized-keys %u",
		"AuthorizedKeysCommandUser SYSTEM",
		"AuthorizedKeysFile .ssh/authorized_keys",
		"AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("managed config is missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, supportKeyConfigPath) {
		t.Fatalf("legacy service-writable key path remains configured:\n%s", output)
	}
}

func TestBuildManagedSSHDConfigPreservesCustomKeyPath(t *testing.T) {
	input := "AuthorizedKeysFile C:/custom/authorized_keys\nMatch Group administrators\n    AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys\n"
	output, err := buildManagedSSHDConfigForExecutable(input, "C:\\Program Files\\1CatTunnel\\1cattunnel.exe")
	if err != nil {
		t.Fatalf("buildManagedSSHDConfigForExecutable: %v", err)
	}
	if !strings.Contains(output, "AuthorizedKeysFile C:/custom/authorized_keys") {
		t.Fatalf("custom authorized key path was not preserved:\n%s", output)
	}
	if strings.Contains(output, supportKeyConfigPath) {
		t.Fatalf("legacy support key path was not removed:\n%s", output)
	}
}

func TestEmitSupportAuthorizedKeyReturnsOnlyActiveForcedKey(t *testing.T) {
	p := paths{SupportKeysRoot: t.TempDir()}
	path, err := supportKeyFilePath(p, "tester")
	if err != nil {
		t.Fatalf("supportKeyFilePath: %v", err)
	}
	line, err := buildSupportAuthorizedKeyLine("C:\\Program Files\\1CatTunnel\\1cattunnel.exe", "0123456789abcdef0123456789abcdef", testRemoteSupportPublicKey)
	if err != nil {
		t.Fatalf("buildSupportAuthorizedKeyLine: %v", err)
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write active support key: %v", err)
	}
	var output strings.Builder
	if err := emitSupportAuthorizedKey(p, "tester", &output); err != nil {
		t.Fatalf("emitSupportAuthorizedKey: %v", err)
	}
	if strings.TrimSpace(output.String()) != line {
		t.Fatalf("emitted authorization = %q, want %q", output.String(), line)
	}

	if err := clearSupportKeyContents(path); err != nil {
		t.Fatalf("clearSupportKeyContents: %v", err)
	}
	output.Reset()
	if err := emitSupportAuthorizedKey(p, "tester", &output); err != nil {
		t.Fatalf("emit cleared support key: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cleared authorization was emitted: %q", output.String())
	}
}

func TestEmitSupportAuthorizedKeyRejectsUnrestrictedKey(t *testing.T) {
	p := paths{SupportKeysRoot: t.TempDir()}
	path, err := supportKeyFilePath(p, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(testRemoteSupportPublicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emitSupportAuthorizedKey(p, "tester", &strings.Builder{}); err == nil {
		t.Fatal("expected an unrestricted support key to be rejected")
	}
}

func TestReadAuthenticatedSupportRequestReadsBeforeImpersonating(t *testing.T) {
	pipeName := fmt.Sprintf("\\\\.\\pipe\\1CatTunnelRemoteSupport-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	pipe, err := createSupportPipeNamed(pipeName)
	if err != nil {
		t.Fatalf("createSupportPipeNamed: %v", err)
	}
	serverFile := os.NewFile(uintptr(pipe), "1CatTunnel test support server")
	if serverFile == nil {
		windows.CloseHandle(pipe)
		t.Fatal("open test support server file")
	}
	defer serverFile.Close()

	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
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
		t.Fatalf("open test pipe: %v", err)
	}
	clientFile := os.NewFile(uintptr(client), "1CatTunnel test support client")
	if clientFile == nil {
		windows.CloseHandle(client)
		t.Fatal("open test support client file")
	}
	defer clientFile.Close()

	if err := windows.ConnectNamedPipe(pipe, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		t.Fatalf("connect test pipe: %v", err)
	}
	payload, err := json.Marshal(supportRequest{Action: supportActionStatus, Account: sshLoginAccount()})
	if err != nil {
		t.Fatalf("marshal support request: %v", err)
	}
	if _, err := clientFile.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write support request: %v", err)
	}

	caller, request, err := readAuthenticatedSupportRequest(pipe, serverFile)
	if err != nil {
		t.Fatalf("readAuthenticatedSupportRequest: %v", err)
	}
	if request.Action != supportActionStatus {
		t.Fatalf("unexpected action %q", request.Action)
	}
	if caller.ProcessID != uint32(os.Getpid()) {
		t.Fatalf("caller PID = %d, want %d", caller.ProcessID, os.Getpid())
	}
	if caller.Account == "" || caller.SID == "" {
		t.Fatalf("caller identity is incomplete: %+v", caller)
	}
}

func TestWriteSupportKeyContentsReusesPrecreatedSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support-slot")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create support slot: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat support slot before write: %v", err)
	}
	if err := writeSupportKeyContents(path, []byte("authorized\n")); err != nil {
		t.Fatalf("writeSupportKeyContents: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat support slot after write: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("support key slot was replaced instead of updated in place")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read support slot: %v", err)
	}
	if string(payload) != "authorized\n" {
		t.Fatalf("support slot content = %q", payload)
	}
}

func TestWriteSupportKeyContentsRequiresPrecreatedSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-slot")
	if err := writeSupportKeyContents(path, []byte("authorized\n")); err == nil {
		t.Fatal("expected a missing support slot to be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing support slot was unexpectedly created: %v", err)
	}
}

func TestClearStaleSupportKeysPreservesTrustedSlots(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, supportKeysFilePrefix+"-tester")
	unmanaged := filepath.Join(root, "customer-key")
	if err := os.WriteFile(managed, []byte("temporary authorization"), 0o600); err != nil {
		t.Fatalf("write managed slot: %v", err)
	}
	if err := os.WriteFile(unmanaged, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unmanaged key: %v", err)
	}
	before, err := os.Stat(managed)
	if err != nil {
		t.Fatalf("stat managed slot: %v", err)
	}
	if err := clearStaleSupportKeys(root); err != nil {
		t.Fatalf("clearStaleSupportKeys: %v", err)
	}
	after, err := os.Stat(managed)
	if err != nil {
		t.Fatalf("managed slot was removed: %v", err)
	}
	if !os.SameFile(before, after) || after.Size() != 0 {
		t.Fatalf("managed slot was not preserved and cleared: same=%v size=%d", os.SameFile(before, after), after.Size())
	}
	payload, err := os.ReadFile(unmanaged)
	if err != nil {
		t.Fatalf("read unmanaged key: %v", err)
	}
	if string(payload) != "keep" {
		t.Fatalf("unmanaged key changed: %q", payload)
	}
}

func TestSupportControllerCloseUnblocksIdleListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	controller := &supportController{
		ctx:       ctx,
		cancel:    cancel,
		pipeName:  fmt.Sprintf("\\\\.\\pipe\\1CatTunnelRemoteSupport-close-%d-%d", os.Getpid(), time.Now().UnixNano()),
		serveDone: make(chan struct{}),
		sessions:  make(map[string]*supportSession),
	}
	go controller.serve()

	deadline := time.Now().Add(2 * time.Second)
	for {
		controller.listenerMu.Lock()
		listener := controller.listener
		controller.listenerMu.Unlock()
		if listener != 0 && listener != windows.InvalidHandle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("support listener did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	closed := make(chan struct{})
	go func() {
		controller.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("support controller Close blocked on an idle named-pipe listener")
	}

	// Close is deliberately idempotent because service shutdown paths may converge.
	controller.Close()
}
