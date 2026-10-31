package consensusstatemanager_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/consensusstatemanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// The tests in this file pin the invariant updateSelectedTipUTXODiff relies on: exactly one block, virtual's selected
// parent, has a UTXO diff with no diff child, and that diff is relative to virtual's UTXO set. Every other block's past
// is restored by composing diffs along its diff children up to that root. When virtual moved onto a block whose diff
// still had a diff child, that block's diff was read as relative to virtual and the previous selected parent kept no
// diff child: a second, stale root. Every block whose diff chain ended there restored a wrong past, and the virtual UTXO
// set itself took the error in.
//
// After every step the harness checks:
//   - exactly one block with a stored UTXO diff has no diff child, and it is virtual's selected parent;
//   - for every block with a stored UTXO diff, restorePastUTXO equals the past rebuilt from genesis by replaying the
//     stored acceptance data of its selected-parent chain (the rule applyMergeSetBlocks uses).

type staleRootHarness struct {
	t       *testing.T
	tc      testapi.TestConsensus
	builder testapi.TestConsensus
	genesis *externalapi.DomainHash
	opTrue  *externalapi.ScriptPublicKey
	redeem  []byte
	count   int
	blocks  map[externalapi.DomainHash]*externalapi.DomainBlock
}

func newStaleRootHarness(t *testing.T, name string) *staleRootHarness {
	t.Helper()
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1} // every block past genesis is version 6, as on the live networks
	factory := consensus.NewFactory()
	newConsensus := func(name string) testapi.TestConsensus {
		config := &consensus.Config{Params: params}
		config.SkipProofOfWork = true
		config.BlockCoinbaseMaturity = 0
		tc, teardown, err := factory.NewTestConsensus(config, name)
		if err != nil {
			t.Fatalf("Error setting up consensus %s: %+v", name, err)
		}
		t.Cleanup(func() { teardown(false) })
		return tc
	}
	h := &staleRootHarness{
		t:       t,
		tc:      newConsensus(name),
		builder: newConsensus(name + "_builder"),
		genesis: params.GenesisHash,
		blocks:  make(map[externalapi.DomainHash]*externalapi.DomainBlock),
	}
	// NewTestConsensus resets the process-wide block version; pin it after the last consensus exists.
	constants.ForceSetBlockVersion(6)
	h.opTrue, h.redeem = testutils.OpTrueScript()
	return h
}

// build builds a block on parents and fails the test if it cannot. The builder never updates its virtual: it resolves
// the selected parent's chain in a throwaway staging area for every block it builds, so each block's coinbase and
// header follow from its own past, whatever the node under test does with it. overpay makes the coinbase pay one sompi
// more than the merge set earns, so the block is disqualified when it is resolved.
func (h *staleRootHarness) build(parents []*externalapi.DomainHash, transactions []*externalapi.DomainTransaction,
	overpay bool,
) *externalapi.DomainBlock {
	h.t.Helper()
	block, err := h.tryBuild(parents, transactions, overpay)
	if err != nil {
		h.t.Fatalf("Error building block %d on %v: %+v", h.count, parents, err)
	}
	return block
}

