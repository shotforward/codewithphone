package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type approvalStatus struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	Decision           string `json:"decision"`
	Scope              string `json:"scope"`
	DecisionReason     string `json:"decisionReason"`
	RiskLevel          string `json:"riskLevel"`
	CommandFingerprint string `json:"commandFingerprint"`
}

type approvalClient struct {
	BaseURL      string
	HTTPClient   *http.Client
	PollInterval time.Duration
	MachineID    string
	MachineToken string
}

func (c approvalClient) waitForDecision(ctx context.Context, actionID string) (approvalStatus, error) {
	if strings.TrimSpace(actionID) == "" {
		return approvalStatus{}, fmt.Errorf("action id is required")
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		return approvalStatus{}, fmt.Errorf("base url is required")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	pollEvery := c.PollInterval
	if pollEvery <= 0 {
		pollEvery = 500 * time.Millisecond
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/approval-actions/"+actionID, nil)
		if err != nil {
			return approvalStatus{}, err
		}
		if token := strings.TrimSpace(c.MachineToken); token != "" {
			req.Header.Set("X-Machine-Token", token)
		}
		if machineID := strings.TrimSpace(c.MachineID); machineID != "" {
			req.Header.Set("X-Machine-ID", machineID)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return approvalStatus{}, err
		}

		var status approvalStatus
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return approvalStatus{}, fmt.Errorf("unexpected approval status response: %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return approvalStatus{}, decodeErr
		}
		if status.Status != "pending" {
			return status, nil
		}

		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return approvalStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
}
