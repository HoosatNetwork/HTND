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
