1cat Tunnel Linux Client

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

Install as systemd service:
  sh ./install-client-systemd.sh --yes

Open the private login link printed in the foreground terminal, or run:
  ./1cat-tunnel-client -panel -config /absolute/path/client-linux.json
The link is private and expires when the client restarts. Never share it.
The unauthenticated page does not expose configuration or status.

Stop and remove the system service before deleting the program:
  sh ./uninstall-client-systemd.sh --yes
Configuration and rollback backups are retained.

The installer copies the binary to /usr/local/lib/1cat-tunnel, preserves
configuration in ~/.config/1cat-tunnel, and enables the service at boot.

Submit a blocked IP through the local Linux client API:
  curl -u admin:YOUR_ADMIN_PASSWORD -H "Content-Type: application/json" -d '{"ip":"203.0.113.10","reason":"scan"}' http://127.0.0.1:51888/api/block-ip

No Go installation is required on the customer machine.
