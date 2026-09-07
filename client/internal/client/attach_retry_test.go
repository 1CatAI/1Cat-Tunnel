package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tunnel/internal/common"
)

func TestOpenAttachConnectionRetriesLegacyHandshakeDrops(t *testing.T) {
	const (
		connections  = 16
		initialBurst = 10
	)

	sess := &session{
		cfg: Config{
			NodeName: "retry-node",
		},
		accessToken: "dedicated-token",
	}

	var dialCalls atomic.Int32
	received := make(chan common.AttachRequest, connections)
	serverErrors := make(chan error, connections)
	dial := func(context.Context) (net.Conn, error) {
		call := int(dialCalls.Add(1))
		if call > initialBurst && call <= connections {
			return nil, errors.New("simulated legacy handshake limiter drop")
		}

		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			line, err := common.ReadMessageLine(bufio.NewReader(serverConn))
			if err != nil {
				serverErrors <- err
				return
			}
			var attach common.AttachRequest
			if err := json.Unmarshal(line, &attach); err != nil {
				serverErrors <- err
				return
			}
			received <- attach
			_, _ = io.Copy(io.Discard, serverConn)
		}()
		return clientConn, nil
	}

	start := make(chan struct{})
	errCh := make(chan error, connections)
	var wg sync.WaitGroup
	for index := 0; index < connections; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			req := common.OpenConnectionRequest{
				TunnelID: "tunnel-1",
				ConnID:   fmt.Sprintf("conn-%d", index),
			}
			conn, err := sess.openAttachConnectionWithPolicy(
				context.Background(),
				req,
				common.NetworkTCP,
				dial,
				4,
				time.Millisecond,
			)
			if err != nil {
				errCh <- err
				return
			}
			_ = conn.Close()
		}(index)
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("attach did not recover: %v", err)
	}
	if got, want := int(dialCalls.Load()), connections+(connections-initialBurst); got != want {
		t.Fatalf("dial calls = %d, want %d", got, want)
	}

	seen := make(map[string]bool, connections)
	for index := 0; index < connections; index++ {
		select {
		case err := <-serverErrors:
			t.Fatalf("read attach request: %v", err)
		case attach := <-received:
			if attach.Token != sess.accessToken || attach.NodeName != sess.cfg.NodeName {
				t.Fatalf("unexpected attach identity: %+v", attach)
			}
			if seen[attach.ConnID] {
				t.Fatalf("duplicate successful attach for %q", attach.ConnID)
			}
			seen[attach.ConnID] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for attach request")
		}
	}
}

func TestOpenAttachConnectionStopsAfterRetryBudget(t *testing.T) {
	sess := &session{}
	var dialCalls atomic.Int32
	expected := errors.New("server unavailable")
	dial := func(context.Context) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, expected
	}

	_, err := sess.openAttachConnectionWithPolicy(
		context.Background(),
		common.OpenConnectionRequest{ConnID: "retry-exhausted"},
		common.NetworkTCP,
		dial,
		3,
		time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), userText("3 次尝试", "after 3 attempts")) || !errors.Is(err, expected) {
		t.Fatalf("unexpected retry error: %v", err)
	}
	if got := dialCalls.Load(); got != 3 {
		t.Fatalf("dial calls = %d, want 3", got)
	}
}

func TestOpenAttachConnectionHonorsContextDuringRetryWait(t *testing.T) {
	sess := &session{}
	var dialCalls atomic.Int32
	dial := func(context.Context) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errors.New("temporary failure")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sess.openAttachConnectionWithPolicy(
		ctx,
		common.OpenConnectionRequest{ConnID: "cancelled"},
		common.NetworkTCP,
		dial,
		4,
		time.Second,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("dial calls = %d, want 1", got)
	}
}
