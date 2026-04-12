package config

import "testing"

func TestParseBindMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty defaults to auto", input: "", want: BindModeAuto},
		{name: "auto", input: "auto", want: BindModeAuto},
		{name: "force", input: "force", want: BindModeForce},
		{name: "token only", input: "token_only", want: BindModeTokenOnly},
		{name: "case insensitive", input: "FoRcE", want: BindModeForce},
		{name: "invalid", input: "random", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseBindMode(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil and mode=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected mode: got=%q want=%q", got, tc.want)
			}
		})
	}
}
