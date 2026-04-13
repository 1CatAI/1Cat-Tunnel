FROM alpine:3.20

WORKDIR /opt/1cat-tunnel

COPY tunnel-server /usr/local/bin/tunnel-server
COPY server.json /etc/1cat-tunnel/server.json

RUN chmod +x /usr/local/bin/tunnel-server && \
    mkdir -p /etc/1cat-tunnel /var/lib/1cat-tunnel

VOLUME ["/var/lib/1cat-tunnel"]

ENTRYPOINT ["/usr/local/bin/tunnel-server", "-config", "/etc/1cat-tunnel/server.json", "-pause-on-exit=false"]
