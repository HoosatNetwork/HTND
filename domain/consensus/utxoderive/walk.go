package utxoderive

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/processes/consensusstatemanager"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/pkg/errors"
)

// DefaultProbeDepth is how far below the current pruning point Preflight looks for a real
// body. Deep enough that a pruned datadir cannot pass by accident, shallow enough to be
// instant.
const DefaultProbeDepth = 200

// Preflight refuses to start a replay against a datadir that cannot support one.
//
// Two failure classes, both fatal and both deliberate:
//
//   - Missing or header-only bodies below the pruning point (H3). The P2P layer cannot make
//     these good: no requester ever asks below the pruning point, and a pruned peer answers
//     such a request with a header-only block rather than an error. A replay that started
//     anyway would walk an empty chain and report success on nothing.
//   - Missing GHOSTDAG data below the pruning point. GHOSTDAG is an input to this product.
//     Deriving it here would silently turn a UTXO replay into a topology re-validation,
//     which is a different thing with different risks.
func (d *Deriver) Preflight(probeDepth int) error {
	stagingArea := model.NewStagingArea()

	hasPruningPoint, err := d.stores.PruningStore.HasPruningPoint(d.stores.DatabaseContext, stagingArea)
	if err != nil {
		return errors.Wrap(err, "utxoderive preflight: could not read the pruning point")
	}
	if !hasPruningPoint {
		return errors.Errorf("utxoderive preflight: source datadir has no pruning point")
	}
	pruningPoint, err := d.stores.PruningStore.PruningPoint(d.stores.DatabaseContext, stagingArea)
	if err != nil {
		return errors.Wrap(err, "utxoderive preflight: could not read the pruning point")
	}

	if _, err := d.loadBodyStrict(d.genesisHash); err != nil {
		return errors.Wrap(err, "utxoderive preflight: genesis body is unavailable, so a replay cannot "+
			"start from an empty MuHash")
	}

	// Walk down from the pruning point. Every step must have GHOSTDAG data, and the deepest
	// block reached must have a real body - that is the block a pruned datadir will not have.
	current := pruningPoint
	depth := 0
	for depth < probeDepth {
		if current.Equal(d.genesisHash) {
			break
		}
		ghostdagData, err := d.stores.GHOSTDAGDataStore.Get(d.stores.DatabaseContext, stagingArea, current, false)
		if err != nil {
			return errors.Wrapf(err, "utxoderive preflight: no stored GHOSTDAG data for %s, %d blocks below "+
				"the pruning point. GHOSTDAG is an input to this replay and will not be recomputed - this "+
				"datadir is not usable, same class as a missing body", current, depth)
		}
		selectedParent := ghostdagData.SelectedParent()
		if selectedParent == nil || selectedParent.Equal(model.VirtualGenesisBlockHash) {
			break
		}
		current = selectedParent
		depth++
	}

	if _, err := d.loadBodyStrict(current); err != nil {
		return errors.Wrapf(err, "utxoderive preflight: no usable body %d blocks below the pruning point "+
			"(%s). This datadir was not run with --archival, or its history came from a headers-proof "+
			"sync that never had these bodies. C1 cannot fetch them: see H3", depth, current)
	}

	return nil
}

// CheckpointHook runs after a target chain block has been fully applied, with the deriver in
// exactly the state that block's header commits to.
type CheckpointHook func(blockHash *externalapi.DomainHash, d *Deriver) error

