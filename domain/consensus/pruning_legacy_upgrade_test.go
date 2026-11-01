//go:build pruningtest

package consensus_test

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

func upgradedLegacyFixture(t *testing.T) (*pruningMoveNode, *pp42ViewDB, func()) {
	t.Helper()
	executable := os.Getenv("PP42_BASE_TEST_BINARY")
	if executable == "" {
		t.Skip("requires ff2fac023 probe with testdata/legacy_upgrade_fixture_test.go")
	}
	dir := t.TempDir()
	cmd := exec.Command(executable, "-test.run=^TestPP42CreateLegacyUpgradeFixture$", "-test.v")
	cmd.Env = append(os.Environ(), "PP42_LEGACY_UPGRADE_DIR="+dir)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy fixture: %v\n%s", err, data)
	}
	if !bytesContainFixturePass(data) {
		t.Fatalf("legacy fixture did not execute: %s", data)
	}
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	raw, err := pebble.OpenPebbleDB(dir, 8)
	if err != nil {
		t.Fatal(err)
	}
	db := &pp42ViewDB{DB: raw}
	cfg := pp42CrashConfig()
	var chain, acceptance atomic.Int32
	cfg.PruningPointJournalTestHook = func(p string) {
		if p == "derive-diff-chain" {
			chain.Add(1)
		}
		if p == "derive-acceptance" {
			acceptance.Add(1)
		}
	}
	n := &pruningMoveNode{t: t, nodeConfig: cfg, dbPrefix: &prefix.Prefix{}, db: &updateCountingDB{Database: db}, mismatches: make(chan error, 10)}
	n.updatingKey = database.MakeBucket(n.dbPrefix.Serialize()).Key([]byte("updating-pruning-point-utxo-set"))
	cfg.OnPruningPointUTXOSetMismatch = func(err error) { n.mismatches <- err }
	n.node, _, err = consensus.NewFactory().NewConsensus(cfg, db, n.dbPrefix, nil)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	closeNode := func() {
		n.node.Close()
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}
	if chain.Load() == 0 || acceptance.Load() != 0 {
		closeNode()
		t.Fatalf("recorded legacy method not honored: chain=%d acceptance=%d", chain.Load(), acceptance.Load())
	}
	n.requireServedAndVerified(n.pruningPoint())
	if state := pp42Manifest(t, raw); state == nil || state.Phase != "complete" || state.Target != n.pruningPoint().String() {
		closeNode()
		t.Fatalf("legacy recovery did not precede initialization: %+v", state)
	}
	return n, db, closeNode
}
func bytesContainFixturePass(data []byte) bool {
	return strings.Contains(string(data), "--- PASS: TestPP42CreateLegacyUpgradeFixture")
}
func TestPruningLegacyDatadirUpgrade(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	_, _, closeNode := upgradedLegacyFixture(t)
	defer closeNode()
}

func TestPruningLegacyUpgradeRestartMidJournal(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)
	n, db, closeNode := upgradedLegacyFixture(t)
	defer closeNode()
	n.node.Close()
	saved := pruningstore.JournalBatchEntries
	pruningstore.JournalBatchEntries = 2
	defer func() { pruningstore.JournalBatchEntries = saved }()
	hold := &scanHold{reached: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	defer hold.Release()
	var armed atomic.Bool
	armed.Store(true)
	n.nodeConfig.PruningPointJournalTestHook = func(p string) {
		if p == "data-commit" && armed.CompareAndSwap(true, false) {
			close(hold.reached)
			<-hold.release
		}
	}
	var err error
	n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, n.dbPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := pp42MineSelectedMove(t, n.node)
	if err = n.node.UpdatePruningPointIfRequired(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hold.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("upgraded worker did not reach journal batch")
	}
	state := pp42Manifest(t, db.DB)
	if state == nil || state.Phase != "ready" || state.Applied == 0 || state.Applied >= state.Count {
		t.Fatalf("not mid-journal: %+v", state)
	}
	cancelled := consensus.PruningCancellationForTest(n.node)
	joined := make(chan struct{})
	go func() { n.node.Close(); close(joined) }()
	// Close cancels the owner before the paused batch can continue.
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not cancel journal owner")
	}
	hold.Release()
	<-joined
	n.nodeConfig.PruningPointJournalTestHook = nil
	n.node, _, err = consensus.NewFactory().NewConsensus(n.nodeConfig, db, &prefix.Prefix{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.waitUntilUpdated()
	n.requireServedAndVerified(target)
	if s := pp42Manifest(t, db.DB); s == nil || s.Phase != "complete" || s.Target != target.String() {
		t.Fatalf("restart did not finish upgraded journal: %+v", s)
	}
}

func pp42MineSelectedMove(t *testing.T, node externalapi.Consensus) *externalapi.DomainHash {
	t.Helper()
	last, err := node.PruningPoint()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
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
			return point
		}
	}
	t.Fatal("pruning point did not move")
	return nil
}
