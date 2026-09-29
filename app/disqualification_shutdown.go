package app

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/signal"
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

// logDisqualification logs the full report for every block disqualified from the chain, whether it
// failed UTXO verification itself or inherited its selected parent's disqualification, and lets the
// node carry on.
//
// It used to panic instead. Any single invalid block relayed by any peer then stopped the node, and
// because the panic fired before the status was committed, the same block stopped it again on every
// restart. With the strict UTXO commitment gate active, a block from a miner on a different UTXO
// history is disqualified in the ordinary course of things - rejecting it is the gate's job - so the
// panic turned a rule working as intended into a crash loop. A node that has really fallen off the
// network shows up as a streak, which stopNodeOnDisqualifiedBlockStreak still handles.
func logDisqualification(blockHash *externalapi.DomainHash, reason string) {
	log.Warnf("Block %s disqualified from chain: %s", blockHash, reason)
}
