# 1cat Tunnel Linux Client Package

This is the Linux amd64 0.5.3 client package. Windows remains on its existing release.

## Files

- `tunnel-client`
- `client-linux.json`
- `start-client.sh`
- `install-client-systemd.sh`
- `1cat-tunnel-ca.pem`
- `README.md`

## Default behavior

The default config points to `dx.1catai.com:50001` but does not include the enrollment password.

Run `1cattunnel -setup` to configure the client, register autostart, and remain in the current terminal. The optional Web panel remains available using `1cattunnel panel`. Its private login link expires on restart; do not share it. Registration replaces the entered enrollment credential with an independent node token stored with mode `0600`.

TLS 1.3 and certificate verification are enabled by default. The project CA is embedded in the executable and is also provided as `1cat-tunnel-ca.pem` for inspection or optional system/browser import.

## Run

```bash
sh ./start-client.sh
```

The command remains running and serves the local management page. Edit `client-linux.json` before delivery if you want a fixed customer node name. The default SSH preset is `0.0.0.0:22`.

The Linux client also exposes a local loopback API for submitting blocked IPs to the server:

```bash
curl -u admin:YOUR_ADMIN_PASSWORD \
  -H "Content-Type: application/json" \
  -d '{"ip":"203.0.113.10","reason":"scan"}' \
  http://127.0.0.1:51888/api/block-ip
```

The server validates the same WebUI administrator username/password before changing the persistent blocklist.

## Install as systemd service

```bash
sh ./install-client-systemd.sh --yes
1cattunnel -setup
```

The installer copies the binary to `/usr/local/lib/1cat-tunnel`, creates `/usr/local/bin/1cattunnel`, and preserves configuration under `~/.config/1cat-tunnel`. First installation does not bind or start systemd. Saving setup registers boot autostart and continues in the current terminal. Upgrading an existing active service preserves its boot setting and restarts it with rollback on failure. Default config directories are secured without following links or modifying foreign-owned paths.

The Web panel is optional. Run these commands as the service owner:

```bash
1cattunnel status
1cattunnel panel
1cattunnel -setup
1cattunnel -config /absolute/path/client.json
1cattunnel monitor
1cattunnel desktop
```

This operation is explicit; simply extracting the package never starts a service. Stop and remove the service before deleting the program: `sh ./uninstall-client-systemd.sh --yes`. Configuration and rollback backups are retained.
