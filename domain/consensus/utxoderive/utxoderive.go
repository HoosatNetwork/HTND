// Package utxoderive rebuilds a UTXO set and its MuHash directly from block bodies,
// walking the selected-parent chain and applying each chain block's full merge set, and
// compares the result to every pruning point header's own UTXOCommitment.
//
// It exists because the network no longer agrees on UTXO state: two peers with genuinely
// different pruning-point imports reached the same pruning point and still disagreed by
// ~1.95M DAA of history. There is no snapshot left to adopt, so a header-matching set has
// to be derived rather than fetched.
//
// What this package deliberately does NOT do:
//
//   - It does not touch the diff algebra. The whole point is to bypass
//     utxoDiffStore/withDiffInPlace, which is where the corruption lives, and materialise
//     the set that calculateMultiset's hash has always implied but nothing ever wrote down.
//   - It does not replay stored acceptance data. That data was produced by the
//     skip-missing-input and swallow-error paths under investigation, so replaying it would
//     faithfully reproduce the contamination. Acceptance is re-derived from bodies.
//   - It does not recompute topology. Parents, GHOSTDAG and DAA scores are inputs, read
//     from the source datadir. GhostDAG was never the fault, and re-deriving it would make
//     this a different (and much larger) product.
//   - It never opens a network socket. See Preflight: the P2P layer cannot serve pre-pruning
//     point bodies to a requester that asks for them, so the input must already be on disk.
package utxoderive

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

// Stores is the read side of a source datadir. Every field is an input: nothing here is
// derived, and nothing here is written by this package.
//
// Note what is absent - consensusStateStore, pruningStore's UTXO bucket, utxoDiffStore,
// multisetStore and the utxo index. Those are exactly the stores whose contents are under
// suspicion, and a Deriver that could read them would be tempted to trust them.
type Stores struct {
	DatabaseContext   model.DBReader
	BlockStore        model.BlockStore
	BlockHeaderStore  model.BlockHeaderStore
	GHOSTDAGDataStore model.GHOSTDAGDataStore
	DAABlocksStore    model.DAABlocksStore
	PruningStore      model.PruningStore
}

// Deriver performs one replay over one source datadir.
type Deriver struct {
	stores      Stores
	genesisHash *externalapi.DomainHash

	// utxos is the derived set: the deliverable. Kept alongside the MuHash so that the two
	// are driven by the same accepted-transaction stream and cannot drift from one another -
	// which is precisely the guarantee the served pruning-point bucket never had.
	utxos map[externalapi.DomainOutpoint]externalapi.UTXOEntry
	ms    model.Multiset

	stopOnMismatch bool
	report         *Report
}

// New creates a Deriver over the given source stores.
func New(stores Stores, genesisHash *externalapi.DomainHash, stopOnMismatch bool) (*Deriver, error) {
	if stores.DatabaseContext == nil || stores.BlockStore == nil || stores.BlockHeaderStore == nil ||
		stores.GHOSTDAGDataStore == nil || stores.DAABlocksStore == nil || stores.PruningStore == nil {
		return nil, errors.Errorf("utxoderive: every source store is required")
	}
	if genesisHash == nil {
		return nil, errors.Errorf("utxoderive: genesis hash is required")
	}
	return &Deriver{
		stores:         stores,
		genesisHash:    genesisHash,
		utxos:          make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry),
		ms:             multiset.New(),
		stopOnMismatch: stopOnMismatch,
		report:         &Report{},
	}, nil
}

// Report is the outcome of a replay. Checkpoints is the running pruning-point comparison;
// FirstMismatch is the mandatory output - the corruption horizon - and is set even when the
// walk is allowed to continue past it.
type Report struct {
	Checkpoints    []Checkpoint
	FirstMismatch  *Checkpoint
	BlocksApplied  uint64
	ChainBlocks    uint64
	TxsAccepted    uint64
	DerivedSum     uint64
	DerivedEntries uint64
	StoppedAt      *externalapi.DomainHash
	StopReason     string

	// Mismatches holds every block that failed either check, in walk order. With
	// stop-on-mismatch enabled it has at most one entry; the point of disabling that flag is to
	// fill this in.
	Mismatches []Checkpoint

	// AcceptanceDiverged is set once the replay and the network disagree about which
	// transactions a block accepted. Past that point the derived set is not merely wrong, it is
	// meaningless - so nothing may be persisted from this run even if later blocks appear to
	// match again.
	AcceptanceDiverged bool
}

