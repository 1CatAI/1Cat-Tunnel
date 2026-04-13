1cat Tunnel Linux Client

This folder is ready to hand to a customer.

Files:
- 1cat-tunnel-client
- start-client.sh
- install-client-systemd.sh
- client-linux.json

How to use:
1. Open a terminal in this folder
2. Run: sh ./start-client.sh
3. If the config is still a template, paste the node-specific bootstrap token from the server console
4. Follow the first-start wizard

Notes:
- No Go installation is required on the customer machine
- The binary is built with CGO disabled for portable delivery
- The helper scripts only require `/bin/sh`
- Logs are written to tunnel-client.log in this same folder
- The customer machine only needs outbound access to the server control address