// tryBuild is build, but returns the error, for a block the builder refuses (a spend its past does not allow).
func (h *staleRootHarness) tryBuild(parents []*externalapi.DomainHash, transactions []*externalapi.DomainTransaction,
	overpay bool,
) (*externalapi.DomainBlock, error) {
	h.count++
	coinbaseData := &externalapi.DomainCoinbaseData{
		ScriptPublicKey: h.opTrue, ExtraData: []byte(fmt.Sprintf("block %d", h.count)),
	}
	block, _, err := h.builder.BuildBlockWithParents(parents, coinbaseData, transactions)
	if err != nil && strings.Contains(err.Error(), "DisqualifiedFromChain") {
		// The selected parent's chain is disqualified, so this block is disqualified by inheritance and its content
		// is never verified: build it as a UTXO-invalid block, without transactions. Only the coinbase subsidy is
		// checked on insertion, so it is copied from a parent's coinbase.
		block, err = h.builder.BuildUTXOInvalidBlock(parents)
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if parentBlock, ok := h.blocks[*parent]; ok {
				const subsidyStart, subsidyEnd = 8, 16
				copy(block.Transactions[0].Payload[subsidyStart:subsidyEnd],
					parentBlock.Transactions[0].Payload[subsidyStart:subsidyEnd])
				break
			}
		}
	} else if err != nil {
		return nil, err
	} else if overpay {
		coinbase := block.Transactions[0]
		if len(coinbase.Outputs) == 0 {
			coinbase.Outputs = append(coinbase.Outputs,
				&externalapi.DomainTransactionOutput{ScriptPublicKey: h.opTrue, Value: 1})
		} else {
			coinbase.Outputs[0].Value++
		}
	}
	header := block.Header.ToMutable()
	header.SetHashMerkleRoot(merkle.CalculateHashMerkleRoot(block.Transactions))
	block.Header = header.ToImmutable()
	err = h.builder.ValidateAndInsertBlock(block, false, true)
	if err != nil {
		return nil, err
	}
	h.blocks[*consensushashing.BlockHash(block)] = block
	return block, nil
}

func (h *staleRootHarness) insert(block *externalapi.DomainBlock, updateVirtual bool) *externalapi.DomainHash {
	h.t.Helper()
	err := h.tc.ValidateAndInsertBlock(block, updateVirtual, true)
	if err != nil {
		h.t.Fatalf("Error inserting block %s (updateVirtual=%t): %+v", consensushashing.BlockHash(block),
			updateVirtual, err)
	}
	return consensushashing.BlockHash(block)
}

// chain builds and inserts length blocks, each on the previous one, and returns their hashes.
func (h *staleRootHarness) chain(parent *externalapi.DomainHash, length int, updateVirtual bool) []*externalapi.DomainHash {
	h.t.Helper()
	hashes := make([]*externalapi.DomainHash, 0, length)
	for range length {
		parent = h.insert(h.build([]*externalapi.DomainHash{parent}, nil, false), updateVirtual)
		hashes = append(hashes, parent)
	}
	return hashes
}

// coinOf returns the coinbase of block and the index of its first output that pays the anyone-can-spend script.
func (h *staleRootHarness) coinOf(block *externalapi.DomainHash) (*externalapi.DomainTransaction, uint32) {
	h.t.Helper()
	transaction, index := h.coinOfOrNil(block)
	if transaction == nil {
		h.t.Fatalf("the coinbase of %s pays nothing to the anyone-can-spend script", block)
	}
	return transaction, index
}

func (h *staleRootHarness) coinOfOrNil(block *externalapi.DomainHash) (*externalapi.DomainTransaction, uint32) {
	coinbase := h.blocks[*block].Transactions[transactionhelper.CoinbaseTransactionIndex]
	for i, output := range coinbase.Outputs {
		if output.ScriptPublicKey.Equal(h.opTrue) {
			return coinbase, uint32(i)
		}
	}
	return nil, 0
}

func (h *staleRootHarness) spend(transaction *externalapi.DomainTransaction, index uint32) *externalapi.DomainTransaction {
	h.t.Helper()
	signatureScript, err := txscript.PayToScriptHashSignatureScript(h.redeem, nil)
	if err != nil {
		h.t.Fatalf("Error building the signature script: %+v", err)
	}
	return &externalapi.DomainTransaction{
		Version: constants.MaxTransactionVersion,
		Inputs: []*externalapi.DomainTransactionInput{{
			PreviousOutpoint: externalapi.DomainOutpoint{
				TransactionID: *consensushashing.TransactionID(transaction), Index: index,
			},
			SignatureScript: signatureScript,
			Sequence:        constants.MaxTxInSequenceNum,
		}},
		Outputs: []*externalapi.DomainTransactionOutput{{
			ScriptPublicKey: h.opTrue, Value: transaction.Outputs[index].Value - 1000,
		}},
		Payload: []byte{},
	}
}

