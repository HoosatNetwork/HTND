package consensus_test

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	consensusdb "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// Copy only this fixture into ff2fac023 to create an actual 2.18.x datadir.
// The implementation under test is the unmodified base implementation.
func TestPP42CreateLegacyUpgradeFixture(t *testing.T) {
	dir := os.Getenv("PP42_LEGACY_UPGRADE_DIR")
	if dir == "" {
		t.Skip("legacy fixture output directory not supplied")
	}
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	defer constants.ForceSetBlockVersion(1)
	cfg := &consensus.Config{Params: dagconfig.TestnetParams, DeferPruningPointUTXOSetUpdate: true}
	cfg.SkipProofOfWork = true
	cfg.DisableDifficultyAdjustment = true
	cfg.POWScores = []uint64{math.MaxUint64}
	cfg.K = append([]externalapi.KType(nil), cfg.K...)
	cfg.K[0] = 0
	cfg.FinalityDuration = []time.Duration{30 * cfg.TargetTimePerBlock[0]}
	cfg.PruningProofM = 1
	cfg.DifficultyAdjustmentWindowSize = make([]int, len(cfg.DifficultyAdjustmentWindowSize))
	for i := range cfg.DifficultyAdjustmentWindowSize {
		cfg.DifficultyAdjustmentWindowSize[i] = 10
	}
	raw, err := pebble.OpenPebbleDB(dir, 8)
	if err != nil {
		t.Fatal(err)
	}
	node, _, err := consensus.NewFactory().NewConsensus(cfg, raw, &prefix.Prefix{}, nil)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	defer func() {
		node.Close()
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	}()
	bucket := consensusdb.MakeBucket((&prefix.Prefix{}).Serialize())
	last := cfg.GenesisHash
	moves := 0
	for i := 0; i < 400; i++ {
		template, err := node.BuildBlockTemplate(&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = node.ValidateAndInsertBlock(template.Block, true, true); err != nil {
			t.Fatal(err)
		}
		point, err := node.PruningPoint()
		if err != nil {
			t.Fatal(err)
		}
		if !point.Equal(last) {
			moves++
			last = point
			if moves == 3 {
				db := consensusdb.New(raw)
				ps := pruningstore.New(bucket, 2, false)
				if err = ps.StorePruningPointUTXOSetUpdateMethod(db, point, "diff-chain-walk"); err != nil {
					t.Fatal(err)
				}
				if pending, err := consensusdb.New(raw).Has(bucket.Key([]byte("updating-pruning-point-utxo-set"))); err != nil || !pending {
					t.Fatalf("fixture not pending: %t %v", pending, err)
				}
				if marker, err := consensusdb.New(raw).Has(bucket.Key([]byte("pruning-point-utxo-set-journal-state"))); err != nil || marker {
					t.Fatalf("fixture has journal: %t %v", marker, err)
				}
				return
			}
		}
		if err = node.UpdatePruningPointIfRequired(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(10 * time.Second); ; {
			pending, err := consensusdb.New(raw).Has(bucket.Key([]byte("updating-pruning-point-utxo-set")))
			if err != nil {
				t.Fatal(err)
			}
			if !pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("legacy update stalled")
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("legacy pruning point did not move")
}
