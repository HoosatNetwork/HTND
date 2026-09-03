package utxoderive_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/processes/consensusstatemanager"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utxoderive"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/util/staging"
)

// buildFixture creates a small real DAG in its own datadir, then closes it so the derive path
// can open the same files. Using a real consensus rather than hand-rolled headers is the whole
// point: the commitments the replay is checked against are the ones this consensus actually
// produced, so a replay that reproduces them is reproducing real behaviour.
func buildFixture(t *testing.T, dataDir string, chainLength int) (*dagconfig.Params, []*externalapi.DomainHash) {
	t.Helper()

	consensusConfig := consensus.Config{Params: dagconfig.SimnetParams}
	consensusConfig.SkipProofOfWork = true
	consensusConfig.BlockCoinbaseMaturity = 0
	// Copy the slice before narrowing it: Params is copied by value but its slices are not, and
	// mutating the package-level dagconfig would leak into every other test in the process.
	windowSizes := make([]int, len(consensusConfig.DifficultyAdjustmentWindowSize))
	for i := range windowSizes {
		windowSizes[i] = 1
	}
	consensusConfig.DifficultyAdjustmentWindowSize = windowSizes

	// The consensus mutates a process-global block version during validation and building.
	// Pin it for the fixture and restore it, so this package cannot poison anything that runs
	// after it.
	previousBlockVersion := constants.GetBlockVersion()
	constants.ForceSetBlockVersion(1)
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previousBlockVersion)) })

	factory := consensus.NewFactory()
	factory.SetTestDataDir(dataDir)
	tc, teardown, err := factory.NewTestConsensus(&consensusConfig, "utxoderive")
	if err != nil {
		t.Fatalf("could not create test consensus: %+v", err)
	}

	hashes := []*externalapi.DomainHash{consensusConfig.GenesisHash}
	parent := consensusConfig.GenesisHash
	for i := 0; i < chainLength; i++ {
		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{parent}, nil, nil)
		if err != nil {
			teardown(false)
			t.Fatalf("could not add block %d: %+v", i, err)
		}
		hashes = append(hashes, blockHash)
		parent = blockHash
	}

	teardown(true) // closes the database, keeps the files
	return &consensusConfig.Params, hashes
}

func openDeriver(t *testing.T, dataDir string, params *dagconfig.Params, stopOnMismatch bool) (
	*utxoderive.Deriver, utxoderive.Stores, infrastructuredatabase.Database,
) {
	t.Helper()
	db, err := utxoderive.OpenLevelDB(dataDir, 8)
	if err != nil {
		t.Fatalf("could not open %s: %+v", dataDir, err)
	}
	stores, err := utxoderive.OpenStores(db, []byte{0}, 100, false)
	if err != nil {
		db.Close()
		t.Fatalf("could not open stores: %+v", err)
	}
	deriver, err := utxoderive.New(stores, params.GenesisHash, stopOnMismatch)
	if err != nil {
		db.Close()
		t.Fatalf("could not create deriver: %+v", err)
	}
	return deriver, stores, db
}

// tamperBlock replaces a block's stored body while keeping its hash key, simulating a datadir
// whose bodies no longer agree with the headers that commit to them.
func tamperBlock(t *testing.T, stores utxoderive.Stores, blockHash *externalapi.DomainHash,
	mutate func(*externalapi.DomainBlock),
) {
	t.Helper()
	stagingArea := model.NewStagingArea()
	block, err := stores.BlockStore.Block(stores.DatabaseContext, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("could not read block %s: %+v", blockHash, err)
	}
	mutate(block)

	writeArea := model.NewStagingArea()
	stores.BlockStore.Stage(writeArea, blockHash, block)
	if err := staging.CommitAllChanges(stores.DatabaseContext.(model.DBManager), writeArea); err != nil {
		t.Fatalf("could not write tampered block: %+v", err)
	}
}

