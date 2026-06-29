package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingLogWriterRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codewithphone.log")

	writer, err := newRotatingLogWriter(path, 10, 2)
	if err != nil {
		t.Fatalf("newRotatingLogWriter() error = %v", err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte("1234567890abc")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte("defghijk")); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}

	if info, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(current) error = %v", err)
	} else if info.Size() > 10 {
		t.Fatalf("current log size = %d, want <= 10", info.Size())
	}
	if info, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("Stat(backup) error = %v", err)
	} else if info.Size() > 10 {
		t.Fatalf("backup log size = %d, want <= 10", info.Size())
	}
}

func TestRotatingLogWriterTruncatesOversizedExistingLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codewithphone.log")
	if err := os.WriteFile(path, []byte("existing oversized log"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	writer, err := newRotatingLogWriter(path, 10, 2)
	if err != nil {
		t.Fatalf("newRotatingLogWriter() error = %v", err)
	}
	defer writer.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("existing log size = %d, want 0 after startup truncate", info.Size())
	}
}

func TestLogLimitWriterTruncatesLongLines(t *testing.T) {
	var dst bytes.Buffer
	writer := newLogLimitWriter(&dst, 4, 20, time.Second)

	if _, err := writer.Write([]byte("abcdef\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := dst.String()
	if !strings.Contains(got, "abcd ... [truncated 2 bytes]") {
		t.Fatalf("limited output = %q, want truncation notice", got)
	}
}

func TestLogLimitWriterSuppressesRepeatedLines(t *testing.T) {
	var dst bytes.Buffer
	writer := newLogLimitWriter(&dst, 1024, 2, time.Minute)

	for i := 0; i < 5; i++ {
		if _, err := writer.Write([]byte("same\n")); err != nil {
			t.Fatalf("Write(same) error = %v", err)
		}
	}
	if _, err := writer.Write([]byte("different\n")); err != nil {
		t.Fatalf("Write(different) error = %v", err)
	}

	got := dst.String()
	if strings.Count(got, "same\n") != 2 {
		t.Fatalf("same count = %d, want 2; output = %q", strings.Count(got, "same\n"), got)
	}
	if !strings.Contains(got, "[suppressed 3 repeated log lines]") {
		t.Fatalf("output = %q, want suppression notice", got)
	}
	if !strings.Contains(got, "different\n") {
		t.Fatalf("output = %q, want different line", got)
	}
}

func TestShouldUseManagedLoggingWhenBackgroundEnvSet(t *testing.T) {
	t.Setenv(backgroundLogEnv, "1")

	if !shouldUseManagedLogging(filepath.Join(t.TempDir(), "codewithphone.log")) {
		t.Fatal("shouldUseManagedLogging() = false, want true")
	}
}
