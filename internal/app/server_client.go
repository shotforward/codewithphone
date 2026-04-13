package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type machineInventory struct {
	AllowedRoots []string                   `json:"allowedRoots"`
	Projects     []string                   `json:"projects"`
	Capabilities runtimeCapabilitiesPayload `json:"capabilities"`
}

type taskDispatch struct {
	TaskRunID     string `json:"taskRunId"`
	SessionID     string `json:"sessionId"`
	Runtime       string `json:"runtime"`
	Model         string `json:"model,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot"`
	Prompt        string `json:"prompt"`

	// WorkspaceSnapshotRoot is the local path to the snapshot of the
	// workspace taken at turn start. Used by file.touched emitters to
	// compute "diff vs turn start" cumulative diffs at write time. Empty
	// when the turn profile has TrackChanges disabled.
	WorkspaceSnapshotRoot string `json:"-"`
}

type fsTaskDispatch struct {
	TaskID      string          `json:"taskId"`
	MachineID   string          `json:"machineId"`
	TaskType    string          `json:"taskType"`
	SessionID   string          `json:"sessionId"`
	RequestJSON json.RawMessage `json:"request"`
}

type daemonEvent struct {
	EventID    string `json:"eventId"`
	MachineID  string `json:"machineId"`
	SessionID  string `json:"sessionId,omitempty"`
	TaskRunID  string `json:"taskRunId"`
	OccurredAt string `json:"occurredAt"`
	EventType  string `json:"eventType"`
	Payload    any    `json:"payload"`
}

type sessionStatusResponse struct {
	Status string `json:"status"`
}

type recoverTasksResponse struct {
	Recovered int `json:"recovered"`
}

type serverClient struct {
	BaseURL      string
	MachineID    string
	MachineToken string
	HTTPClient   *http.Client
}

type httpStatusError struct {
	Op         string
	StatusCode int
	Body       string
}

func (e *httpStatusError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("%s failed: %d", e.Op, e.StatusCode)
	}
	return fmt.Sprintf("%s failed: %d (%s)", e.Op, e.StatusCode, strings.TrimSpace(e.Body))
}

func (c serverClient) claimTask(ctx context.Context) (*taskDispatch, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/tasks/claim"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var dispatch taskDispatch
		if err := json.NewDecoder(resp.Body).Decode(&dispatch); err != nil {
			return nil, err
		}
		return &dispatch, nil
	default:
		return nil, newHTTPStatusError("claim task", resp)
	}
}

func (c serverClient) postEvent(ctx context.Context, evt daemonEvent) error {
	if evt.EventID == "" {
		evt.EventID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if evt.MachineID == "" {
		evt.MachineID = c.MachineID
	}
	if evt.OccurredAt == "" {
		evt.OccurredAt = time.Now().UTC().Format(time.RFC3339)
	}

	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	t0 := time.Now()
	resp, err := c.httpClient().Do(req)
	if dur := time.Since(t0); dur > 50*time.Millisecond {
		log.Printf("[TIMING] postEvent HTTP %s: %v", evt.EventType, dur)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return newHTTPStatusError("post event", resp)
	}
	return nil
}

func (c serverClient) claimFSTask(ctx context.Context) (*fsTaskDispatch, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/fs/claim"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var dispatch fsTaskDispatch
		if err := json.NewDecoder(resp.Body).Decode(&dispatch); err != nil {
			return nil, err
		}
		return &dispatch, nil
	default:
		return nil, newHTTPStatusError("claim fs task", resp)
	}
}

func (c serverClient) completeFSTask(ctx context.Context, taskID, status, errorMessage string, result any) error {
	payload := map[string]any{
		"status": status,
		"error":  errorMessage,
		"result": result,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/fs/" + taskID + "/result"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return newHTTPStatusError("complete fs task", resp)
	}
	return nil
}

func (c serverClient) registerMachine(ctx context.Context, pairingCode, hostname, version, workspaceRoot string, inventory machineInventory) error {
	payload := map[string]any{
		"machineId":     c.MachineID,
		"pairingCode":   pairingCode,
		"hostname":      hostname,
		"daemonVersion": version,
		"workspaceRoot": workspaceRoot,
		"allowedRoots":  inventory.AllowedRoots,
		"capabilities":  inventory.Capabilities,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return newHTTPStatusError("register machine", resp)
	}
	return nil
}

func (c serverClient) heartbeat(ctx context.Context, inventory machineInventory) error {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/heartbeat"
	body, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return newHTTPStatusError("heartbeat", resp)
	}
	return nil
}

func (c serverClient) markMachineOffline(ctx context.Context) error {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/offline"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return newHTTPStatusError("mark machine offline", resp)
	}
	return nil
}

type deviceCodeResponse struct {
	MachineID string `json:"machineId"`
	Code      string `json:"code"`
}

func (c serverClient) requestDeviceCode(ctx context.Context, hostname string) (deviceCodeResponse, error) {
	payload := map[string]string{
		"machineId": c.MachineID,
		"hostname":  hostname,
	}
	body, _ := json.Marshal(payload)
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/auth/device-code"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return deviceCodeResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return deviceCodeResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return deviceCodeResponse{}, newHTTPStatusError("request device code", resp)
	}
	var result deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return deviceCodeResponse{}, err
	}
	return result, nil
}

type bindingPollResult struct {
	Status       string `json:"status"`
	Code         string `json:"code"`
	UserName     string `json:"userName"`
	UserEmail    string `json:"userEmail"`
	ConfirmNonce string `json:"confirmNonce"`
}

func (c serverClient) pollBindingStatus(ctx context.Context) (*bindingPollResult, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/auth/device-code/poll?machineId=" + c.MachineID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, newHTTPStatusError("poll binding status", resp)
	}

	var result bindingPollResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c serverClient) confirmBinding(ctx context.Context, code, confirmNonce string, approved bool) (string, error) {
	payload := map[string]any{
		"machineId":    c.MachineID,
		"code":         code,
		"confirmNonce": confirmNonce,
		"approved":     approved,
	}
	body, _ := json.Marshal(payload)
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/auth/device-code/confirm"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", newHTTPStatusError("confirm binding", resp)
	}

	var result struct {
		Status       string `json:"status"`
		MachineToken string `json:"machineToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.MachineToken, nil
}

func (c serverClient) fetchTaskStatus(ctx context.Context, taskRunID string) (string, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/tasks/" + taskRunID + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", newHTTPStatusError("fetch task status", resp)
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Status), nil
}

func (c serverClient) fetchSessionStatus(ctx context.Context, sessionID string) (string, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/sessions/" + sessionID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", newHTTPStatusError("fetch session status", resp)
	}
	var result sessionStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Status), nil
}

func (c serverClient) recoverActiveTasks(ctx context.Context) (int, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/machines/" + c.MachineID + "/tasks/recover"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.MachineToken != "" {
		req.Header.Set("X-Machine-Token", c.MachineToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result recoverTasksResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return 0, err
		}
		return result.Recovered, nil
	case http.StatusNoContent:
		return 0, nil
	case http.StatusNotFound:
		// Backward compatibility: older servers might not expose this endpoint.
		return 0, nil
	default:
		return 0, newHTTPStatusError("recover active tasks", resp)
	}
}

func (c serverClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func newHTTPStatusError(op string, resp *http.Response) error {
	if resp == nil {
		return &httpStatusError{Op: op, StatusCode: 0}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &httpStatusError{
		Op:         op,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
}