// T2: a replay of a real tiny DAG reproduces every header commitment it walks past, starting
// from genesis's EmptyMuHash. If this fails, the walk order or the acceptance rule is wrong.
func TestDeriveMatchesFixtureCommitments(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, _, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	tip := hashes[len(hashes)-1]
	if err := deriver.Walk(tip, nil); err != nil {
		t.Fatalf("walk failed: %+v", err)
	}

	report := deriver.Report()
	if report.FirstMismatch != nil {
		t.Fatalf("replay diverged at %s: header %s, derived %s",
			report.FirstMismatch.PruningPoint, report.FirstMismatch.HeaderCommitment,
			report.FirstMismatch.DerivedMultiset)
	}
	if report.ChainBlocks != uint64(len(hashes)) {
		t.Errorf("walked %d chain blocks, want %d", report.ChainBlocks, len(hashes))
	}
	if report.TxsAccepted == 0 {
		t.Errorf("no transactions accepted - the replay applied nothing and would report success on an " +
			"empty walk, which is exactly what must never happen")
	}
}

// T3: a tampered output amount must surface as a commitment mismatch, and nothing may be
// persisted. The tampered block's own merge-set parent is what accepts it, so the mismatch
// appears at the following chain block.
func TestDeriveDetectsTamperedOutput(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	target := hashes[2]
	tamperBlock(t, stores, target, func(block *externalapi.DomainBlock) {
		block.Transactions[0].Outputs[0].Value += 1
	})

	err := deriver.Walk(hashes[len(hashes)-1], nil)
	report := deriver.Report()

	// Either the accepted-ID merkle check or the UTXO commitment catches it; both are
	// acceptable, but silence is not.
	if err == nil && report.FirstMismatch == nil {
		t.Fatalf("tampering an output amount produced neither an error nor a commitment mismatch")
	}
	if report.FirstMismatch != nil && report.FirstMismatch.Match {
		t.Errorf("FirstMismatch recorded as a match")
	}
}

// T4: a transaction spending an outpoint the replay has never seen must stop the walk and name
// the transaction. The live path turns this into "not accepted" and, when the offset flag is
// latched, skips it entirely - which is how outputs vanish. The replay must not inherit that.
func TestDeriveStopsOnMissingInput(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	// Give a block a second, non-coinbase transaction that spends an outpoint nobody created.
	target := hashes[3]
	var addedTransactionID string
	tamperBlock(t, stores, target, func(block *externalapi.DomainBlock) {
		phantom := externalapi.DomainOutpoint{
			TransactionID: externalapi.DomainTransactionID(*params.GenesisHash),
			Index:         9999,
		}
		spend := &externalapi.DomainTransaction{
			Version: 0,
			Inputs: []*externalapi.DomainTransactionInput{
				{PreviousOutpoint: phantom, SignatureScript: []byte{}, Sequence: 0},
			},
			Outputs: []*externalapi.DomainTransactionOutput{
				{Value: 1, ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}},
			},
		}
		block.Transactions = append(block.Transactions, spend)
		addedTransactionID = phantom.TransactionID.String()
	})

	err := deriver.Walk(hashes[len(hashes)-1], nil)
	if err == nil {
		t.Fatalf("a transaction spending a non-existent outpoint did not stop the replay")
	}
	if !strings.Contains(err.Error(), "not in the derived UTXO set") &&
		!strings.Contains(err.Error(), "acceptedIDMerkleRoot") {
		t.Fatalf("unexpected error for a missing input: %s", err)
	}
	if strings.Contains(err.Error(), "not in the derived UTXO set") &&
		!strings.Contains(err.Error(), addedTransactionID) {
		t.Errorf("missing-input error does not name the outpoint being spent: %s", err)
	}
	if deriver.Report().StopReason == "" {
		t.Errorf("report does not record why the walk stopped")
	}
}

// deleteBlockBody removes a block's body while leaving its header, which is exactly the shape
// a pruned datadir has - and the shape GetBlockEvenIfHeaderOnly hands back over the wire.
func deleteBlockBody(t *testing.T, stores utxoderive.Stores, blockHash *externalapi.DomainHash) {
	t.Helper()
	writeArea := model.NewStagingArea()
	stores.BlockStore.Delete(writeArea, blockHash)
	if err := staging.CommitAllChanges(stores.DatabaseContext.(model.DBManager), writeArea); err != nil {
		t.Fatalf("could not delete block body: %+v", err)
	}
}

