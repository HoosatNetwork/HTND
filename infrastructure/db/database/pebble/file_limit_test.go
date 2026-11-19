package pebble

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/cockroachdb/pebble/v2"
)

func TestOptionsFileLimit(t *testing.T) {
	tests := []struct {
		name, value string
		unset       bool
		limit       uint64
		limitErr    error
		want        int
	}{
		{name: "default", unset: true, limit: 2048, want: 768},
		{name: "minimum node limit", unset: true, limit: 1024, want: 128},
		{name: "large limit", unset: true, limit: 65536, want: 40860},
		{name: "unbounded", unset: true, limit: ^uint64(0), want: 40860},
		{name: "override below budget", value: "400", limit: 2048, want: 400},
		{name: "override above budget", value: "9000", limit: 2048, want: 768},
		{name: "exact budget", value: "768", limit: 2048, want: 768},
		{name: "large override", value: strconv.Itoa(int(^uint(0) >> 1)), limit: 2048, want: 768},
		{name: "positive small override", value: "1", limit: 2048, want: 1},
		{name: "zero", value: "0", limit: 65536, want: 40860},
		{name: "negative", value: "-1", limit: 65536, want: 40860},
		{name: "garbage", value: "not-a-number", limit: 65536, want: 40860},
		{name: "empty", value: "", limit: 65536, want: 40860},
		{name: "overflow", value: "18446744073709551616", limit: 65536, want: 40860},
		{name: "invalid still clamped", value: "invalid", limit: 2048, want: 768},
		{name: "reserve boundary", unset: true, limit: 1408, want: 128},
		{name: "below reserve boundary", unset: true, limit: 1407, want: 128},
		{name: "small tool limit", unset: true, limit: 256, want: 128},
		{name: "tiny limit", unset: true, limit: 64, want: 64},
		{name: "zero limit", unset: true, limit: 0, want: 1},
		{name: "query error", unset: true, limitErr: errors.New("query failed"), want: 40860},
		{name: "query error override", value: "400", limitErr: errors.New("query failed"), want: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HTND_PEBBLE_MAX_OPEN_FILES", test.value)
			if test.unset {
				if err := os.Unsetenv("HTND_PEBBLE_MAX_OPEN_FILES"); err != nil {
					t.Fatal(err)
				}
			}
			options := optionsWithFileLimit(8, func() (uint64, error) { return test.limit, test.limitErr })
			defer options.Cache.Unref()
			if got := options.MaxOpenFiles; got != test.want {
				t.Fatalf("MaxOpenFiles = %d, want %d", got, test.want)
			}
		})
	}
}

type fileLimitLogBuffer struct{ bytes.Buffer }

func (*fileLimitLogBuffer) Close() error { return nil }

func TestFileLimitLogging(t *testing.T) {
	backend := logger.NewBackend()
	writer := &fileLimitLogBuffer{}
	if err := backend.AddLogWriter(writer, logger.LevelInfo); err != nil {
		t.Fatal(err)
	}
	if err := backend.Run(); err != nil {
		t.Fatal(err)
	}
	oldLog := log
	log = backend.Logger("TEST")
	log.SetLevel(logger.LevelInfo)
	logFileLimitOnce = sync.Once{}
	defer func() { backend.Close(); log = oldLog; logFileLimitOnce = sync.Once{} }()

	t.Setenv("HTND_PEBBLE_MAX_OPEN_FILES", "secret/path\nforged-log")
	for range 2 {
		options := optionsWithFileLimit(8, func() (uint64, error) { return 2048, nil })
		options.Cache.Unref()
	}
	backend.Close() // drains the writer before inspection
	output := writer.String()
	if !strings.Contains(output, "Invalid HTND_PEBBLE_MAX_OPEN_FILES; using default 40860") {
		t.Fatalf("missing invalid-value warning: %s", output)
	}
	if strings.Contains(output, "secret") || strings.Contains(output, "forged-log") {
		t.Fatalf("environment contents leaked into logs: %s", output)
	}
	if got := strings.Count(output, "Pebble MaxOpenFiles=768 (requested=40860, soft limit=2048, reserve=1280)"); got != 1 {
		t.Fatalf("effective value logged %d times, want once: %s", got, output)
	}
}

// Exercise the cache budgets against a real DB, including reopening an SST written
// with the old high allowance under the node's minimum and desired soft limits.
func TestFileLimitDatabaseReopen(t *testing.T) {
	t.Setenv("HTND_PEBBLE_MAX_OPEN_FILES", "40860")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "false")
	t.Setenv("HTND_TEST_MODE", "false")
	path := t.TempDir()
	for _, limit := range []uint64{65536, 1024, 2048} {
		t.Run(strconv.FormatUint(limit, 10), func(t *testing.T) {
			options := optionsWithFileLimit(8, func() (uint64, error) { return limit, nil })
			defer options.Cache.Unref()
			db, err := pebble.Open(path, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			if limit != 65536 {
				value, closer, err := db.Get([]byte("key"))
				if err != nil {
					t.Fatal(err)
				}
				if string(value) != "value" {
					t.Errorf("value = %q, want value", value)
				}
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Set([]byte("key"), []byte("value"), pebble.Sync); err != nil {
				t.Fatal(err)
			}
			if err := db.Flush(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