func (h *staleRootHarness) resolve(maxBlocksToResolve uint64) bool {
	h.t.Helper()
	_, isCompletelyResolved, err := h.tc.ResolveVirtualWithMaxParam(maxBlocksToResolve)
	if err != nil {
		h.t.Fatalf("ResolveVirtual(%d): %+v", maxBlocksToResolve, err)
	}
	return isCompletelyResolved
}

func (h *staleRootHarness) hasDiffChild(block *externalapi.DomainHash) bool {
	h.t.Helper()
	hasChild, err := h.tc.UTXODiffStore().HasUTXODiffChild(h.tc.DatabaseContext(), model.NewStagingArea(), block)
	if err != nil {
		h.t.Fatalf("Error reading the diff child of %s: %+v", block, err)
	}
	return hasChild
}

// replayedPast rebuilds block's past UTXO set from genesis (which holds nothing spendable) by applying the stored
// acceptance data of every block on its selected-parent chain, oldest first. Outputs are stamped with the DAA score of
// the chain block that accepted them, as applyMergeSetBlocks stamps them.
func (h *staleRootHarness) replayedPast(block *externalapi.DomainHash) map[externalapi.DomainOutpoint]externalapi.UTXOEntry {
	h.t.Helper()
	stagingArea := model.NewStagingArea()
	var chain []*externalapi.DomainHash
	for current := block; !current.Equal(h.genesis); {
		chain = append(chain, current)
		ghostdagData, err := h.tc.GHOSTDAGDataStore().Get(h.tc.DatabaseContext(), stagingArea, current, false)
		if err != nil {
			h.t.Fatalf("Error getting the GHOSTDAG data of %s: %+v", current, err)
		}
		current = ghostdagData.SelectedParent()
	}
	past := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	for i := len(chain) - 1; i >= 0; i-- {
		acceptanceData, err := h.tc.AcceptanceDataStore().Get(h.tc.DatabaseContext(), stagingArea, chain[i])
		if err != nil {
			h.t.Fatalf("Error getting the acceptance data of %s: %+v", chain[i], err)
		}
		header, err := h.tc.BlockHeaderStore().BlockHeader(h.tc.DatabaseContext(), stagingArea, chain[i])
		if err != nil {
			h.t.Fatalf("Error getting the header of %s: %+v", chain[i], err)
		}
		for _, blockAcceptanceData := range acceptanceData {
			for _, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
				if !transactionAcceptanceData.IsAccepted {
					continue
				}
				transaction := transactionAcceptanceData.Transaction
				for _, input := range transaction.Inputs {
					delete(past, input.PreviousOutpoint)
				}
				transactionID := consensushashing.TransactionID(transaction)
				for index, output := range transaction.Outputs {
					past[externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(index)}] =
						utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, transactionhelper.IsCoinBase(transaction),
							header.DAAScore())
				}
			}
		}
	}
	return past
}

// restoredPast is block's past UTXO set as the node restores it: virtual's UTXO set with restorePastUTXO's diff applied.
func (h *staleRootHarness) restoredPast(block *externalapi.DomainHash,
	virtualSet map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
) map[externalapi.DomainOutpoint]externalapi.UTXOEntry {
	h.t.Helper()
	diff, err := consensusstatemanager.RestorePastUTXO(h.tc.ConsensusStateManager(), block)
	if err != nil {
		h.t.Fatalf("Error restoring the past UTXO of %s: %+v", block, err)
	}
	past := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, len(virtualSet))
	for outpoint, entry := range virtualSet {
		past[outpoint] = entry
	}
	for _, collection := range []struct {
		entries externalapi.UTXOCollection
		add     bool
	}{{diff.ToRemove(), false}, {diff.ToAdd(), true}} {
		iterator := collection.entries.Iterator()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, err := iterator.Get()
			if err != nil {
				h.t.Fatalf("Error iterating the restored diff of %s: %+v", block, err)
			}
			if collection.add {
				past[*outpoint] = entry
			} else {
				delete(past, *outpoint)
			}
		}
		_ = iterator.Close()
	}
	return past
}