// Checkpoint is one block's comparison: what the header committed to versus what replaying every
// body from genesis actually produces, for both commitments the block carries.
//
// Both are recorded because they fail for different reasons and the difference matters. A UTXO
// commitment miss with a matching accepted-ID merkle root means the replay agreed on WHICH
// transactions were accepted but not on the resulting set - a UTXO accounting fault. An
// accepted-ID miss means the replay disagreed about acceptance itself, and everything derived
// after it is meaningless rather than merely wrong.
type Checkpoint struct {
	PruningPoint     *externalapi.DomainHash
	DAAScore         uint64
	DerivedMultiset  *externalapi.DomainHash
	HeaderCommitment *externalapi.DomainHash

	DerivedAcceptedIDMerkleRoot *externalapi.DomainHash
	HeaderAcceptedIDMerkleRoot  *externalapi.DomainHash

	// FailedChecks is "utxo", "accepted-id", "both", or "" when the block matched.
	FailedChecks string
	Match        bool
}

// Multiset returns the derived MuHash at the current point of the walk.
func (d *Deriver) Multiset() model.Multiset { return d.ms }

// Report returns the running report.
func (d *Deriver) Report() *Report { return d.report }

// UTXOs exposes the derived set. Only meaningful after a walk that reached its target with
// a matching commitment - a set derived past an unresolved mismatch must never be served.
func (d *Deriver) UTXOs() map[externalapi.DomainOutpoint]externalapi.UTXOEntry { return d.utxos }

// selectedParentChain returns the selected-parent chain from genesis (inclusive) up to
// highHash (inclusive), walking down via stored GHOSTDAG data and reversing.
//
// Walking down rather than up is deliberate: it needs only ghostdagDataStore, so a datadir
// whose selected-chain index is absent or stale cannot silently steer the replay.
func (d *Deriver) selectedParentChain(highHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	stagingArea := model.NewStagingArea()
	var reversed []*externalapi.DomainHash
	current := highHash
	for {
		reversed = append(reversed, current)
		if current.Equal(d.genesisHash) {
			break
		}
		ghostdagData, err := d.stores.GHOSTDAGDataStore.Get(d.stores.DatabaseContext, stagingArea, current, false)
		if err != nil {
			return nil, errors.Wrapf(err, "utxoderive: no stored GHOSTDAG data for %s. This datadir cannot "+
				"support a replay: GHOSTDAG is an input, and deriving it here would make this a "+
				"topology re-validation rather than a UTXO replay", current)
		}
		selectedParent := ghostdagData.SelectedParent()
		if selectedParent == nil || selectedParent.Equal(model.VirtualGenesisBlockHash) {
			break
		}
		current = selectedParent
	}

	chain := make([]*externalapi.DomainHash, len(reversed))
	for i, hash := range reversed {
		chain[len(reversed)-1-i] = hash
	}
	return chain, nil
}

// sortedMergeSet reproduces ghostdagManager.GetSortedMergeSet from stored GHOSTDAG data
// alone - selected parent first, then blues and reds interleaved by (blueWork, hash).
//
// Reproduced rather than called because the real one hangs off a ghostdagManager that needs
// topology and traversal managers this package deliberately does not construct. It reads
// only what GetSortedMergeSet reads, and the ordering must match exactly: acceptance is
// order-dependent, since a later transaction in the same merge set may spend an earlier
// one's output.
func (d *Deriver) sortedMergeSet(blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	stagingArea := model.NewStagingArea()
	ghostdagData, err := d.stores.GHOSTDAGDataStore.Get(d.stores.DatabaseContext, stagingArea, blockHash, false)
	if err != nil {
		return nil, errors.Wrapf(err, "utxoderive: no stored GHOSTDAG data for %s", blockHash)
	}

	blues := ghostdagData.MergeSetBlues()
	reds := ghostdagData.MergeSetReds()
	sorted := make([]*externalapi.DomainHash, 0, len(blues)+len(reds))
	if len(blues) == 0 {
		return sorted, nil
	}

	selectedParent := ghostdagData.SelectedParent()
	filteredBlues := make([]*externalapi.DomainHash, 0, len(blues))
	for _, hash := range blues {
		if !hash.Equal(selectedParent) {
			filteredBlues = append(filteredBlues, hash)
		}
	}
	sorted = append(sorted, selectedParent)

	less := func(hashA *externalapi.DomainHash, dataA *externalapi.BlockGHOSTDAGData,
		hashB *externalapi.DomainHash, dataB *externalapi.BlockGHOSTDAGData,
	) bool {
		switch dataA.BlueWork().Cmp(dataB.BlueWork()) {
		case -1:
			return true
		case 1:
			return false
		default:
			return hashA.Less(hashB)
		}
	}

	i, j := 0, 0
	for i < len(filteredBlues) && j < len(reds) {
		blueData, err := d.stores.GHOSTDAGDataStore.Get(d.stores.DatabaseContext, stagingArea, filteredBlues[i], false)
		if err != nil {
			return nil, err
		}
		redData, err := d.stores.GHOSTDAGDataStore.Get(d.stores.DatabaseContext, stagingArea, reds[j], false)
		if err != nil {
			return nil, err
		}
		if less(filteredBlues[i], blueData, reds[j], redData) {
			sorted = append(sorted, filteredBlues[i])
			i++
		} else {
			sorted = append(sorted, reds[j])
			j++
		}
	}
	sorted = append(sorted, filteredBlues[i:]...)
	sorted = append(sorted, reds[j:]...)
	return sorted, nil
}

