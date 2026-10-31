package pruningmanager

import (
	"bytes"
	"math/big"
	"slices"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockrelationstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/multisetstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/reachabilitydatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/utxodiffstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtopologymanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtraversalmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/reachabilitymanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// This fixture deliberately rewrites mutable diff records and warms the LIVE store
// cache after snapshot capture. Using the snapshot with the live store is the
// negative control: it observes the later value. Private stores preserve the exact
// earlier method and diff, even when the selected PP index has advanced.
func TestPP05PrivateViewDoesNotLeakLiveDiffCache(t *testing.T) {
	for _, recorded := range []string{"diff-chain-walk", "acceptance-data", "unknown-method"} {
		t.Run(recorded, func(t *testing.T) { testPrivateViewEquivalence(t, recorded) })
	}
}

func testPrivateViewEquivalence(t *testing.T, recorded string) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	raw, err := pebble.OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db := database.New(raw)
	p := database.MakeBucket([]byte("view"))
	store := pruningstore.New(p, 2, false)
	diffs := utxodiffstore.New(p, 128, false)
	ghost := ghostdagdatastore.New(p.Bucket([]byte{0}), 128, false)
	base := externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	target := externalapi.NewDomainHashFromByteArray(&[32]byte{2})
	later := externalapi.NewDomainHashFromByteArray(&[32]byte{3})
	pm := &pruningManager{databaseContext: db, pruningStore: store, utxoDiffStore: diffs, ghostdagDataStore: ghost, genesisHash: later}
	header, err := blockheaderstore.New(db, p, 128, false)
	if err != nil {
		t.Fatal(err)
	}
	msStore := multisetstore.New(p, 128, false)
	reach := reachabilitydatastore.New(p, 128, false)
	rm := reachabilitymanager.New(db, ghost, reach)
	topology := dagtopologymanager.New(db, rm, blockrelationstore.New(p.Bucket([]byte{0}), 128, false), ghost)
	pm.dagTraversalManager = dagtraversalmanager.New(db, topology, ghost, rm, nil, nil, nil, pm.genesisHash, nil, nil, nil)
	pm.blockHeaderStore, pm.multiSetStore = header, msStore
	commit := func(stage *model.StagingArea) {
		t.Helper()
		if err := staging.CommitAllChanges(db, stage); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []*externalapi.DomainHash{base, target} {
		stage := model.NewStagingArea()
		if err = store.StagePruningPoint(db, stage, h); err != nil {
			t.Fatal(err)
		}
		commit(stage)
	}
	stage := model.NewStagingArea()
	ghost.Stage(stage, base, externalapi.NewBlockGHOSTDAGData(1, big.NewInt(1), later, nil, nil, nil, 0), false)
	ghost.Stage(stage, target, externalapi.NewBlockGHOSTDAGData(2, big.NewInt(2), base, nil, nil, nil, 0), false)
	out := externalapi.DomainOutpoint{Index: 42}
	entry := utxo.NewUTXOEntry(17, &externalapi.ScriptPublicKey{Script: []byte{0x51}}, false, 1)
	replacement := utxo.NewUTXOEntry(17, &externalapi.ScriptPublicKey{Script: []byte{0x51}}, false, 2)
	old, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{out: entry}), utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{out: replacement}))
	if err != nil {
		t.Fatal(err)
	}
	baseMS, targetMS := multiset.New(), multiset.New()
	baseBytes, err := utxo.SerializeUTXO(entry, &out)
	if err != nil {
		t.Fatal(err)
	}
	targetBytes, err := utxo.SerializeUTXO(replacement, &out)
	if err != nil {
		t.Fatal(err)
	}
	baseMS.Add(baseBytes)
	targetMS.Add(targetBytes)
	msStore.Stage(stage, base, baseMS)
	header.Stage(stage, target, blockheader.NewImmutableBlockHeader(1, nil, base, base,
		targetMS.Hash(), 0, 0, 0, 2, 2, big.NewInt(2), base))
	diffs.Stage(stage, base, old, target)
	commit(stage)
	if err = store.StorePruningPointUTXOSetUpdateMethod(db, target, recorded); err != nil {
		t.Fatal(err)
	}
	want, method, err := pm.pruningPointUTXOSetDiff(model.NewStagingArea(), target)
	if err != nil {
		t.Fatal(err)
	}
	if method != "diff-chain-walk" {
		t.Fatalf("expected recorded/fallback diff-chain walk, got %s", method)
	}
	// Capture while selection is already beyond the named materialized base/target.
	stage = model.NewStagingArea()
	if err = store.StagePruningPoint(db, stage, later); err != nil {
		t.Fatal(err)
	}
	commit(stage)
	snapshot, err := db.(interface {
		NewSnapshot() (*database.ReadSnapshot, error)
	}).NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	stage = model.NewStagingArea()
	diffs.Stage(stage, base, utxo.NewUTXODiff(), target)
	commit(stage)
	if err = store.StorePruningPointUTXOSetUpdateMethod(db, target, "later-invalid-method"); err != nil {
		t.Fatal(err)
	}
	leaked, err := diffs.UTXODiff(snapshot, model.NewStagingArea(), base)
	if err != nil {
		t.Fatal(err)
	}
	if leaked.Equal(old) {
		t.Fatal("negative control did not populate live cache")
	}
	for name, args := range map[string]struct {
		prefix       model.DBBucket
		index        uint64
		base, target *externalapi.DomainHash
	}{
		"wrong-base":       {p, 0, later, target},
		"wrong-target":     {p, 0, base, later},
		"wrong-instance":   {database.MakeBucket([]byte("other-instance")), 0, base, target},
		"unselected-index": {p, 2, later, target},
		"overflow":         {p, ^uint64(0), base, target},
		"genesis-target":   {p, 0, base, pm.genesisHash},
	} {
		if got, err := pm.readView(snapshot, args.prefix, args.index, args.base, args.target); err == nil || got != nil {
			t.Fatalf("%s identity accepted: %v", name, err)
		}
	}
	view, err := pm.readView(snapshot, p, 0, base, target)
	if err != nil {
		t.Fatal(err)
	}
	got, gotMethod, err := view.diff()
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != method || !got.Equal(want) {
		t.Fatalf("private view changed method/diff: %s vs %s", gotMethod, method)
	}
	if !slices.Equal(canonicalDiff(t, got), canonicalDiff(t, want)) {
		t.Fatal("canonical operations differ")
	}
	// Independent fixture expectation: the coin is restamped, preserving amount/script.
	if got.ToRemove().Len() != 1 || got.ToAdd().Len() != 1 {
		t.Fatal("unexpected operation count")
	}
	removed, removedOK := got.ToRemove().Get(&out)
	added, addedOK := got.ToAdd().Get(&out)
	if !removedOK || !addedOK || !removed.Equal(entry) || !added.Equal(replacement) {
		t.Fatal("wrong restamp")
	}
	gotBytes, err := utxo.SerializeUTXO(added, &out)
	if err != nil || !bytes.Equal(gotBytes, targetBytes) {
		t.Fatalf("serialized target differs: %v", err)
	}
	gotMS, err := applyDiffToMultiset(baseMS, got)
	if err != nil || !gotMS.Hash().Equal(targetMS.Hash()) {
		t.Fatalf("target commitment differs: %v", err)
	}
	// A new staging area per derivation cannot retain the previous call's work.
	again, againMethod, err := view.diff()
	if err != nil || againMethod != method || !again.Equal(got) {
		t.Fatalf("repeat changed output: %v", err)
	}
	index, err := view.manager.pruningStore.CurrentPruningPointIndex(snapshot, model.NewStagingArea())
	if err != nil || index != 1 {
		t.Fatalf("view index %d: %v", index, err)
	}
	if err = snapshot.Put(p.Key([]byte("accidental-write")), []byte{1}); err == nil {
		t.Fatal("read view accepted write")
	}
	if _, err = snapshot.Begin(); err == nil {
		t.Fatal("read view accepted transaction")
	}
}

// Canonical operation bytes are independent of Go map iteration order.
func canonicalDiff(t *testing.T, diff externalapi.UTXODiff) []string {
	t.Helper()
	var operations []string
	for kind, collection := range []externalapi.UTXOCollection{diff.ToRemove(), diff.ToAdd()} {
		iterator := collection.Iterator()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, err := iterator.Get()
			if err != nil {
				t.Fatal(err)
			}
			data, err := utxo.SerializeUTXO(entry, outpoint)
			if err != nil {
				t.Fatal(err)
			}
			operations = append(operations, string(append([]byte{byte(kind)}, data...)))
		}
		if err := iterator.Close(); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(operations)
	return operations
}