// check asserts the two invariants and stops the test at the first step that breaks one, since every later step
// builds on the broken state.
func (h *staleRootHarness) check(step string) {
	h.t.Helper()
	stagingArea := model.NewStagingArea()
	selectedParent := virtualSelectedParent(h.t, h.tc)
	virtualSet := virtualUTXOSet(h.t, h.tc)

	iterator, err := h.tc.BlockStore().AllBlockHashesIterator(h.tc.DatabaseContext())
	if err != nil {
		h.t.Fatalf("Error iterating the blocks: %+v", err)
	}
	defer iterator.Close()
	var roots []*externalapi.DomainHash
	failed := false
	for ok := iterator.First(); ok; ok = iterator.Next() {
		block, err := iterator.Get()
		if err != nil {
			h.t.Fatalf("Error reading a block hash: %+v", err)
		}
		_, err = h.tc.UTXODiffStore().UTXODiff(h.tc.DatabaseContext(), stagingArea, block)
		if database.IsNotFoundError(err) {
			continue // not resolved yet
		}
		if err != nil {
			h.t.Fatalf("Error reading the UTXO diff of %s: %+v", block, err)
		}
		if !h.hasDiffChild(block) {
			roots = append(roots, block)
		}

		restored := h.restoredPast(block, virtualSet)
		replayed := h.replayedPast(block)
		missing, extra, restamped := 0, 0, 0
		for outpoint, entry := range replayed {
			restoredEntry, ok := restored[outpoint]
			if !ok {
				missing++
			} else if !restoredEntry.Equal(entry) {
				restamped++
			}
		}
		for outpoint := range restored {
			if _, ok := replayed[outpoint]; !ok {
				extra++
			}
		}
		if missing+extra+restamped > 0 {
			failed = true
			h.t.Errorf("%s: the restored past of %s (%s) is not its acceptance replay: %d coins missing, %d extra, "+
				"%d with another entry", step, block, blockStatus(h.t, h.tc, block), missing, extra, restamped)
		}
	}
	if len(roots) != 1 || !roots[0].Equal(selectedParent) {
		failed = true
		h.t.Errorf("%s: %d blocks have a UTXO diff and no diff child %v; expected exactly virtual's selected parent %s",
			step, len(roots), roots, selectedParent)
	}
	if failed {
		h.t.FailNow()
	}
}

