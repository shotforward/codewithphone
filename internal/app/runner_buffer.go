package app

import (
	"context"
	"strings"
	"sync"
	"time"
)

type EventBuffer struct {
	server    *serverClient
	sessionID string
	taskRunID string
	mu        sync.Mutex
	builder   strings.Builder
	itemID    string
	timer     *time.Timer
	closed    bool
}

func NewEventBuffer(server *serverClient, sessionID, taskRunID string) *EventBuffer {
	eb := &EventBuffer{
		server:    server,
		sessionID: sessionID,
		taskRunID: taskRunID,
	}
	eb.timer = time.AfterFunc(200*time.Millisecond, func() {
		eb.doFlush()
	})
	eb.timer.Stop()
	return eb
}

func (eb *EventBuffer) Append(ctx context.Context, text string, itemID string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	if eb.closed {
		return
	}
	nextItemID := strings.TrimSpace(itemID)
	currentItemID := strings.TrimSpace(eb.itemID)
	if eb.builder.Len() > 0 && nextItemID != "" && currentItemID != "" && nextItemID != currentItemID {
		eb.timer.Stop()
		go eb.doFlush()
	}
	if nextItemID != "" {
		eb.itemID = nextItemID
	}

	wasEmpty := eb.builder.Len() == 0
	eb.builder.WriteString(text)

	if wasEmpty {
		eb.timer.Reset(200 * time.Millisecond)
	} else if eb.builder.Len() >= 100 {
		eb.timer.Stop()
		go eb.doFlush()
	}
}

func (eb *EventBuffer) Flush(ctx context.Context) {
	eb.mu.Lock()
	if eb.closed {
		eb.mu.Unlock()
		return
	}
	eb.timer.Stop()
	eb.mu.Unlock()
	eb.doFlush()
}

func (eb *EventBuffer) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.closed = true
	eb.timer.Stop()
}

func (eb *EventBuffer) doFlush() {
	eb.mu.Lock()
	text := eb.builder.String()
	itemID := strings.TrimSpace(eb.itemID)
	eb.builder.Reset()
	eb.itemID = ""
	eb.mu.Unlock()

	if text == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = eb.server.postEvent(ctx, daemonEvent{
		SessionID: eb.sessionID,
		TaskRunID: eb.taskRunID,
		EventType: "assistant.delta",
		Payload: map[string]any{
			"itemId": chooseNonEmpty(itemID, eb.taskRunID),
			"delta":  text,
		},
	})
}

func chooseNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
