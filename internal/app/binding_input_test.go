package app

import (
	"os"
	"testing"
)

func TestParseBindingApprovalInput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   bool
	}{
		{name: "y", input: "y", wantOK: true, want: true},
		{name: "Y", input: "Y", wantOK: true, want: true},
		{name: "yes", input: "yes", wantOK: true, want: true},
		{name: "YES", input: "YES", wantOK: true, want: true},
		{name: "space_yes", input: "  YeS  ", wantOK: true, want: true},
		{name: "n", input: "n", wantOK: true, want: false},
		{name: "N", input: "N", wantOK: true, want: false},
		{name: "no", input: "no", wantOK: true, want: false},
		{name: "NO", input: "NO", wantOK: true, want: false},
		{name: "empty", input: "", wantOK: true, want: true},
		{name: "unknown_with_y", input: "maybe", wantOK: true, want: true},
		{name: "unknown_no_y", input: "what", wantOK: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseBindingApprovalInput(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: got %v want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("decision mismatch: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestShouldAutoApproveBindingForTests(t *testing.T) {
	t.Setenv("DAEMON_TEST_AUTO_APPROVE_BINDING", "")
	if shouldAutoApproveBindingForTests() {
		t.Fatal("expected false when env is empty")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("DAEMON_TEST_AUTO_APPROVE_BINDING", value)
		if !shouldAutoApproveBindingForTests() {
			t.Fatalf("expected true for %q", value)
		}
	}

	t.Setenv("DAEMON_TEST_AUTO_APPROVE_BINDING", "no")
	if shouldAutoApproveBindingForTests() {
		t.Fatal("expected false for no")
	}

	_ = os.Unsetenv("DAEMON_TEST_AUTO_APPROVE_BINDING")
}