// T1: a datadir whose bodies are gone must fail preflight, so a replay never starts and never
// reports success on an empty walk.
func TestPreflightRejectsMissingBody(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	// Preflight probes genesis and then the deepest block below the pruning point. On a tiny
	// fixture the pruning point is genesis, so genesis is what it loads.
	deleteBlockBody(t, stores, hashes[0])

	err := deriver.Preflight(utxoderive.DefaultProbeDepth)
	if err == nil {
		t.Fatalf("preflight passed on a datadir whose body is gone")
	}
	if !strings.Contains(err.Error(), "H3") {
		t.Fatalf("preflight failed without naming H3, so an operator cannot tell why: %s", err)
	}
}

// T1b: a body that is present but carries no transactions - a header-only block wearing a
// block's clothes - must be refused during the walk too, not only at preflight.
func TestWalkRejectsHeaderOnlyBody(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	tamperBlock(t, stores, hashes[2], func(block *externalapi.DomainBlock) {
		block.Transactions = nil
	})

	err := deriver.Walk(hashes[len(hashes)-1], nil)
	if err == nil {
		t.Fatalf("the walk accepted a non-genesis block with zero transactions")
	}
	if !strings.Contains(err.Error(), "zero transactions") && !strings.Contains(err.Error(), "H3") {
		t.Fatalf("unexpected error for a header-only body: %s", err)
	}
}

// T5: the wipe must remove every derived store and leave the inputs alone. A surviving
// pruning-point bucket would reintroduce the exported lineage the replay exists to escape.
func TestWipeRemovesDerivedStoresAndKeepsInputs(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 3)

	db, err := utxoderive.OpenLevelDB(dataDir, 8)
	if err != nil {
		t.Fatalf("could not open datadir: %+v", err)
	}
	defer db.Close()

	// Plant a served pruning-point bucket entry, as a real source datadir would have.
	prefixBytes := []byte{0}
	plantedKey := infrastructuredatabase.MakeBucket(prefixBytes).
		Bucket([]byte("pruning-point-utxo-set")).Key([]byte("planted"))
	if err := db.Put(plantedKey, []byte{1, 2, 3}); err != nil {
		t.Fatalf("could not plant a bucket entry: %+v", err)
	}
	if err := utxoderive.VerifyDerivedStoresAbsent(db, prefixBytes); err == nil {
		t.Fatalf("VerifyDerivedStoresAbsent passed while a planted pruning-point bucket was present")
	}

	if err := utxoderive.WipeDerivedStores(db, prefixBytes); err != nil {
		t.Fatalf("wipe failed: %+v", err)
	}
	if err := utxoderive.VerifyDerivedStoresAbsent(db, prefixBytes); err != nil {
		t.Fatalf("derived stores survived the wipe: %+v", err)
	}

	// Inputs must be untouched: blocks and GHOSTDAG still readable after the wipe.
	stores, err := utxoderive.OpenStores(db, prefixBytes, 100, false)
	if err != nil {
		t.Fatalf("could not reopen stores: %+v", err)
	}
	stagingArea := model.NewStagingArea()
	for _, blockHash := range hashes {
		if _, err := stores.BlockStore.Block(stores.DatabaseContext, stagingArea, blockHash); err != nil {
			t.Fatalf("block %s was lost by the wipe: %+v", blockHash, err)
		}
		if _, err := stores.GHOSTDAGDataStore.Get(stores.DatabaseContext, stagingArea, blockHash, false); err != nil {
			t.Fatalf("GHOSTDAG data for %s was lost by the wipe: %+v", blockHash, err)
		}
	}

	// And the replay still works against the wiped destination.
	deriver, err := utxoderive.New(stores, params.GenesisHash, true)
	if err != nil {
		t.Fatalf("could not create deriver: %+v", err)
	}
	if err := deriver.Walk(hashes[len(hashes)-1], nil); err != nil {
		t.Fatalf("walk failed after the wipe: %+v", err)
	}
	if deriver.Report().FirstMismatch != nil {
		t.Errorf("replay diverged after the wipe, so the wipe removed something it needed")
	}
}

