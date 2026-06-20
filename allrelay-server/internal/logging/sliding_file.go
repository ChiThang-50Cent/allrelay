package logging

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const DefaultMaxBytes int64 = 10 * 1024 * 1024

type SlidingFileWriter struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
	file     *os.File
}

func NewSlidingFileWriter(path string, maxBytes int64) (*SlidingFileWriter, error) {
	if path == "" {
		return nil, fmt.Errorf("sliding log path is empty")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	w := &SlidingFileWriter{path: path, maxBytes: maxBytes}
	if err := w.openLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *SlidingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.openLocked(); err != nil {
		return 0, err
	}

	n, err := w.file.Write(p)
	if err != nil {
		return n, err
	}
	if err := w.trimLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *SlidingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *SlidingFileWriter) openLocked() error {
	if w.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	return nil
}

func (w *SlidingFileWriter) trimLocked() error {
	if w.file == nil {
		return nil
	}
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= w.maxBytes {
		return nil
	}

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	if int64(len(data)) <= w.maxBytes {
		return nil
	}

	start := len(data) - int(w.maxBytes)
	if start < 0 {
		start = 0
	}
	if start > 0 {
		if idx := bytes.IndexByte(data[start:], '\n'); idx >= 0 {
			start += idx + 1
		}
	}
	trimmed := data[start:]

	if err := w.file.Close(); err != nil {
		w.file = nil
		return err
	}
	w.file = nil

	if err := os.WriteFile(w.path, trimmed, info.Mode().Perm()); err != nil {
		return err
	}
	return w.openLocked()
}
