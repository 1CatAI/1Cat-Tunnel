package common

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

const MaxDatagramSize = 64 * 1024

func WriteFrame(writer io.Writer, payload []byte) error {
	if len(payload) > MaxDatagramSize {
		return fmt.Errorf("frame is too large: %d bytes", len(payload))
	}

	header := [4]byte{}
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := writer.Write(payload)
	return err
}

func ReadFrame(reader io.Reader) ([]byte, error) {
	header := [4]byte{}
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}

	size := binary.BigEndian.Uint32(header[:])
	if size > MaxDatagramSize {
		return nil, fmt.Errorf("frame exceeds maximum size: %d bytes", size)
	}

	payload := make([]byte, size)
	if size == 0 {
		return payload, nil
	}

	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func ProxyDatagrams(local net.Conn, stream net.Conn, onLocalToStream func(uint64), onStreamToLocal func(uint64)) {
	defer local.Close()
	defer stream.Close()

	var (
		wg      sync.WaitGroup
		writeMu sync.Mutex
	)

	writeFrameSafe := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return WriteFrame(stream, payload)
	}

	copyLocalToStream := func() {
		defer wg.Done()
		buffer := make([]byte, MaxDatagramSize)
		for {
			n, err := local.Read(buffer)
			if n > 0 {
				payload := append([]byte(nil), buffer[:n]...)
				if writeErr := writeFrameSafe(payload); writeErr != nil {
					return
				}
				if onLocalToStream != nil {
					onLocalToStream(uint64(n))
				}
			}
			if err != nil {
				return
			}
		}
	}

	copyStreamToLocal := func() {
		defer wg.Done()
		reader := bufio.NewReader(stream)
		for {
			payload, err := ReadFrame(reader)
			if err != nil {
				return
			}
			if len(payload) == 0 {
				continue
			}
			n, writeErr := local.Write(payload)
			if n > 0 && onStreamToLocal != nil {
				onStreamToLocal(uint64(n))
			}
			if writeErr != nil {
				return
			}
		}
	}

	wg.Add(2)
	go copyLocalToStream()
	go copyStreamToLocal()
	wg.Wait()
}