// Guard against the fixture silently pointing at nothing.
func TestFixtureDataDirIsPopulated(t *testing.T) {
	dataDir := t.TempDir()
	buildFixture(t, dataDir, 2)

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("could not read %s: %+v", dataDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("fixture datadir %s is empty", filepath.Base(dataDir))
	}
}

// buildOrderSensitiveFixture builds a chain whose last chain block accepts several transactions
// in an order that is NOT sorted by transaction ID.
//
// That property is what makes the accepted-ID merkle root sensitive to the block version: version
// 4 and below sort accepted transactions by ID before hashing, 5 and above do not. A fixture whose
// natural order already happens to be sorted would hash identically either way and could not
// detect a replay that threaded the wrong version.
//
// Returns the chain hashes and the hash of the block whose acceptance is order-sensitive.
func buildOrderSensitiveFixture(t *testing.T, dataDir string) (
	*dagconfig.Params, []*externalapi.DomainHash, *externalapi.DomainHash,
) {
	t.Helper()

	consensusConfig := consensus.Config{Params: dagconfig.SimnetParams}
	consensusConfig.SkipProofOfWork = true
	consensusConfig.BlockCoinbaseMaturity = 0
	windowSizes := make([]int, len(consensusConfig.DifficultyAdjustmentWindowSize))
	for i := range windowSizes {
		windowSizes[i] = 1
	}
	consensusConfig.DifficultyAdjustmentWindowSize = windowSizes

	previousBlockVersion := constants.GetBlockVersion()
	constants.ForceSetBlockVersion(1)
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previousBlockVersion)) })

	factory := consensus.NewFactory()
	factory.SetTestDataDir(dataDir)
	tc, teardown, err := factory.NewTestConsensus(&consensusConfig, "utxoderive-order")
	if err != nil {
		t.Fatalf("could not create test consensus: %+v", err)
	}
	keepDataDir := false
	defer func() { teardown(keepDataDir) }()

	scriptPublicKey, _ := testutils.OpTrueScript()
	coinbaseData := &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey}

	hashes := []*externalapi.DomainHash{consensusConfig.GenesisHash}
	parent := consensusConfig.GenesisHash
	addBlock := func(transactions []*externalapi.DomainTransaction) *externalapi.DomainHash {
		t.Helper()
		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{parent}, coinbaseData, transactions)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		hashes = append(hashes, blockHash)
		parent = blockHash
		return blockHash
	}

	// The first block after genesis merges only genesis, whose coinbase carries no reward, so its
	// own coinbase has no outputs and nothing to spend. Skip it and fund from the next three.
	addBlock(nil)
	fundingHashes := []*externalapi.DomainHash{addBlock(nil), addBlock(nil), addBlock(nil)}
	addBlock(nil) // accepts the third funding coinbase, making all three spendable

	spends := make([]*externalapi.DomainTransaction, 0, len(fundingHashes))
	for _, fundingHash := range fundingHashes {
		fundingBlock, _, err := tc.GetBlock(fundingHash)
		if err != nil {
			t.Fatalf("could not read funding block: %+v", err)
		}
		spend, err := testutils.CreateTransaction(fundingBlock.Transactions[0], 1)
		if err != nil {
			t.Fatalf("could not create spending transaction: %+v", err)
		}
		spends = append(spends, spend)
	}

	// Hand them over in DESCENDING transaction-ID order. Sorting ascending therefore cannot be a
	// no-op, so the two block-version behaviours must produce different merkle roots.
	sort.Slice(spends, func(i, j int) bool {
		return consensushashing.TransactionID(spends[j]).Less(consensushashing.TransactionID(spends[i]))
	})

	orderSensitiveBlock := addBlock(spends)
	addBlock(nil) // this block is the one that ACCEPTS the multi-transaction block above

	keepDataDir = true
	return &consensusConfig.Params, hashes, orderSensitiveBlock
}

