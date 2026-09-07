# 1cat Tunnel Linux Client Package

This is a ready-to-deliver Linux amd64 client package.

## Files

- `tunnel-client`
- `client-linux.json`
- `start-client.sh`
- `install-client-systemd.sh`
- `1cat-tunnel-ca.pem`
- `README.md`

## Default behavior

The default config points to `dx.1catai.com:50001` but does not include the enrollment password.

The managed client starts even before a credential is configured. Open the private login link printed by the foreground client, or run `./tunnel-client -panel -config /absolute/path/client-linux.json` as the service owner. The link expires on restart; do not share it. The public loopback page does not reveal configuration. Select a hosted server, mapping count and local TCP/UDP targets, then enter the administrator-provided credential. Registration replaces it with an independent node token stored with mode `0600`.

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
```

The installer copies the binary to `/usr/local/lib/1cat-tunnel`, preserves configuration under `~/.config/1cat-tunnel`, and enables `1cat-tunnel-client.service` immediately and at boot.

This operation is explicit; simply extracting the package never starts a service. Stop and remove the service before deleting the program: `sh ./uninstall-client-systemd.sh --yes`. Configuration and rollback backups are retained.
