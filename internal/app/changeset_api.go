package app

import (
	"github.com/shotforward/codewithphone/internal/changeset"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type changeSetStatus struct {
	ID            string                  `json:"id"`
	Status        string                  `json:"status"`
	Decision      string                  `json:"decision"`
	FileDecisions []changeset.FileDecision `json:"fileDecisions"`
}

type changeSetClient struct {
	BaseURL      string
	HTTPClient   *http.Client
	PollInterval time.Duration
}

func (c changeSetClient) waitForDecision(ctx context.Context, changeSetID string) (changeSetStatus, error) {
	if strings.TrimSpace(changeSetID) == "" {
		return changeSetStatus{}, fmt.Errorf("changeset id is required")
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		return changeSetStatus{}, fmt.Errorf("base url is required")
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/changesets/"+changeSetID, nil)
		if err != nil {
			return changeSetStatus{}, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return changeSetStatus{}, err
		}

		var status changeSetStatus
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return changeSetStatus{}, fmt.Errorf("unexpected changeset status response: %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return changeSetStatus{}, decodeErr
		}
		if status.Decision != "" {
			return status, nil
		}

		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return changeSetStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
}