// acceptanceDataOf reconstructs the acceptance data a chain block's child would build for it:
// one entry, every transaction accepted. Only valid for a linear chain where the block is its
// child's selected parent, which is how the fixture is built.
func acceptanceDataOf(t *testing.T, stores utxoderive.Stores,
	blockHash *externalapi.DomainHash,
) externalapi.AcceptanceData {
	t.Helper()
	block, err := stores.BlockStore.Block(stores.DatabaseContext, model.NewStagingArea(), blockHash)
	if err != nil {
		t.Fatalf("could not read block %s: %+v", blockHash, err)
	}
	transactionAcceptanceData := make([]*externalapi.TransactionAcceptanceData, len(block.Transactions))
	for i, transaction := range block.Transactions {
		transactionAcceptanceData[i] = &externalapi.TransactionAcceptanceData{
			Transaction: transaction,
			IsAccepted:  true,
		}
	}
	return externalapi.AcceptanceData{
		{BlockHash: blockHash, TransactionAcceptanceData: transactionAcceptanceData},
	}
}

// TestDeriveUsesHeaderVersionNotAmbient is the guard for the ambient-version leak.
//
// The fixture's blocks are version 1, which sorts accepted transactions by ID. The walk then runs
// with the process-global block version forced to 9, which does not sort. If the walk threaded
// constants.GetBlockVersion() instead of each block's own header version, it would hash the
// accepted transactions in the wrong order and the accepted-ID merkle check would fail.
func TestDeriveUsesHeaderVersionNotAmbient(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes, orderSensitiveBlock := buildOrderSensitiveFixture(t, dataDir)

	deriver, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	// Precondition: this fixture really is order-sensitive. Without it the test could pass
	// while threading the ambient version, which is exactly the bug it exists to catch.
	acceptanceData := acceptanceDataOf(t, stores, orderSensitiveBlock)
	sorted := consensusstatemanager.CalculateAcceptedIDMerkleRoot(acceptanceData, 1)
	unsorted := consensusstatemanager.CalculateAcceptedIDMerkleRoot(acceptanceData, 9)
	if sorted.Equal(unsorted) {
		t.Fatalf("fixture is not order-sensitive: version 1 and version 9 hash %s identically, so this "+
			"test cannot detect a walk that threads the ambient version", orderSensitiveBlock)
	}

	// Now make the ambient version disagree with every header in the fixture.
	constants.ForceSetBlockVersion(9)
	if got := constants.GetBlockVersion(); got != 9 {
		t.Fatalf("could not raise the ambient block version, got %d", got)
	}

	if err := deriver.Walk(hashes[len(hashes)-1], nil); err != nil {
		t.Fatalf("walk failed with the ambient block version at 9 while the fixture is version 1. "+
			"The walk is threading the ambient version somewhere instead of each block's own "+
			"header version: %+v", err)
	}
	if report := deriver.Report(); report.FirstMismatch != nil {
		t.Fatalf("commitment mismatch at %s with the ambient version raised: header %s, derived %s",
			report.FirstMismatch.PruningPoint, report.FirstMismatch.HeaderCommitment,
			report.FirstMismatch.DerivedMultiset)
	}
}

// TestFixtureDAAScoresDifferFromSelectedParent is what gives TestDeriveMatchesFixtureCommitments
// its coverage of DAA threading.
//
// The walk stamps UTXO entries with the MERGE-SET block's own DAA score, not the chain block's.
// On a linear chain the merge set is exactly the selected parent, so if those two DAA scores
// differ, a walk that stamped the wrong one would serialize entries differently and the MuHash
// would miss. Genesis and the block directly above it both sit at DAA 0, so that one pair cannot
// discriminate; every pair above it must.
func TestFixtureDAAScoresDifferFromSelectedParent(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	_, stores, db := openDeriver(t, dataDir, params, true)
	defer db.Close()

	stagingArea := model.NewStagingArea()
	daaScoreOf := func(blockHash *externalapi.DomainHash) uint64 {
		t.Helper()
		header, err := stores.BlockHeaderStore.BlockHeader(stores.DatabaseContext, stagingArea, blockHash)
		if err != nil {
			t.Fatalf("could not read header for %s: %+v", blockHash, err)
		}
		return header.DAAScore()
	}

	discriminating := 0
	// hashes[0] is genesis and hashes[1] shares its DAA score of 0; start the comparison above it.
	for i := 2; i < len(hashes); i++ {
		childDAAScore := daaScoreOf(hashes[i])
		parentDAAScore := daaScoreOf(hashes[i-1])
		if childDAAScore == parentDAAScore {
			t.Errorf("chain block %s and its selected parent %s share DAA score %d, so a walk that "+
				"stamped the chain block's score instead of the merge-set block's would still match here",
				hashes[i], hashes[i-1], childDAAScore)
			continue
		}
		discriminating++
	}

	if discriminating == 0 {
		t.Fatalf("no chain block in the fixture has a DAA score distinct from its selected parent, so " +
			"the commitment test cannot detect wrong DAA threading at all")
	}
}

