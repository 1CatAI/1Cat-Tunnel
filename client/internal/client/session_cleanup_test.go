package client

import (
	"context"
	"net"
	"testing"
	"time"

	"tunnel/internal/common"
)

func TestSessionCancellationClosesIdleDataChannels(t *testing.T) {
	for _, test := range []struct {
		name  string
		proxy func(net.Conn, net.Conn)
	}{
		{"tcp", common.Proxy},
		{"udp", func(a, b net.Conn) { common.ProxyDatagrams(a, b, nil, nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			local, localPeer := net.Pipe()
			data, dataPeer := net.Pipe()
			defer local.Close()
			defer data.Close()
			defer localPeer.Close()
			defer dataPeer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := closeDataOnCancel(ctx, local, data)
			defer stop()
			finished := make(chan struct{})
			go func() {
				test.proxy(local, data)
				close(finished)
			}()
			cancel()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("idle data channel survived session cancellation")
			}
			for _, peer := range []net.Conn{localPeer, dataPeer} {
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := peer.Read(make([]byte, 1)); err == nil {
					t.Fatal("peer still open after session cancellation")
				} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					t.Fatal("peer blocked instead of seeing a closed connection")
				}
			}
		})
	}
}
