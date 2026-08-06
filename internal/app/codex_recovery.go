package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	codexRecoveryMaxMessages = 30
	codexRecoveryMaxChars    = 60000
)

type codexStreamDisconnectedError struct {
	Message string
	Raw     string
}

func (e *codexStreamDisconnectedError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Codex stream disconnected before completion"
	}
	return "Codex stream disconnected before completion: " + strings.TrimSpace(e.Message)
}

type codexStreamFailureRecord struct {
	ProviderSessionRef string
	Count              int
	LastFailedAt       time.Time
}

type codexErrorNotification struct {
	Message           string `json:"message"`
	Code              string `json:"code"`
	WillRetry         bool   `json:"willRetry"`
	AdditionalDetails string `json:"additionalDetails"`
	Error             *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func parseCodexErrorNotification(raw json.RawMessage) codexErrorNotification {
	var payload codexErrorNotification
	_ = json.Unmarshal(raw, &payload)
	if strings.TrimSpace(payload.Message) == "" && payload.Error != nil {
		payload.Message = payload.Error.Message
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = payload.AdditionalDetails
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = string(raw)
	}
	return payload
}

func isCodexResponseStreamDisconnected(raw string) bool {
	normalized := strings.ToLower(raw)
	return strings.Contains(normalized, "responsestreamdisconnected") ||
		strings.Contains(normalized, "stream disconnected before completion") ||
		strings.Contains(normalized, "websocket closed by server before response.completed")
}

func (s *Service) recordCodexStreamFailure(providerSessionKey, providerSessionRef string) int {
	providerSessionKey = strings.TrimSpace(providerSessionKey)
	providerSessionRef = strings.TrimSpace(providerSessionRef)
	if providerSessionKey == "" {
		providerSessionKey = providerSessionRef
	}
	if providerSessionKey == "" {
		return 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codexStreamFailures == nil {
		s.codexStreamFailures = map[string]codexStreamFailureRecord{}
	}
	record := s.codexStreamFailures[providerSessionKey]
	if strings.TrimSpace(record.ProviderSessionRef) != providerSessionRef {
		record = codexStreamFailureRecord{ProviderSessionRef: providerSessionRef}
	}
	record.Count++
	record.LastFailedAt = time.Now()
	s.codexStreamFailures[providerSessionKey] = record
	return record.Count
}

func (s *Service) clearCodexStreamFailure(providerSessionKey string) {
	providerSessionKey = strings.TrimSpace(providerSessionKey)
	if providerSessionKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.codexStreamFailures, providerSessionKey)
}

func buildCodexRecoveryPrompt(oldProviderSessionRef, recoveredContext, currentPrompt string) string {
	currentPrompt = strings.TrimSpace(currentPrompt)
	recoveredContext = strings.TrimSpace(recoveredContext)
	oldProviderSessionRef = strings.TrimSpace(oldProviderSessionRef)
	if recoveredContext == "" {
		return currentPrompt
	}
	var b strings.Builder
	b.WriteString("The previous Codex thread")
	if oldProviderSessionRef != "" {
		b.WriteString(" ")
		b.WriteString(oldProviderSessionRef)
	}
	b.WriteString(" disconnected repeatedly before completion. Continue in this new thread using only the recovered visible conversation context below. Do not mention this recovery unless it is directly relevant.\n\n")
	b.WriteString("<recovered_context>\n")
	b.WriteString(recoveredContext)
	b.WriteString("\n</recovered_context>\n\n")
	b.WriteString("<current_user_request>\n")
	b.WriteString(currentPrompt)
	b.WriteString("\n</current_user_request>")
	return b.String()
}

func extractCodexRecoveryContext(providerSessionRef string) (string, error) {
	providerSessionRef = strings.TrimSpace(providerSessionRef)
	if providerSessionRef == "" {
		return "", nil
	}
	path, err := findCodexSessionFile(providerSessionRef)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	return extractCodexRecoveryContextFromFile(path)
}

func findCodexSessionFile(providerSessionRef string) (string, error) {
	root := codexHomeDir()
	sessionsRoot := filepath.Join(root, "sessions")
	var newestPath string
	var newestMod time.Time
	err := filepath.WalkDir(sessionsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".jsonl") || !strings.Contains(name, providerSessionRef) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if newestPath == "" || info.ModTime().After(newestMod) {
			newestPath = path
			newestMod = info.ModTime()
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return newestPath, nil
}

func codexHomeDir() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

type codexRecoveryMessage struct {
	Role string
	Text string
}

func extractCodexRecoveryContextFromFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return extractCodexRecoveryContextFromReader(f)
}

func extractCodexRecoveryContextFromReader(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	messages := make([]codexRecoveryMessage, 0, codexRecoveryMaxMessages)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type == "compacted" || isCodexContextCompactedEvent(event.Type, event.Payload) {
			messages = messages[:0]
			continue
		}
		msg, ok := parseCodexRecoveryMessage(event.Type, event.Payload)
		if !ok {
			continue
		}
		messages = append(messages, msg)
		if len(messages) > codexRecoveryMaxMessages {
			copy(messages, messages[len(messages)-codexRecoveryMaxMessages:])
			messages = messages[:codexRecoveryMaxMessages]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return renderCodexRecoveryMessages(messages), nil
}

func isCodexContextCompactedEvent(eventType string, payload json.RawMessage) bool {
	if eventType != "event_msg" {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return false
	}
	return event.Type == "context_compacted"
}

func parseCodexRecoveryMessage(eventType string, payload json.RawMessage) (codexRecoveryMessage, bool) {
	if eventType != "response_item" {
		return codexRecoveryMessage{}, false
	}
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return codexRecoveryMessage{}, false
	}
	if item.Type != "message" {
		return codexRecoveryMessage{}, false
	}
	role := strings.TrimSpace(strings.ToLower(item.Role))
	if role != "user" && role != "assistant" {
		return codexRecoveryMessage{}, false
	}
	var parts []string
	imageCount := 0
	for _, content := range item.Content {
		switch content.Type {
		case "input_text", "output_text":
			text := strings.TrimSpace(content.Text)
			if text != "" {
				parts = append(parts, text)
			}
		case "input_image":
			imageCount++
		}
	}
	if imageCount > 0 {
		parts = append(parts, fmt.Sprintf("[image attachments in prior turn: %d]", imageCount))
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		return codexRecoveryMessage{}, false
	}
	return codexRecoveryMessage{Role: role, Text: text}, true
}

func renderCodexRecoveryMessages(messages []codexRecoveryMessage) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, msg := range messages {
		chunk := fmt.Sprintf("%s:\n%s\n\n", msg.Role, strings.TrimSpace(msg.Text))
		if b.Len()+len(chunk) > codexRecoveryMaxChars {
			break
		}
		b.WriteString(chunk)
	}
	return strings.TrimSpace(b.String())
}

func codexRecoveryPromptForProviderSession(providerSessionRef, currentPrompt string) string {
	recoveredContext, err := extractCodexRecoveryContext(providerSessionRef)
	if err != nil {
		return strings.TrimSpace(currentPrompt)
	}
	return buildCodexRecoveryPrompt(providerSessionRef, recoveredContext, currentPrompt)
}