// TestMismatchesAreRecordedWhenContinuing covers --stop-on-mismatch=false.
//
// Walking past the first break is only useful if every break is recorded, so each mismatch must
// carry both commitment pairs and say which check failed. Without that the operator gets a longer
// walk and no more information than stopping would have given.
func TestMismatchesAreRecordedWhenContinuing(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, false /* keep walking */)
	defer db.Close()

	tamperBlock(t, stores, hashes[2], func(block *externalapi.DomainBlock) {
		block.Transactions[0].Outputs[0].Value += 1
	})

	// A tampered body may or may not still resolve every later input; either outcome is fine, the
	// point is what the report holds.
	_ = deriver.Walk(hashes[len(hashes)-1], nil)
	report := deriver.Report()

	if len(report.Mismatches) == 0 {
		t.Fatalf("walking with stop-on-mismatch disabled recorded no mismatches at all")
	}
	if report.FirstMismatch == nil {
		t.Fatalf("FirstMismatch is nil even though %d mismatches were recorded", len(report.Mismatches))
	}

	for i, mismatch := range report.Mismatches {
		if mismatch.Match {
			t.Errorf("mismatch %d is flagged as a match", i)
		}
		if mismatch.FailedChecks == "" {
			t.Errorf("mismatch %d does not say which check failed", i)
		}
		switch mismatch.FailedChecks {
		case "utxo", "accepted-id", "both":
		default:
			t.Errorf("mismatch %d has an unknown FailedChecks value %q", i, mismatch.FailedChecks)
		}
		for name, hash := range map[string]*externalapi.DomainHash{
			"DerivedMultiset":             mismatch.DerivedMultiset,
			"HeaderCommitment":            mismatch.HeaderCommitment,
			"DerivedAcceptedIDMerkleRoot": mismatch.DerivedAcceptedIDMerkleRoot,
			"HeaderAcceptedIDMerkleRoot":  mismatch.HeaderAcceptedIDMerkleRoot,
		} {
			if hash == nil {
				t.Errorf("mismatch %d is missing %s, so the record cannot be acted on", i, name)
			}
		}
	}

	// The first recorded mismatch must be the one FirstMismatch points at.
	if report.FirstMismatch.PruningPoint == nil ||
		!report.FirstMismatch.PruningPoint.Equal(report.Mismatches[0].PruningPoint) {
		t.Errorf("FirstMismatch (%s) is not the first recorded mismatch (%s)",
			report.FirstMismatch.PruningPoint, report.Mismatches[0].PruningPoint)
	}
}

// TestStopOnMismatchStopsAtTheFirstOne pins the default: one record, and the walk ends there.
func TestStopOnMismatchStopsAtTheFirstOne(t *testing.T) {
	dataDir := t.TempDir()
	params, hashes := buildFixture(t, dataDir, 6)

	deriver, stores, db := openDeriver(t, dataDir, params, true /* default */)
	defer db.Close()

	tamperBlock(t, stores, hashes[2], func(block *externalapi.DomainBlock) {
		block.Transactions[0].Outputs[0].Value += 1
	})

	_ = deriver.Walk(hashes[len(hashes)-1], nil)
	report := deriver.Report()

	if len(report.Mismatches) != 1 {
		t.Fatalf("stop-on-mismatch recorded %d mismatches, want exactly 1", len(report.Mismatches))
	}
	if report.StopReason == "" {
		t.Errorf("report does not say why the walk stopped")
	}
	if !strings.Contains(report.StopReason, report.Mismatches[0].FailedChecks) {
		t.Errorf("StopReason %q does not name the failed check %q",
			report.StopReason, report.Mismatches[0].FailedChecks)
	}
}
