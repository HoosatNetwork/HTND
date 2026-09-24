package consensus

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// disqualificationStreak counts consecutive blocks that came out of a virtual-updating insertion as
// StatusDisqualifiedFromChain.
//
// One disqualified block is consensus doing its job. A run of them with nothing valid in between is
// a node that has disqualified the chain everyone else is on: every new block extends that chain and
// inherits the disqualification, so the node keeps accepting blocks, never follows the network again,
// and burns time re-resolving every tip. Past the threshold there is nothing useful left for it to do,
// so it reports once and leaves the decision (stopping the node) to whoever wired the callback.
//
// A UTXO-valid block ends the streak. Blocks whose status is still pending say nothing either way and
// leave the count alone. Guarded by consensus.lock.
type disqualificationStreak struct {
	threshold int
	onReached func(streak int, lastBlock *externalapi.DomainHash)
	count     int
	reported  bool
}

// note records the resolved status of a block just inserted with updateVirtual. It returns true on
// the call that reaches the threshold, and only on that one until a valid block resets the streak.
func (d *disqualificationStreak) note(status externalapi.BlockStatus) bool {
	if d.threshold <= 0 {
		return false
	}
	switch status {
	case externalapi.StatusUTXOValid:
		d.count = 0
		d.reported = false
	case externalapi.StatusDisqualifiedFromChain:
		d.count++
		if d.count >= d.threshold && !d.reported {
			d.reported = true
			return true
		}
	}
	return false
}

// noteInsertedBlockStatus reads the status blockHash was resolved to and advances the streak. The
// status the block processor returns is the pre-resolution one, so it is read back from the store,
// the same way the relay flow logs it.
func (s *consensus) noteInsertedBlockStatus(blockHash *externalapi.DomainHash) {
	if s.disqualificationStreak.threshold <= 0 {
		return
	}
	status, err := s.blockStatusStore.Get(s.databaseContext, model.NewStagingArea(), blockHash)
	if err != nil {
		log.Warnf("Could not read the status of block %s for the disqualification streak: %s", blockHash, err)
		return
	}
	if !s.disqualificationStreak.note(status) {
		return
	}
	log.Criticalf("%d blocks in a row were disqualified from the chain, the last one %s. This node "+
		"is rejecting the chain the network is on and cannot follow it again on its own.",
		s.disqualificationStreak.count, blockHash)
	if s.disqualificationStreak.onReached != nil {
		s.disqualificationStreak.onReached(s.disqualificationStreak.count, blockHash)
	}
}