// Walk replays the selected-parent chain from genesis up to and including highHash.
//
// At every chain block the derived MuHash is compared to that block's own header
// UTXOCommitment - not only at pruning points. Comparing everywhere costs nothing (the
// header is already loaded) and localises the corruption horizon to a single block instead
// of a pruning-point-wide range. Pruning points are still recorded separately in the report,
// because those are the ones an operator can act on.
//
// hooks fire after their block is applied, which is where a caller persists a bucket at the
// current pruning point and a virtual set at the tip.
func (d *Deriver) Walk(highHash *externalapi.DomainHash, hooks map[externalapi.DomainHash]CheckpointHook) error {
	chain, err := d.selectedParentChain(highHash)
	if err != nil {
		return err
	}

	pruningPoints, err := d.pruningPointSet()
	if err != nil {
		return err
	}

	stagingArea := model.NewStagingArea()
	for _, chainBlockHash := range chain {
		if err := d.applyChainBlock(chainBlockHash); err != nil {
			d.report.StoppedAt = chainBlockHash
			d.report.StopReason = err.Error()
			return err
		}
		d.report.ChainBlocks++

		header, err := d.stores.BlockHeaderStore.BlockHeader(d.stores.DatabaseContext, stagingArea, chainBlockHash)
		if err != nil {
			return errors.Wrapf(err, "utxoderive: no header for chain block %s", chainBlockHash)
		}

		derived := d.ms.Hash()
		match := derived.Equal(header.UTXOCommitment())
		_, isPruningPoint := pruningPoints[*chainBlockHash]

		if isPruningPoint {
			d.report.Checkpoints = append(d.report.Checkpoints, Checkpoint{
				PruningPoint:     chainBlockHash,
				DAAScore:         header.DAAScore(),
				DerivedMultiset:  derived,
				HeaderCommitment: header.UTXOCommitment(),
				Match:            match,
			})
		}

		if !match && d.report.FirstMismatch == nil {
			d.report.FirstMismatch = &Checkpoint{
				PruningPoint:     chainBlockHash,
				DAAScore:         header.DAAScore(),
				DerivedMultiset:  derived,
				HeaderCommitment: header.UTXOCommitment(),
				Match:            false,
			}
			if d.stopOnMismatch {
				d.report.StoppedAt = chainBlockHash
				d.report.StopReason = "first commitment mismatch"
				d.finishReport()
				return nil
			}
		}

		if hook, ok := hooks[*chainBlockHash]; ok && match {
			if err := hook(chainBlockHash, d); err != nil {
				return err
			}
		}
	}

	d.finishReport()
	return nil
}

func (d *Deriver) finishReport() {
	var sum uint64
	for _, entry := range d.utxos {
		sum += entry.Amount()
	}
	d.report.DerivedSum = sum
	d.report.DerivedEntries = uint64(len(d.utxos))
}

// applyChainBlock applies one chain block's full merge set, in GetSortedMergeSet order.
//
// This mirrors applyMergeSetBlocks: the chain block's own transactions are not applied here
// (its coinbase is accepted by whichever chain block later merges it as selected parent),
// and a merge-set block's coinbase is accepted only when that block is the selected parent.
func (d *Deriver) applyChainBlock(chainBlockHash *externalapi.DomainHash) error {
	mergeSet, err := d.sortedMergeSet(chainBlockHash)
	if err != nil {
		return err
	}

	acceptanceData := make(externalapi.AcceptanceData, 0, len(mergeSet))

	for i, mergeSetBlockHash := range mergeSet {
		if mergeSetBlockHash.Equal(model.VirtualGenesisBlockHash) || mergeSetBlockHash.Equal(model.VirtualBlockHash) {
			continue
		}
		isSelectedParent := i == 0

		creatingBlockDAAScore, err := d.blockOwnDAAScore(mergeSetBlockHash)
		if err != nil {
			return err
		}
		mergeSetBlock, err := d.loadBodyStrict(mergeSetBlockHash)
		if err != nil {
			return err
		}

		blockAcceptanceData := &externalapi.BlockAcceptanceData{
			BlockHash:                 mergeSetBlockHash,
			TransactionAcceptanceData: make([]*externalapi.TransactionAcceptanceData, len(mergeSetBlock.Transactions)),
		}

		for j, transaction := range mergeSetBlock.Transactions {
			accepted, err := d.isAccepted(transaction, isSelectedParent, chainBlockHash, mergeSetBlockHash)
			if err != nil {
				return err
			}
			blockAcceptanceData.TransactionAcceptanceData[j] = &externalapi.TransactionAcceptanceData{
				Transaction: transaction,
				IsAccepted:  accepted,
			}
			if !accepted {
				continue
			}
			if err := d.applyTransaction(transaction, creatingBlockDAAScore); err != nil {
				return errors.Wrapf(err, "utxoderive: applying transaction from merge-set block %s of "+
					"chain block %s", mergeSetBlockHash, chainBlockHash)
			}
		}

		acceptanceData = append(acceptanceData, blockAcceptanceData)
		d.report.BlocksApplied++
	}

	return d.verifyAcceptedIDMerkleRoot(chainBlockHash, acceptanceData)
}

