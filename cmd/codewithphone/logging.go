package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	backgroundLogEnv     = "CODEWITHPHONE_BACKGROUND"
	defaultLogMaxBytes   = 20 * 1024 * 1024
	defaultLogBackups    = 2
	defaultLogLineMaxLen = 64 * 1024
	defaultRepeatBurst   = 20
	defaultRepeatWindow  = 10 * time.Second
)

type closeFunc func() error

func configureBackgroundLogging(path string) (io.Writer, closeFunc, error) {
	rotating, err := newRotatingLogWriter(path, defaultLogMaxBytes, defaultLogBackups)
	if err != nil {
		return nil, nil, err
	}
	limited := newLogLimitWriter(rotating, defaultLogLineMaxLen, defaultRepeatBurst, defaultRepeatWindow)
	return limited, rotating.Close, nil
}

func shouldUseManagedLogging(path string) bool {
	if os.Getenv(backgroundLogEnv) == "1" {
		return true
	}
	cleanPath := filepath.Clean(path)
	for _, fd := range []string{"/proc/self/fd/1", "/proc/self/fd/2"} {
		target, err := os.Readlink(fd)
		if err != nil {
			continue
		}
		if filepath.Clean(target) == cleanPath {
			return true
		}
	}
	return false
}

type rotatingLogWriter struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

func newRotatingLogWriter(path string, maxBytes int64, maxBackups int) (*rotatingLogWriter, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("max log bytes must be positive")
	}
	if maxBackups < 1 {
		return nil, fmt.Errorf("max log backups must be positive")
	}
	w := &rotatingLogWriter{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	if w.size >= w.maxBytes {
		if err := w.truncateLocked(); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	return w, nil
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	written := 0
	for len(p) > 0 {
		if w.size >= w.maxBytes {
			if err := w.rotateLocked(); err != nil {
				return written, err
			}
		}
		remaining := int(w.maxBytes - w.size)
		if remaining <= 0 {
			continue
		}
		chunk := p
		if len(chunk) > remaining {
			chunk = p[:remaining]
		}
		n, err := w.file.Write(chunk)
		w.size += int64(n)
		written += n
		p = p[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingLogWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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

func (w *rotatingLogWriter) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	for i := w.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		dst := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Remove(dst)
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := os.Stat(w.path); err == nil {
		dst := fmt.Sprintf("%s.1", w.path)
		_ = os.Remove(dst)
		if err := os.Rename(w.path, dst); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func (w *rotatingLogWriter) truncateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.size = 0
	return nil
}

type logLimitWriter struct {
	mu           sync.Mutex
	dst          io.Writer
	lineMaxLen   int
	repeatBurst  int
	repeatWindow time.Duration
	pending      []byte
	lastLine     string
	lastWindow   time.Time
	repeatCount  int
	suppressed   int
}

func newLogLimitWriter(dst io.Writer, lineMaxLen int, repeatBurst int, repeatWindow time.Duration) *logLimitWriter {
	return &logLimitWriter{
		dst:          dst,
		lineMaxLen:   lineMaxLen,
		repeatBurst:  repeatBurst,
		repeatWindow: repeatWindow,
	}
}

func (w *logLimitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	accepted := len(p)
	w.pending = append(w.pending, p...)
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			if len(w.pending) > w.lineMaxLen {
				line := append([]byte(nil), w.pending[:w.lineMaxLen]...)
				dropped := len(w.pending) - w.lineMaxLen
				w.pending = w.pending[:0]
				if err := w.writeLineLocked(line, dropped); err != nil {
					return accepted, err
				}
			}
			return accepted, nil
		}
		line := append([]byte(nil), w.pending[:idx]...)
		w.pending = w.pending[idx+1:]
		dropped := 0
		if len(line) > w.lineMaxLen {
			dropped = len(line) - w.lineMaxLen
			line = line[:w.lineMaxLen]
		}
		if err := w.writeLineLocked(line, dropped); err != nil {
			return accepted, err
		}
	}
}

func (w *logLimitWriter) writeLineLocked(line []byte, dropped int) error {
	now := time.Now()
	lineKey := string(line)
	if lineKey == w.lastLine && now.Sub(w.lastWindow) <= w.repeatWindow {
		w.repeatCount++
		if w.repeatCount > w.repeatBurst {
			w.suppressed++
			return nil
		}
	} else {
		if err := w.flushSuppressedLocked(); err != nil {
			return err
		}
		w.lastLine = lineKey
		w.lastWindow = now
		w.repeatCount = 1
		w.suppressed = 0
	}

	if dropped > 0 {
		_, err := fmt.Fprintf(w.dst, "%s ... [truncated %d bytes]\n", line, dropped)
		return err
	}
	_, err := w.dst.Write(append(line, '\n'))
	return err
}

func (w *logLimitWriter) flushSuppressedLocked() error {
	if w.suppressed == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w.dst, "[suppressed %d repeated log lines]\n", w.suppressed)
	w.suppressed = 0
	return err
}
