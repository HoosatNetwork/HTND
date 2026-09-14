package main

import "testing"

// TestApplyDefaultMemoryLimit pins that htnd's default memory limit applies only when GOMEMLIMIT is unset. A set
// value has already been applied by the Go runtime; it used to be re-parsed as a plain integer, so values with a unit
// suffix or "off" were replaced by the 8 GB default.
func TestApplyDefaultMemoryLimit(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantLimit int64
		wantSet   bool
	}{
		{name: "unset", env: map[string]string{}, wantLimit: defaultMemoryLimit, wantSet: true},
		{name: "unit suffix", env: map[string]string{"GOMEMLIMIT": "4GiB"}},
		{name: "off", env: map[string]string{"GOMEMLIMIT": "off"}},
		{name: "plain bytes", env: map[string]string{"GOMEMLIMIT": "123456789"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookupEnv := func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			}
			var gotLimit int64
			gotSet := false
			applyDefaultMemoryLimit(lookupEnv, func(limit int64) int64 {
				gotLimit, gotSet = limit, true
				return 0
			})
			if gotSet != test.wantSet || gotLimit != test.wantLimit {
				t.Fatalf("set=%t limit=%d, want set=%t limit=%d", gotSet, gotLimit, test.wantSet, test.wantLimit)
			}
		})
	}
}
