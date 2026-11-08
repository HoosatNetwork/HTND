package main

import (
	"io"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

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
				if limit < 0 { // a read of the effective limit, not a set
					return 0
				}
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

// TestApplyDefaultMemoryLimitLogsEffectiveValues checks that the startup log states the effective
// GOMEMLIMIT and GOGC, with their source, whether they were chosen
// automatically or came from the environment.
func TestApplyDefaultMemoryLimitLogsEffectiveValues(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		runtimeLimit int64 // what the runtime enforces, as it would after applying an explicit GOMEMLIMIT
		runtimeGC    int
		want         []string
		wantNot      []string
	}{
		{
			name:      "automatic",
			env:       map[string]string{},
			runtimeGC: 100,
			want:      []string{" -> GOMEMLIMIT set to: ", " -> GOGC set to: ", " -> HTND_PEBBLE_CACHE_MB set to: "},
			wantNot:   []string{"(environment)"},
		},
		{
			name:         "environment",
			env:          map[string]string{"GOMEMLIMIT": "4GiB", "GOGC": "75", "HTND_PEBBLE_CACHE_MB": "512"},
			runtimeLimit: 4 << 30,
			runtimeGC:    75,
			want: []string{
				" -> GOMEMLIMIT: 4096 MB (environment)",
				" -> GOGC: 75% (environment)",
			},
			wantNot: []string{"GOMEMLIMIT set to", "GOGC set to", "HTND_PEBBLE_CACHE_MB set to"},
		},
		{
			name:         "limit and GC off",
			env:          map[string]string{"GOMEMLIMIT": "off", "GOGC": "off"},
			runtimeLimit: math.MaxInt64,
			runtimeGC:    -1,
			want:         []string{" -> GOMEMLIMIT: off (environment)", " -> GOGC: off (environment)"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HTND_PEBBLE_CACHE_MB", test.env["HTND_PEBBLE_CACHE_MB"])
			defer debug.SetGCPercent(debug.SetGCPercent(test.runtimeGC))

			lookupEnv := func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			}
			limit := test.runtimeLimit
			setMemoryLimit := func(newLimit int64) int64 {
				previous := limit
				if newLimit >= 0 {
					limit = newLimit
				}
				return previous
			}

			output := captureStdout(t, func() { applyDefaultMemoryLimit(lookupEnv, setMemoryLimit) })
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Errorf("startup log lacks %q:\n%s", want, output)
				}
			}
			for _, wantNot := range test.wantNot {
				if strings.Contains(output, wantNot) {
					t.Errorf("startup log has unexpected %q:\n%s", wantNot, output)
				}
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	fn()
	os.Stdout = original
	writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
