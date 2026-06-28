package app

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadRunnerLineReturnsLineWithoutNewline(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello\r\nnext\n"))

	line, err := readRunnerLine(reader, 1024, "test stdout")
	if err != nil {
		t.Fatalf("readRunnerLine() error = %v", err)
	}
	if string(line) != "hello" {
		t.Fatalf("line = %q, want hello", string(line))
	}

	line, err = readRunnerLine(reader, 1024, "test stdout")
	if err != nil {
		t.Fatalf("readRunnerLine() second error = %v", err)
	}
	if string(line) != "next" {
		t.Fatalf("second line = %q, want next", string(line))
	}
}

func TestReadRunnerLineHandlesEOFWithoutTrailingNewline(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("last line"))

	line, err := readRunnerLine(reader, 1024, "test stdout")
	if err != nil {
		t.Fatalf("readRunnerLine() error = %v", err)
	}
	if string(line) != "last line" {
		t.Fatalf("line = %q, want last line", string(line))
	}

	_, err = readRunnerLine(reader, 1024, "test stdout")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("readRunnerLine() final error = %v, want EOF", err)
	}
}

func TestReadRunnerLineTooLongDrainsCurrentLine(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("abcdef\nok\n"), 2)

	_, err := readRunnerLine(reader, 3, "codex stdout")
	var tooLong *runnerLineTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("readRunnerLine() error = %v, want runnerLineTooLongError", err)
	}
	if tooLong.Source != "codex stdout" {
		t.Fatalf("tooLong.Source = %q, want codex stdout", tooLong.Source)
	}

	line, err := readRunnerLine(reader, 3, "codex stdout")
	if err != nil {
		t.Fatalf("readRunnerLine() after too long error = %v", err)
	}
	if string(line) != "ok" {
		t.Fatalf("line after drain = %q, want ok", string(line))
	}
}
