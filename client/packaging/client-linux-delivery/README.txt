1CatTunnel Linux Client 0.5.3

Ready-to-deliver Linux amd64 client folder.

Files:
- 1cat-tunnel-client
- start-client.sh
- install-client-systemd.sh
- client-linux.json
- 1cat-tunnel-ca.pem

Default config:
- server: dx.1catai.com:50001
- enrollment password: not included; enter it through the local Web management page
- default preset: SSH on 0.0.0.0:22
- TLS 1.3: enabled; project CA is embedded and also included as a PEM file
- local management page: http://127.0.0.1:51888/
- public port numbers: assigned by the selected server; users choose only the count and local targets

After first registration, the shared enrollment password is removed and the
dedicated node token is saved with restricted permissions.

Run:
  sh ./start-client.sh

Install the command (first installation does not start or bind systemd):
  sh ./install-client-systemd.sh --yes

Then configure and run in the same terminal:
  1cattunnel -setup

Saving setup registers boot autostart but keeps the running client in this
terminal. Ctrl+C stops this foreground client. The next machine boot starts
the registered service. Existing configured services retain their boot setting
and are restarted when the installer upgrades an active service.

The system installer creates /usr/local/bin/1cattunnel. Run these commands as
the service owner:
  1cattunnel
  1cattunnel status
  1cattunnel panel
  1cattunnel -setup
  1cattunnel -config /absolute/path/client.json
  1cattunnel monitor
  1cattunnel desktop

Running 1cattunnel again stops the matching service/old process and starts a
new foreground process. Only the same user's same configuration is replaced.
The terminal reports the actual server IP:port, assigned public endpoints,
TCP connections and traffic. Monitor and desktop commands never restart the
client; closing an observation window does not disconnect tunnels.

After setup, a Linux desktop login starts a lightweight observer. It opens
an installed terminal for status/logs when the client starts or restarts.
GNOME, KDE, XFCE, MATE, x-terminal-emulator and xterm are supported. No GUI
dependencies are installed automatically. A headless/SSH session uses the
current terminal; no window can appear before a graphical login exists.

The Web panel is optional. The terminal setup wizard and explicit JSON config
path remain supported. If the default ~/.config directory is owned by the
service user but is group/world-writable, the installer safely removes those
write bits before storing credentials. Linked, foreign-owned, and unsafe
custom paths are still rejected.

To open the optional Web panel, obtain its private link with:
  ./1cat-tunnel-client -panel -config /absolute/path/client-linux.json
The link is private and expires when the client restarts. Never share it.
The unauthenticated page does not expose configuration or status.

Stop and remove the system service before deleting the program:
  sh ./uninstall-client-systemd.sh --yes
Configuration and rollback backups are retained.

The installer copies the binary to /usr/local/lib/1cat-tunnel, installs the
command in /usr/local/bin, preserves configuration in ~/.config/1cat-tunnel,
and only registers boot autostart after setup. Per-config runtime logs are
kept beside the configuration as client-linux.json.log, mode 0600, at most 8 MiB.

Submit a blocked IP through the local Linux client API:
  curl -u admin:YOUR_ADMIN_PASSWORD -H "Content-Type: application/json" -d '{"ip":"203.0.113.10","reason":"scan"}' http://127.0.0.1:51888/api/block-ip

No Go installation is required on the customer machine.
