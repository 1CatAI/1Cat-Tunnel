# Third-party components

The Go standard library and golang.org/x/{crypto,sys,term,text} are built from the versions pinned in go.mod/go.sum using Go 1.26.8. Their BSD-style notices are included here. Go module downloads are verified against go.sum; no third-party source is represented as 1Cat's own code.

- Go source: https://go.dev/dl/ (go1.26.8)
- golang.org/x/crypto: https://go.googlesource.com/crypto/ (v0.56.0)
- golang.org/x/sys: https://go.googlesource.com/sys/ (v0.47.0)
- golang.org/x/term: https://go.googlesource.com/term/ (v0.45.0)
- golang.org/x/text: https://go.googlesource.com/text/ (v0.41.0)
- Windows OpenSSH source and releases: https://github.com/PowerShell/Win32-OpenSSH and https://github.com/PowerShell/openssh-portable

The Windows SSH delivery ZIP includes the upstream OpenSSH-Win64.zip separately, with SHA-256 `23f50f3458c4c5d0b12217c6a5ddfde0137210a30fa870e98b29827f7b43aba5` and runtime version OpenSSH_for_Windows_10.0p2. Place this verified file under `packaging/client-windows-ssh/payload/openssh/` when recreating the full delivery folder. The client validates both the archive hash and Microsoft Authenticode signatures before installation. The native Go build scripts do not compile or download OpenSSH.

To assemble a generic Windows package, combine out/tunnel-client-windows-amd64.exe (renamed to `1cat Tunnel Client.exe`), packaging/client-windows-delivery, examples/client-windows.json and internal/client/1cat-tunnel-ca.pem. For the SSH package, use out/1cattunnel-windows-ssh-amd64.exe renamed to `1cattunnel.exe` with packaging/client-windows-ssh and the verified payload. For Linux, combine out/tunnel-client-linux-amd64 renamed to `1cat-tunnel-client`, packaging/client-linux-delivery, the uninstall script from packaging/client-linux, examples/client-linux.json and the same public CA. Native ELF and shell scripts need mode 0755 in the tar.gz; never include a configured customer JSON.

The source tree's existing UNLICENSED status is unchanged. These third-party licenses do not grant a new license to the project's own source.
