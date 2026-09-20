package consensus

import (
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
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
