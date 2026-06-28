package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const (
	runnerStdoutLineLimitBytes = 64 * 1024 * 1024
	runnerStderrLineLimitBytes = 8 * 1024 * 1024
)

type runnerLineTooLongError struct {
	Source     string
	LimitBytes int
}

func (e *runnerLineTooLongError) Error() string {
	source := e.Source
	if source == "" {
		source = "runner output"
	}
	return fmt.Sprintf("%s line exceeded %s", source, formatByteLimit(e.LimitBytes))
}

func formatByteLimit(limit int) string {
	if limit > 0 && limit%(1024*1024) == 0 {
		return fmt.Sprintf("%dMB", limit/(1024*1024))
	}
	if limit > 0 && limit%1024 == 0 {
		return fmt.Sprintf("%dKB", limit/1024)
	}
	return fmt.Sprintf("%d bytes", limit)
}

func readRunnerLine(reader *bufio.Reader, limit int, source string) ([]byte, error) {
	if limit <= 0 {
		limit = runnerStdoutLineLimitBytes
	}

	line := make([]byte, 0, 64*1024)
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if len(line)+len(chunk) > limit {
				drainOversizedRunnerLine(reader, err)
				return nil, &runnerLineTooLongError{Source: source, LimitBytes: limit}
			}
			line = append(line, chunk...)
		}

		switch {
		case err == nil:
			return trimTrailingNewline(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return trimTrailingNewline(line), nil
		default:
			return nil, err
		}
	}
}

func drainOversizedRunnerLine(reader *bufio.Reader, readErr error) {
	for errors.Is(readErr, bufio.ErrBufferFull) {
		_, readErr = reader.ReadSlice('\n')
	}
}

func trimTrailingNewline(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}
