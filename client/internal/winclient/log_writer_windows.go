//go:build windows

package winclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	serviceLogMaxBytes = 8 * 1024 * 1024
	serviceLogBackups  = 3
)

type rotatingFileWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func newRotatingFileWriter(path string, maxBytes int64, backups int) (*rotatingFileWriter, error) {
	if maxBytes <= 0 {
		return nil, errors.New("日志大小上限必须大于 0")
	}
	if backups < 1 {
		return nil, errors.New("日志备份数量至少为 1")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	writer := &rotatingFileWriter{path: path, maxBytes: maxBytes, backups: backups}
	if err := writer.openAppend(); err != nil {
		return nil, err
	}
	if writer.size >= writer.maxBytes {
		if err := writer.rotateLocked(); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	return writer, nil
}

func (w *rotatingFileWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(payload)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(payload)
	w.size += int64(written)
	return written, err
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingFileWriter) openAppend() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingFileWriter) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	var rotateErr error
	oldest := fmt.Sprintf("%s.%d", w.path, w.backups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		rotateErr = err
	}
	for index := w.backups - 1; index >= 1 && rotateErr == nil; index-- {
		source := fmt.Sprintf("%s.%d", w.path, index)
		target := fmt.Sprintf("%s.%d", w.path, index+1)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			rotateErr = err
			break
		}
		if err := replaceFileAtomic(source, target); err != nil {
			rotateErr = err
		}
	}
	if rotateErr == nil {
		if _, err := os.Stat(w.path); err == nil {
			rotateErr = replaceFileAtomic(w.path, w.path+".1")
		} else if !os.IsNotExist(err) {
			rotateErr = err
		}
	}

	openErr := w.openAppend()
	if rotateErr != nil || openErr != nil {
		return errors.Join(rotateErr, openErr)
	}
	return nil
}
