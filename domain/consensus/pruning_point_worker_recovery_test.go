package consensus_test

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

// Compare real worker moves with the unchanged synchronous reference, including
// archival retention, restart, terminal cancellation and clean downgrade.
func TestPruningWorkerRecoveryAndDowngrade(t *testing.T) {
	for _, archival := range []bool{false, true} {
		name := "pruned"
		if archival {
			name = "archival"
		}
		t.Run(name, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
				t.Setenv("HTND_TEST_MODE", "0")
				t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
				cfg.IsArchival = archival
				cfg.DisableDifficultyAdjustment = true
				raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
				if err != nil {
					t.Fatal(err)
				}
				n, closeNode := newPruningMoveNodeWithDatabase(t, cfg, "worker-recovery", true, raw)
				defer closeNode()
				reopen := func() {
					n.node.Close()

					n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, raw, n.dbPrefix, nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				reopen()
				for i := 0; i < 3; i++ {
					n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
					n.waitUntilUpdated()
				}
				target := n.pruningPoint()
				n.requireServedAndVerified(target)
				compareWorkerState(t, n)
				// A restart resumes the same journal automatically.
				reopen()
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				compareWorkerState(t, n)
				normalize := n.node.(interface{ PreparePruningPointDowngrade(context.Context) error })
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := normalize.PreparePruningPointDowngrade(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled normalization: %v", err)
				}
				bucket := database.MakeBucket(n.dbPrefix.Serialize())
				if exists, err := raw.Has(bucket.Key([]byte("pruning-point-utxo-set-journal-state"))); err != nil || !exists {
					t.Fatalf("cancel lost recovery marker: %t %v", exists, err)
				}
				if err := normalize.PreparePruningPointDowngrade(context.Background()); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"pruning-point-utxo-set-journal-state", "blocks-count-deleted", "blocks-deleted"} {
					if exists, err := raw.Has(bucket.Key([]byte(key))); err != nil || exists {
						t.Fatalf("downgrade retained %s: %t %v", key, exists, err)
					}
				}
				if historyDeletionPending(t, raw, n.dbPrefix) {
					t.Fatal("downgrade retained history jobs")
				}
				reopen()
				n.requireServedAndVerified(n.pruningPoint())
				reopen()
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				compareWorkerState(t, n)
			})
		})
	}
}

func compareWorkerState(t *testing.T, n *pruningMoveNode) {
	t.Helper()
	point := n.pruningPoint()
	want, err := n.source.GetPruningPointUTXOs(point, nil, 10000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := n.node.GetPruningPointUTXOs(point, nil, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("UTXO counts %d != %d", len(want), len(got))
	}
	for i := range want {
		if *got[i].Outpoint != *want[i].Outpoint || !got[i].UTXOEntry.Equal(want[i].UTXOEntry) {
			t.Fatalf("UTXO %d differs", i)
		}
	}
	chain, err := n.source.GetVirtualSelectedParentChainFromBlock(n.genesis)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range chain.Added {
		a, err := n.source.GetBlockInfo(hash)
		if err != nil {
			t.Fatal(err)
		}
		b, err := n.node.GetBlockInfo(hash)
		if err != nil {
			t.Fatal(err)
		}
		if a.BlockStatus != b.BlockStatus {
			t.Fatalf("status %s: %v != %v", hash, a.BlockStatus, b.BlockStatus)
		}
		for _, c := range []externalapi.Consensus{n.source, n.node} {
			_, found, err := c.GetBlock(hash)
			if err != nil {
				t.Fatal(err)
			}
			if !n.nodeConfig.IsArchival && found != (a.BlockStatus != externalapi.StatusHeaderOnly) {
				t.Fatalf("body/status disagreement %s", hash)
			}
		}
	}
	a, err := n.source.GetSyncInfo()
	if err != nil {
		t.Fatal(err)
	}
	b, err := n.node.GetSyncInfo()
	if err != nil {
		t.Fatal(err)
	}
	if a.BlockCount != b.BlockCount {
		t.Fatalf("block counts %d != %d", a.BlockCount, b.BlockCount)
	}
}

type reserveWorkerDB struct {
	*pebble.DB
	available, queries atomic.Uint64
}

func (d *reserveWorkerDB) AvailableDiskBytes() (uint64, error) {
	d.queries.Add(1)
	return d.available.Load(), nil
}

func TestPruningWorkerReserveRecovery(t *testing.T) {
	for _, archival := range []bool{false, true} {
		name := "pruned"
		if archival {
			name = "archival"
		}
		t.Run(name, func(t *testing.T) {
			testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
				t.Setenv("HTND_TEST_MODE", "0")
				t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
				cfg.IsArchival = archival
				cfg.DisableDifficultyAdjustment = true
				raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
				if err != nil {
					t.Fatal(err)
				}
				db := &reserveWorkerDB{DB: raw}
				db.available.Store(math.MaxUint64)
				n, closeNode := newPruningMoveNodeWithDatabase(t, cfg, "worker-reserve", true, db)
				defer closeNode()
				n.node.Close()

				n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
				if err != nil {
					t.Fatal(err)
				}
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				n.waitUntilUpdated()
				db.available.Store(0)
				queries := db.queries.Load()
				point := n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
				waitUntil(t, "reserve queried", func() bool { return db.queries.Load() > queries })
				if archival {
					n.requireRefused(point, point)
				} else {
					// The one-step fallback writes synchronously and then frees history.
					n.waitUntilUpdated()
					compareWorkerState(t, n)
				}
				db.available.Store(math.MaxUint64)
				if err := n.node.UpdatePruningPointIfRequired(); err != nil {
					t.Fatal(err)
				}
				n.waitUntilUpdated()
				compareWorkerState(t, n)
			})
		})
	}
}

func TestPruningWorkerCloseJoinsWriter(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		t.Setenv("HTND_TEST_MODE", "0")
		t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
		cfg.DisableDifficultyAdjustment = true
		raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
		if err != nil {
			t.Fatal(err)
		}
		db := &pp42WriteDB{DB: raw}
		n, closeNode := newPruningMoveNodeWithDatabase(t, cfg, "worker-join", true, db)
		defer closeNode()
		n.node.Close()

		n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		n.waitUntilUpdated()
		hold := db.arm()
		defer hold.Release()
		n.insertUntilThePruningPointMoves(n.insertAndUpdate, nil)
		select {
		case <-hold.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("writer did not reach barrier")
		}
		joined := make(chan struct{})
		go func() { n.node.Close(); close(joined) }()
		select {
		case <-joined:
			t.Fatal("Close returned while writer still owned a transaction")
		case <-time.After(50 * time.Millisecond):
		}
		hold.Release()
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			t.Fatal("Close did not join writer")
		}

		n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
		if err != nil {
			t.Fatal(err)
		}
		n.waitUntilUpdated()
		compareWorkerState(t, n)
	})
}