// blockOwnDAAScore returns the DAA score stamped into the UTXO entries a block creates,
// preferring the header exactly as consensusStateManager.blockOwnDAAScore does.
func (d *Deriver) blockOwnDAAScore(blockHash *externalapi.DomainHash) (uint64, error) {
	stagingArea := model.NewStagingArea()
	header, err := d.stores.BlockHeaderStore.BlockHeader(d.stores.DatabaseContext, stagingArea, blockHash)
	if err == nil {
		return header.DAAScore(), nil
	}
	daaScore, daaErr := d.stores.DAABlocksStore.DAAScore(d.stores.DatabaseContext, stagingArea, blockHash)
	if daaErr != nil {
		return 0, errors.Wrapf(daaErr, "utxoderive: no DAA score for %s", blockHash)
	}
	return daaScore, nil
}

// loadBodyStrict loads a block and refuses a body-less one.
//
// This is the H3 guard. Asking a pruned peer for a pre-pruning-point body does not error:
// HandleIBDBlockRequests falls back to GetBlockEvenIfHeaderOnly, which returns a
// DomainBlock carrying only a Header and no Transactions. A datadir populated that way,
// or simply pruned, would let a naive replay walk an empty chain and report a confident
// wrong answer. Every non-genesis block must carry at least a coinbase.
func (d *Deriver) loadBodyStrict(blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	stagingArea := model.NewStagingArea()
	block, err := d.stores.BlockStore.Block(d.stores.DatabaseContext, stagingArea, blockHash)
	if err != nil {
		return nil, errors.Wrapf(err, "utxoderive: block body for %s is missing. A replay cannot "+
			"reconstruct it - the P2P layer will not serve bodies below the pruning point, and a pruned "+
			"peer answers such a request with a header-only block rather than an error (H3)", blockHash)
	}
	if len(block.Transactions) == 0 && !blockHash.Equal(d.genesisHash) {
		return nil, errors.Errorf("utxoderive: block %s loaded with zero transactions. This is a "+
			"header-only block masquerading as a body (H3); replaying it would silently contribute "+
			"nothing and produce a wrong answer", blockHash)
	}
	return block, nil
}

// applyTransaction folds one accepted transaction into both the derived set and the MuHash.
// Inputs are removed, outputs added, stamped with the creating block's own DAA score - the
// same value applyMergeSetBlocks stamps and addTransactionToMultiset serializes.
func (d *Deriver) applyTransaction(transaction *externalapi.DomainTransaction, creatingBlockDAAScore uint64) error {
	isCoinbase := transactionhelper.IsCoinBase(transaction)
	transactionID := consensushashing.TransactionID(transaction)

	for _, input := range transaction.Inputs {
		entry, ok := d.utxos[input.PreviousOutpoint]
		if !ok {
			return errors.Errorf("utxoderive: transaction %s spends %s:%d which is not in the derived "+
				"UTXO set", transactionID, input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
		}
		serialized, err := utxo.SerializeUTXO(entry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
		d.ms.Remove(serialized)
		delete(d.utxos, input.PreviousOutpoint)
	}

	for i, output := range transaction.Outputs {
		outpoint := externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(i)}
		entry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, creatingBlockDAAScore)
		serialized, err := utxo.SerializeUTXO(entry, &outpoint)
		if err != nil {
			return err
		}
		d.ms.Add(serialized)
		d.utxos[outpoint] = entry
	}

	d.report.TxsAccepted++
	return nil
}
