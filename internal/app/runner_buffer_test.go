package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEventBufferRecordsPostEventError(t *testing.T) {
	client := &serverClient{
		BaseURL:   "http://codewithphone.test",
		MachineID: "machine_001",
		HTTPClient: &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("no blob store")),
				Header:     make(http.Header),
			}, nil
		})},
	}
	buffer := NewEventBuffer(client, "sess_001", "task_001")
	buffer.Append(context.Background(), "hello", "item_001")
	buffer.Flush(context.Background())

	err := buffer.LastError()
	if err == nil {
		t.Fatal("LastError() = nil, want postEvent error")
	}
	if !strings.Contains(err.Error(), "no blob store") {
		t.Fatalf("LastError() = %v, want server response body", err)
	}
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
