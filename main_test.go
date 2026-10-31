package main

import "testing"

// TestApplyDefaultMemoryLimit checks that an adaptive limit is selected only when GOMEMLIMIT is unset or empty.
// An explicit value has already been applied by the Go runtime, including unit suffixes and "off".
func TestApplyDefaultMemoryLimit(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantSet bool
	}{
		{name: "unset", env: map[string]string{}, wantSet: true},
		{name: "empty", env: map[string]string{"GOMEMLIMIT": ""}, wantSet: true},
		{name: "unit suffix", env: map[string]string{"GOMEMLIMIT": "4GiB"}},
		{name: "off", env: map[string]string{"GOMEMLIMIT": "off"}},
		{name: "plain bytes", env: map[string]string{"GOMEMLIMIT": "123456789"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Restore the cache environment after the automatic default is selected.
			t.Setenv("HTND_PEBBLE_CACHE_MB", "")
			lookupEnv := func(key string) (string, bool) {
				if key == "GOGC" {
					// Preserve the test process's GC settings.
					return "100", true
				}
				value, ok := test.env[key]
				return value, ok
			}
			var gotLimit int64
			gotSet := false
			applyDefaultMemoryLimit(lookupEnv, func(limit int64) int64 {
				gotLimit, gotSet = limit, true
				return 0
			})
			if gotSet != test.wantSet {
				t.Fatalf("set=%t, want set=%t", gotSet, test.wantSet)
			}
			// Available memory can change during the test, so check the adaptive
			// policy's bounds rather than pinning a machine-dependent value.
			if gotSet && (gotLimit <= 0 || gotLimit > 16<<30) {
				t.Fatalf("limit=%d, want a positive limit no larger than 16 GiB", gotLimit)
			}
		})
	}
}