// TestResolveVirtualKeepsOneUTXODiffRootOverDisqualifiedSelectedParent is the original failing sequence. Virtual is on a
// heavy chain A; a lighter branch B is UTXO-valid and spends a coin A does not. A gets an invalid block and a valid child,
// left pending as IBD leaves them, and the chunked resolve disqualifies them one at a time, making each disqualified
// processing point virtual's selected parent. The next ResolveVirtual moves virtual onto B's tip, whose diff was
// relative to A, not to virtual. Before the fix B's diff was read as virtual-relative and the last disqualified
// processing point was left as a second root, so A's past lost the coin that only B spent and a block on A spending it
// was disqualified.
func TestResolveVirtualKeepsOneUTXODiffRootOverDisqualifiedSelectedParent(t *testing.T) {
	h := newStaleRootHarness(t, "TestStaleRootDisqualifiedSelectedParent")
	h.check("genesis")

	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	coinTransaction, coinIndex := h.coinOf(common[2]) // accepted by common[3], so in the past of both branches
	spendCoin := h.spend(coinTransaction, coinIndex)
	h.check("common history")

	bFirst := h.insert(h.build([]*externalapi.DomainHash{fork}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	bTip := h.chain(bFirst, 1, true)[0]
	h.check("branch B")
	aChain := h.chain(fork, 4, true)
	aTip := aChain[len(aChain)-1]
	h.check("chain A")
	if sp := virtualSelectedParent(t, h.tc); !sp.Equal(aTip) {
		t.Fatalf("virtual's selected parent is %s, expected A's tip %s", sp, aTip)
	}
	if status := blockStatus(t, h.tc, bTip); status != externalapi.StatusUTXOValid || !h.hasDiffChild(bTip) {
		t.Fatalf("B's tip must be UTXO-valid with a diff relative to another block: %s, diff child %t", status,
			h.hasDiffChild(bTip))
	}

	aBad := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), false)
	aOK := h.insert(h.build([]*externalapi.DomainHash{aBad}, nil, false), false)
	h.check("A's invalid block and its child pending")

	for step := 1; ; step++ {
		if step > 8 {
			t.Fatalf("ResolveVirtual did not finish in %d calls", step-1)
		}
		complete := h.resolve(1)
		sp := virtualSelectedParent(t, h.tc)
		t.Logf("ResolveVirtual(1) call %d: complete=%t, virtual selected parent %s (%s)", step, complete, sp,
			blockStatus(t, h.tc, sp))
		h.check(fmt.Sprintf("ResolveVirtual(1) call %d", step))
		if complete {
			break
		}
	}
	for hash, want := range map[*externalapi.DomainHash]externalapi.BlockStatus{
		aBad: externalapi.StatusDisqualifiedFromChain,
		aOK:  externalapi.StatusDisqualifiedFromChain,
	} {
		if got := blockStatus(t, h.tc, hash); got != want {
			t.Fatalf("status of %s is %s, expected %s", hash, got, want)
		}
	}

	aSpend := h.insert(h.build([]*externalapi.DomainHash{aTip}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	if status := blockStatus(t, h.tc, aSpend); status != externalapi.StatusUTXOValid {
		t.Fatalf("a block on A spending the coin only B spent is %s, expected %s", status,
			externalapi.StatusUTXOValid)
	}
	h.check("block on A spending the coin only B spent")
	if sp := virtualSelectedParent(t, h.tc); !sp.Equal(aSpend) {
		t.Fatalf("virtual's selected parent is %s, expected the new heaviest block %s", sp, aSpend)
	}
}

// TestBlockInsertionKeepsOneUTXODiffRootOverInteriorProcessingPoint is the block-insertion trigger: a block submitted
// with updateVirtual while the chunked resolve has left virtual on a valid interior processing point Q of a pending
// chain A, and B's tip has Q as its diff child. Inserting the block first drains the rest of the resolve
// (ValidateAndInsertBlock), which disqualifies A from an invalid block on and then moves virtual onto B's tip, whose
// diff is relative to Q.
func TestBlockInsertionKeepsOneUTXODiffRootOverInteriorProcessingPoint(t *testing.T) {
	h := newStaleRootHarness(t, "TestStaleRootInteriorProcessingPoint")

	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	coinTransaction, coinIndex := h.coinOf(common[2])
	spendCoin := h.spend(coinTransaction, coinIndex)
	bFirst := h.insert(h.build([]*externalapi.DomainHash{fork}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	bTip := h.chain(bFirst, 1, true)[0]
	h.check("common history and branch B")

	aChain := h.chain(fork, 5, false)
	aLastValid := aChain[len(aChain)-1]
	aBad := h.insert(h.build([]*externalapi.DomainHash{aLastValid}, nil, true), false)
	aAbove := h.insert(h.build([]*externalapi.DomainHash{aBad}, nil, false), false)
	h.check("chain A pending")

	if complete := h.resolve(3); complete {
		t.Fatalf("expected the first chunk to leave A partly resolved")
	}
	interior := virtualSelectedParent(t, h.tc)
	if blockStatus(t, h.tc, interior) != externalapi.StatusUTXOValid || interior.Equal(aLastValid) ||
		interior.Equal(bTip) {
		t.Fatalf("expected virtual on a valid interior block of A, got %s (%s)", interior,
			blockStatus(t, h.tc, interior))
	}
	t.Logf("virtual's selected parent is the interior processing point %s", interior)
	h.check("first chunk")

	h.insert(h.build([]*externalapi.DomainHash{aAbove}, nil, false), true)
	sp := virtualSelectedParent(t, h.tc)
	t.Logf("after the insertion: virtual selected parent %s (%s)", sp, blockStatus(t, h.tc, sp))
	if !sp.Equal(bTip) {
		t.Fatalf("expected virtual's selected parent to be B's tip %s, the only tip left, got %s", bTip, sp)
	}
	h.check("block inserted over the interior processing point")

	for step := 1; !h.resolve(3); step++ {
		if step > 8 {
			t.Fatalf("ResolveVirtual did not finish in %d calls", step)
		}
		h.check(fmt.Sprintf("ResolveVirtual(3) call %d", step))
	}
	h.check("resolved")

	aSpend := h.insert(h.build([]*externalapi.DomainHash{aLastValid}, []*externalapi.DomainTransaction{spendCoin}, false),
		true)
	if status := blockStatus(t, h.tc, aSpend); status != externalapi.StatusUTXOValid {
		t.Fatalf("a block on A spending the coin only B spent is %s, expected %s", status,
			externalapi.StatusUTXOValid)
	}
	h.check("block on A spending the coin only B spent")
}

// TestAddBlockKeepsOneUTXODiffRootWhenVirtualLeavesValidSelectedParent is the AddBlock path with a UTXO-valid previous
// selected parent and nothing pending, so neither findNextPendingTip nor ResolveVirtual is involved. Virtual is on A's
// tip, and B's tip is UTXO-valid with a diff relative to A. A child of A's tip arrives with updateVirtual and is
// disqualified. calculateNewTips keeps neither the disqualified block nor its parent as a tip, so pickVirtualParents
// moves virtual onto B's tip. Before the fix B's diff was read as virtual-relative and A's tip stayed a second root, so
// a block on A spending the coin only B spent was disqualified. A fix confined to ResolveVirtual leaves this path open:
// the guard in updateSelectedTipUTXODiff then refuses the block instead.
func TestAddBlockKeepsOneUTXODiffRootWhenVirtualLeavesValidSelectedParent(t *testing.T) {
	h := newStaleRootHarness(t, "TestStaleRootAddBlockValidSelectedParent")

	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	coinTransaction, coinIndex := h.coinOf(common[2])
	spendCoin := h.spend(coinTransaction, coinIndex)
	bFirst := h.insert(h.build([]*externalapi.DomainHash{fork}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	bTip := h.chain(bFirst, 1, true)[0]
	aChain := h.chain(fork, 4, true)
	aTip := aChain[len(aChain)-1]
	h.check("A and B resolved")
	if sp := virtualSelectedParent(t, h.tc); !sp.Equal(aTip) || !h.hasDiffChild(bTip) {
		t.Fatalf("expected virtual on A's tip %s and B's tip with a diff child, got %s, %t", aTip, sp,
			h.hasDiffChild(bTip))
	}
	if !h.resolve(0) {
		t.Fatalf("expected nothing pending")
	}

	disqualified := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), true)
	if status := blockStatus(t, h.tc, disqualified); status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("the overpaying child of A's tip is %s, expected %s", status, externalapi.StatusDisqualifiedFromChain)
	}
	sp := virtualSelectedParent(t, h.tc)
	t.Logf("after the disqualified block: virtual selected parent %s (%s), B's tip %s", sp, blockStatus(t, h.tc, sp),
		bTip)
	h.check("disqualified child of A's tip")

	aSpend := h.insert(h.build([]*externalapi.DomainHash{aTip}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	if status := blockStatus(t, h.tc, aSpend); status != externalapi.StatusUTXOValid {
		t.Fatalf("a block on A spending the coin only B spent is %s, expected %s", status,
			externalapi.StatusUTXOValid)
	}
	h.check("block on A spending the coin only B spent")
	if sp := virtualSelectedParent(t, h.tc); !sp.Equal(aSpend) {
		t.Fatalf("virtual's selected parent is %s, expected the new heaviest block %s", sp, aSpend)
	}
}

// TestResolveVirtualAfterDisqualifiedChunkCommittedWithoutVirtualUpdate pins that ResolveVirtual committing a
// disqualified chunk before its virtual update leaves a consistent state if the update never happens (an error or a
// crash in between). The chunk's blocks keep diffs relative to their selected parents, so the single root is still
// virtual's selected parent and every past restores correctly; the next ResolveVirtual and AddBlock carry on from there.
// This is why the chunk's commit is left before the virtual update.
func TestResolveVirtualAfterDisqualifiedChunkCommittedWithoutVirtualUpdate(t *testing.T) {
	h := newStaleRootHarness(t, "TestStaleRootChunkWithoutVirtualUpdate")

	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	coinTransaction, coinIndex := h.coinOf(common[2])
	spendCoin := h.spend(coinTransaction, coinIndex)
	bFirst := h.insert(h.build([]*externalapi.DomainHash{fork}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	h.chain(bFirst, 1, true)
	aChain := h.chain(fork, 4, true)
	aTip := aChain[len(aChain)-1]
	aBad := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), false)
	h.insert(h.build([]*externalapi.DomainHash{aBad}, nil, false), false)
	h.check("A's invalid block and its child pending")

	// What ResolveVirtual(1) commits for its first chunk, before the virtual update.
	stagingArea := model.NewStagingArea()
	status, err := h.tc.ConsensusStateManager().ResolveBlockStatusTest(stagingArea, aBad, false)
	if err != nil {
		t.Fatalf("Error resolving the chunk: %+v", err)
	}
	if status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("the chunk resolved to %s, expected %s", status, externalapi.StatusDisqualifiedFromChain)
	}
	err = staging.CommitAllChanges(h.tc.DatabaseContext(), stagingArea)
	if err != nil {
		t.Fatalf("Error committing the chunk: %+v", err)
	}
	if sp := virtualSelectedParent(t, h.tc); !sp.Equal(aTip) {
		t.Fatalf("virtual's selected parent moved to %s without a virtual update", sp)
	}
	h.check("disqualified chunk committed, virtual not updated")

	for step := 1; !h.resolve(1); step++ {
		if step > 8 {
			t.Fatalf("ResolveVirtual did not finish in %d calls", step)
		}
		h.check(fmt.Sprintf("ResolveVirtual(1) call %d", step))
	}
	h.check("resolved")

	aSpend := h.insert(h.build([]*externalapi.DomainHash{aTip}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	if status := blockStatus(t, h.tc, aSpend); status != externalapi.StatusUTXOValid {
		t.Fatalf("a block on A spending the coin only B spent is %s, expected %s", status,
			externalapi.StatusUTXOValid)
	}
	h.check("block on A spending the coin only B spent")
}

// TestUTXODiffRootInvariantOnRandomDAGs drives random DAGs through the paths that move virtual: blocks inserted with
// and without updateVirtual, occasional invalid blocks (and their disqualified descendants), occasional spends of
// coins that may already be spent on another branch, and ResolveVirtual with random chunk sizes. The invariants are
// checked after every step. The seeds are fixed so a failure reproduces; runtime is a few seconds.
func TestUTXODiffRootInvariantOnRandomDAGs(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			randomStaleRootScenario(t, seed, 50)
		})
	}
}

func randomStaleRootScenario(t *testing.T, seed int64, blockCount int) {
	random := rand.New(rand.NewSource(seed))
	h := newStaleRootHarness(t, fmt.Sprintf("TestStaleRootRandom_%d", seed))
	all := []*externalapi.DomainHash{h.genesis}
	coinbases := []*externalapi.DomainHash{}
	// Coverage counters, logged at the end: how often virtual left its own selected chain (a reorg, or a move onto
	// a lighter block), which is when the previous selected parent has to be re-pointed.
	sideMoves := 0
	selectedParent := virtualSelectedParent(t, h.tc)
	noteMove := func() {
		next := virtualSelectedParent(t, h.tc)
		onChain, err := h.tc.DAGTopologyManager().IsAncestorOf(model.NewStagingArea(), selectedParent, next)
		if err != nil {
			t.Fatalf("Error checking ancestry: %+v", err)
		}
		if !onChain && !next.Equal(selectedParent) {
			sideMoves++
		}
		selectedParent = next
	}

	for i := range blockCount {
		// Parents: one or two of the most recent blocks, so the DAG forks and merges but stays within merge depth.
		window := all[max(0, len(all)-6):]
		parents := []*externalapi.DomainHash{window[random.Intn(len(window))]}
		if candidate := window[random.Intn(len(window))]; random.Intn(3) == 0 && !candidate.Equal(parents[0]) {
			related, err := h.builder.DAGTopologyManager().IsAncestorOf(model.NewStagingArea(), parents[0], candidate)
			if err != nil {
				t.Fatalf("Error checking ancestry: %+v", err)
			}
			reverse, err := h.builder.DAGTopologyManager().IsAncestorOf(model.NewStagingArea(), candidate, parents[0])
			if err != nil {
				t.Fatalf("Error checking ancestry: %+v", err)
			}
			if !related && !reverse {
				parents = append(parents, candidate)
			}
		}

		var transactions []*externalapi.DomainTransaction
		// Spend an older coinbase coin: usually in the block's past, sometimes already spent on its branch (the block
		// is then disqualified) or only on another branch (the case the stale root lost).
		if len(coinbases) > 8 && random.Intn(5) == 0 {
			transaction, index := h.coinOfOrNil(coinbases[random.Intn(len(coinbases)-6)])
			if transaction != nil {
				transactions = append(transactions, h.spend(transaction, index))
			}
		}
		// No invalid block among the first few: one on genesis's only chain leaves no UTXO-valid tip, which this
		// node handles separately (FindHighestValidBlock) and which is not what this test is about.
		overpay := i >= 4 && random.Intn(15) == 0
		block, err := h.tryBuild(parents, transactions, overpay)
		if err != nil {
			transactions, overpay = nil, false
			block = h.build(parents, nil, false)
		}
		updateVirtual := random.Intn(3) == 0
		t.Logf("block %d on %v: updateVirtual=%t overpay=%t spends=%d", i, parents, updateVirtual, overpay,
			len(transactions))
		hash := h.insert(block, updateVirtual)
		t.Logf("block %d is %s (%s); virtual selected parent %s", i, hash, blockStatus(t, h.tc, hash),
			virtualSelectedParent(t, h.tc))
		noteMove()
		all = append(all, hash)
		if len(block.Transactions[0].Outputs) > 0 {
			coinbases = append(coinbases, hash)
		}
		h.check(fmt.Sprintf("seed %d block %d (updateVirtual=%t, overpay=%t, %d spends)", seed, i, updateVirtual,
			overpay, len(transactions)))

		if random.Intn(3) == 0 {
			chunk := []uint64{0, 1, 2, 3, 5}[random.Intn(5)]
			complete := h.resolve(chunk)
			t.Logf("ResolveVirtual(%d): complete=%t, virtual selected parent %s", chunk, complete,
				virtualSelectedParent(t, h.tc))
			noteMove()
			h.check(fmt.Sprintf("seed %d after block %d: ResolveVirtual(%d)", seed, i, chunk))
		}
	}
	for step := 1; !h.resolve(2); step++ {
		if step > 4*blockCount {
			t.Fatalf("seed %d: ResolveVirtual did not finish", seed)
		}
		h.check(fmt.Sprintf("seed %d final ResolveVirtual(2) call %d", seed, step))
		noteMove()
	}
	h.check(fmt.Sprintf("seed %d resolved", seed))
	noteMove()

	disqualified := 0
	for _, hash := range all {
		if blockStatus(t, h.tc, hash) == externalapi.StatusDisqualifiedFromChain {
			disqualified++
		}
	}
	t.Logf("seed %d: %d blocks, %d disqualified, virtual left its own selected chain %d times", seed, blockCount,
		disqualified, sideMoves)
}
