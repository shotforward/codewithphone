package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const pendingTerminalRetryInterval = 5 * time.Second

var pendingTerminalEventsMu sync.Mutex

type pendingTerminalEventRecord struct {
	Event      daemonEvent `json:"event"`
	EnqueuedAt string      `json:"enqueuedAt"`
	Attempts   int         `json:"attempts"`
	LastError  string      `json:"lastError,omitempty"`
}

func (c serverClient) enqueuePendingTerminalEvent(event daemonEvent, cause error) error {
	path := c.PendingTerminalEventPath
	if path == "" {
		return nil
	}
	event = c.normalizeEvent(event)
	record := pendingTerminalEventRecord{
		Event:      event,
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
		LastError:  errorString(cause),
	}

	pendingTerminalEventsMu.Lock()
	defer pendingTerminalEventsMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	log.Printf("queued pending terminal event taskRun=%s event=%s eventID=%s cause=%v",
		event.TaskRunID, event.EventType, event.EventID, cause)
	return nil
}

func (s *Service) runPendingTerminalEventLoop(ctx context.Context) {
	if s.serverClient.PendingTerminalEventPath == "" {
		return
	}
	if sent, err := s.serverClient.drainPendingTerminalEvents(ctx); err != nil {
		log.Printf("pending terminal event replay failed: %v", err)
	} else if sent > 0 {
		log.Printf("pending terminal events replayed: count=%d", sent)
	}

	ticker := time.NewTicker(pendingTerminalRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sent, err := s.serverClient.drainPendingTerminalEvents(ctx)
			if err != nil {
				log.Printf("pending terminal event replay failed: %v", err)
				continue
			}
			if sent > 0 {
				log.Printf("pending terminal events replayed: count=%d", sent)
			}
		}
	}
}

func (c serverClient) drainPendingTerminalEvents(ctx context.Context) (int, error) {
	path := c.PendingTerminalEventPath
	if path == "" {
		return 0, nil
	}

	pendingTerminalEventsMu.Lock()
	defer pendingTerminalEventsMu.Unlock()

	records, err := readPendingTerminalEventRecords(path)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}

	remaining := make([]pendingTerminalEventRecord, 0, len(records))
	sent := 0
	for _, record := range records {
		if ctx.Err() != nil {
			remaining = append(remaining, record)
			continue
		}
		event := c.normalizeEvent(record.Event)
		postCtx, cancel := context.WithTimeout(ctx, terminalEventPostTimeout)
		err := c.postEvent(postCtx, event)
		cancel()
		if err == nil {
			sent++
			continue
		}
		record.Event = event
		record.Attempts++
		record.LastError = errorString(err)
		remaining = append(remaining, record)
	}

	if err := writePendingTerminalEventRecords(path, remaining); err != nil {
		return sent, err
	}
	return sent, nil
}

func readPendingTerminalEventRecords(path string) ([]pendingTerminalEventRecord, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []pendingTerminalEventRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record pendingTerminalEventRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode pending terminal event: %w", err)
		}
		if record.Event.EventType == "" || record.Event.TaskRunID == "" {
			continue
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func writePendingTerminalEventRecords(path string, records []pendingTerminalEventRecord) error {
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