// verifyAcceptedIDMerkleRoot checks this replay's re-derived acceptance against what the chain
// block actually committed to.
//
// This is the guard that makes re-deriving acceptance safe. If the acceptance rule implemented
// here differs from the one the network used, that shows up immediately, at the first block
// where it matters, naming the block - rather than silently producing a plausible-looking UTXO
// set whose commitment happens not to match for reasons nobody can localise.
//
// The block's OWN header version is passed, never the ambient process global: versions 4 and
// below sort accepted transactions by ID and 5 and above do not, so replaying old history in a
// process ratcheted to 9 would hash the wrong ordering.
func (d *Deriver) verifyAcceptedIDMerkleRoot(chainBlockHash *externalapi.DomainHash,
	acceptanceData externalapi.AcceptanceData,
) error {
	stagingArea := model.NewStagingArea()
	header, err := d.stores.BlockHeaderStore.BlockHeader(d.stores.DatabaseContext, stagingArea, chainBlockHash)
	if err != nil {
		return errors.Wrapf(err, "utxoderive: no header for chain block %s", chainBlockHash)
	}

	derived := consensusstatemanager.CalculateAcceptedIDMerkleRoot(acceptanceData, header.Version())
	if derived.Equal(header.AcceptedIDMerkleRoot()) {
		return nil
	}

	return errors.Errorf("utxoderive: chain block %s committed to acceptedIDMerkleRoot %s but replaying "+
		"its merge set produces %s. This replay's acceptance differs from the network's for this block, "+
		"so any UTXO set derived past it would be wrong for reasons that could not be localised later",
		chainBlockHash, header.AcceptedIDMerkleRoot(), derived)
}

// isAccepted decides whether a merge-set transaction contributes to the UTXO set.
//
// Two rules, matching maybeAcceptTransaction:
//
//  1. A coinbase is accepted only from the selected parent. Merged blocks' rewards are paid
//     by the merging chain block's own coinbase, not by their own.
//  2. Every input must resolve against the derived set.
//
// Rule 2 is where this replay deliberately parts company with the live path. Live code turns
// a missing input into "not accepted" and, when the offset flag is latched, skips the
// transaction and keeps the block - which is how outputs go missing with no error anywhere.
// Here a missing input is a hard error: it is the horizon we are trying to find, and
// tolerating it would reproduce the bug we are replaying to escape.
//
// Not yet implemented in this slice: script, mass, sequence-lock and coinbase-maturity
// validation. Those are block-body properties that were already checked when these blocks
// were first accepted, and any disagreement they would cause surfaces as a commitment
// mismatch - which is this walk's mandatory output regardless.
func (d *Deriver) isAccepted(transaction *externalapi.DomainTransaction, isSelectedParent bool,
	chainBlockHash, mergeSetBlockHash *externalapi.DomainHash,
) (bool, error) {
	if transactionhelper.IsCoinBase(transaction) {
		return isSelectedParent, nil
	}
	for _, input := range transaction.Inputs {
		if _, ok := d.utxos[input.PreviousOutpoint]; !ok {
			return false, errors.Errorf("utxoderive: transaction %s in merge-set block %s of chain block "+
				"%s spends %s:%d, which is not in the derived UTXO set. Stopping rather than skipping the "+
				"transaction - skipping is what the live path does, and it is how outputs vanish silently",
				consensushashing.TransactionID(transaction), mergeSetBlockHash, chainBlockHash,
				input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
		}
	}
	return true, nil
}

// pruningPointSet returns every pruning point this datadir recorded, by index.
func (d *Deriver) pruningPointSet() (map[externalapi.DomainHash]struct{}, error) {
	stagingArea := model.NewStagingArea()
	result := make(map[externalapi.DomainHash]struct{})

	currentIndex, err := d.stores.PruningStore.CurrentPruningPointIndex(d.stores.DatabaseContext, stagingArea)
	if err != nil {
		return result, nil // A datadir with no recorded index simply yields no checkpoints.
	}
	for i := uint64(0); i <= currentIndex; i++ {
		pruningPoint, err := d.stores.PruningStore.PruningPointByIndex(d.stores.DatabaseContext, stagingArea, i)
		if err != nil {
			continue
		}
		result[*pruningPoint] = struct{}{}
	}
	return result, nil
}
