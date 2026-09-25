package app

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/os/signal"
)

// maxConsecutiveDisqualifiedBlocks is how many blocks in a row the node may disqualify from the chain
// before it stops itself.
//
// A node that disqualifies every block it adds has rejected the chain the network is on. Each new
// block extends that chain and is disqualified in turn, so the node never follows the network again,
// keeps serving a stale virtual to miners and RPC clients, and spends its time re-resolving tips.
// Stopping makes the failure visible instead. The data is left alone: what to do with it (a resync, a
// repair flag) is the operator's call.
//
// 15 is about three seconds of mainnet blocks. A streak only builds on blocks inserted with a virtual
// update - relayed and submitted blocks, not IBD bodies, whose statuses are resolved later - and any
// UTXO-valid block in between resets it.
const maxConsecutiveDisqualifiedBlocks = 15

// disqualifiedBlockStreakHandler returns the callback consensus runs when the streak is reached: the
// shutdown below when the operator asked for it with --stop-on-disqualified-streak, and nothing
// otherwise (consensus logs the streak at critical level either way).
//
// Stopping is opt-in because a streak is not proof that this node is the one that is wrong. A block
// resolves to DisqualifiedFromChain when its UTXO commitment, coinbase or accepted-ID merkle root does
// not match; its header, proof of work included, can still be valid. Anyone who mines 15 such blocks
// on top of the tip faster than the network mines one good block - cheap while difficulty sits at the
// powMax floor (HTN-228) - would take every node running with the stop down at once, and a restart
// policy turns that into a loop. It also hides the condition from the operator instead of recovering
// from it: RepairDisqualifiedTipChains and the offset-baseline rules re-verify a disqualified chain;
// a stopped node re-verifies nothing.
func disqualifiedBlockStreakHandler(stop bool) func(streak int, lastBlock *externalapi.DomainHash) {
	if !stop {
		return nil
	}
	return stopNodeOnDisqualifiedBlockStreak
}

// stopNodeOnDisqualifiedBlockStreak requests a shutdown through the interrupt listener. It runs under
// the consensus lock, so the request is sent from its own goroutine: the listener's channel is
// unbuffered, and the shutdown it starts needs that lock.
func stopNodeOnDisqualifiedBlockStreak(streak int, lastBlock *externalapi.DomainHash) {
	log.Criticalf("Stopping the node: %d consecutive blocks were disqualified from the chain, the last "+
		"one %s. This node is not following the network. Check the log for the first disqualification "+
		"(\"NOT tolerating\" / \"UTXO verification for block\"), then resync from a fresh datadir.",
		streak, lastBlock)
	spawn("stopNodeOnDisqualifiedBlockStreak", func() {
		signal.ShutdownRequestChannel <- struct{}{}
	})
}
