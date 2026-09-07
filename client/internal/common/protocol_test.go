package common

import (
	"bufio"
	"bytes"
	"testing"
)

func TestReadMessageLineLimitAcceptsBoundaryAndPreservesFollowingBytes(t *testing.T) {
	reader := bufio.NewReader(bytes.NewBufferString("1234567\nSSH-2.0-test\r\n"))
	line, err := ReadMessageLineLimit(reader, 8)
	if err != nil {
		t.Fatalf("ReadMessageLineLimit returned error: %v", err)
	}
	if string(line) != "1234567" {
		t.Fatalf("line = %q, want %q", line, "1234567")
	}
	remainder, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read preserved payload: %v", err)
	}
	if remainder != "SSH-2.0-test\r\n" {
		t.Fatalf("remainder = %q", remainder)
	}
}

func TestReadMessageLineLimitRejectsOversizedMessage(t *testing.T) {
	reader := bufio.NewReader(bytes.NewBufferString("12345678\n"))
	if _, err := ReadMessageLineLimit(reader, 8); err == nil {
		t.Fatal("expected oversized message to be rejected")
	}
}

func TestWriteMessageHandlesShortWrites(t *testing.T) {
	writer := &chunkWriter{limit: 3}
	if err := WriteMessage(writer, Envelope{Type: "test"}); err != nil {
		t.Fatalf("WriteMessage returned error: %v", err)
	}
	if got, want := writer.buffer.String(), `{"type":"test"}`+"\n"; got != want {
		t.Fatalf("encoded message = %q", got)
	}
}

func FuzzReadMessageLineLimit(f *testing.F) {
	f.Add([]byte(`{"type":"test"}`+"\n"), 64)
	f.Add([]byte("no-newline"), 8)
	f.Fuzz(func(t *testing.T, input []byte, limit int) {
		if limit < 1 || limit > 4096 {
			return
		}
		line, err := ReadMessageLineLimit(bufio.NewReader(bytes.NewReader(input)), limit)
		if err == nil && (len(line) == 0 || len(line) > limit) {
			t.Fatalf("successful line length = %d, limit = %d", len(line), limit)
		}
	})
}
