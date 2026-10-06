package consensus

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestNewConsensus(t *testing.T) {
	f := NewFactory()

	config := &Config{Params: dagconfig.DevnetParams}

	tmpDir, err := os.MkdirTemp("", "TestNewConsensus")
	if err != nil {
		return
	}

	db, err := ldb.NewLevelDB(tmpDir, 8)
	if err != nil {
		t.Fatalf("error in NewLevelDB: %s", err)
	}

	_, shouldMigrate, err := f.NewConsensus(config, db, &prefix.Prefix{}, nil)
	if err != nil {
		t.Fatalf("error in NewConsensus: %+v", err)
	}

	if shouldMigrate {
		t.Fatalf("A fresh consensus should never return shouldMigrate=true")
	}
}

// TestParseLargeCacheDivisor pins that HTND_LARGE_CACHE_DIVISOR divides the large caches by the value
// given, not by that value shifted into the millions.
func TestParseLargeCacheDivisor(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"", 1},
		{"abc", 1},
		{"0", 1},
		{"-3", 1},
		{"1", 1},
		{"2", 2},
		{"50", 50},
		{"51", 50},
		{"1000000", 50},
	}
	for _, test := range tests {
		if got := parseLargeCacheDivisor(test.value); got != test.want {
			t.Errorf("parseLargeCacheDivisor(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}
