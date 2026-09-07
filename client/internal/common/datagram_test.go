package common

import (
	"bytes"
	"net"
	"testing"
	"time"
)

type chunkWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *chunkWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.limit {
		payload = payload[:w.limit]
	}
	return w.buffer.Write(payload)
}

func TestWriteFrameHandlesShortWrites(t *testing.T) {
	writer := &chunkWriter{limit: 2}
	payload := []byte("datagram-payload")
	if err := WriteFrame(writer, payload); err != nil {
		t.Fatalf("WriteFrame returned error: %v", err)
	}
	decoded, err := ReadFrame(bytes.NewReader(writer.buffer.Bytes()))
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded payload = %q, want %q", decoded, payload)
	}
}

func TestProxyDatagramsStopsWhenStreamCloses(t *testing.T) {
	localProxy, localPeer := net.Pipe()
	streamProxy, streamPeer := net.Pipe()
	done := make(chan struct{})
	go func() {
		ProxyDatagrams(localProxy, streamProxy, nil, nil)
		close(done)
	}()

	if err := streamPeer.Close(); err != nil {
		t.Fatal(err)
	}
	defer localPeer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ProxyDatagrams remained blocked after the stream closed")
	}
}

func TestProxyDatagramsStopsWhenLocalConnectionCloses(t *testing.T) {
	localProxy, localPeer := net.Pipe()
	streamProxy, streamPeer := net.Pipe()
	done := make(chan struct{})
	go func() {
		ProxyDatagrams(localProxy, streamProxy, nil, nil)
		close(done)
	}()

	if err := localPeer.Close(); err != nil {
		t.Fatal(err)
	}
	defer streamPeer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ProxyDatagrams remained blocked after the local connection closed")
	}
}

func FuzzReadFrame(f *testing.F) {
	valid := &bytes.Buffer{}
	if err := WriteFrame(valid, []byte("seed")); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, input []byte) {
		payload, err := ReadFrame(bytes.NewReader(input))
		if err == nil && len(payload) > MaxDatagramSize {
			t.Fatalf("decoded oversized datagram: %d", len(payload))
		}
	})
}
