package common

import (
	"io"
	"net"
	"sync"
)

type closeWriter interface {
	CloseWrite() error
}

func Proxy(a, b net.Conn) {
	ProxyWithAccounting(a, b, nil, nil)
}

func ProxyWithAccounting(a, b net.Conn, onAToB func(uint64), onBToA func(uint64)) {
	var wg sync.WaitGroup
	copyStream := func(dst, src net.Conn, onBytes func(uint64)) {
		defer wg.Done()
		writer := io.Writer(dst)
		if onBytes != nil {
			writer = &countingWriter{
				writer: writer,
				onWrite: func(written int) {
					onBytes(uint64(written))
				},
			}
		}
		_, _ = io.Copy(writer, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
			return
		}
		_ = dst.Close()
	}

	wg.Add(2)
	go copyStream(b, a, onAToB)
	go copyStream(a, b, onBToA)
	wg.Wait()

	_ = a.Close()
	_ = b.Close()
}

type countingWriter struct {
	writer  io.Writer
	onWrite func(int)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 && w.onWrite != nil {
		w.onWrite(n)
	}
	return n, err
}
