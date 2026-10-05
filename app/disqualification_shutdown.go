package app

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain"
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

var (
	disqualifiedStreakMu     sync.Mutex
	disqualifiedStreakDomain domain.Domain
	shutdownOnDisqualified   bool
	disqualifiedRecoveryBusy bool
)

func bindDisqualifiedStreakRecovery(d domain.Domain, shutdownInstead bool) {
	disqualifiedStreakMu.Lock()
	defer disqualifiedStreakMu.Unlock()
	disqualifiedStreakDomain = d
	shutdownOnDisqualified = shutdownInstead
}

type disqualifiedTipRepairer interface {
	RepairDisqualifiedTipChains() (uint64, error)
	ResolveVirtual(progressReportCallback func(uint64, uint64)) error
}

func recoverDisqualifiedTipChains(consensus disqualifiedTipRepairer) (uint64, error) {
	resetCount, err := consensus.RepairDisqualifiedTipChains()
	if err != nil || resetCount == 0 {
		return resetCount, err
	}
	return resetCount, consensus.ResolveVirtual(nil)
}

// recoverFromDisqualifiedBlockStreak retries the disqualified tips while keeping the process and
// peer connections alive. Operators can retain the old fail-stop behavior explicitly.
func recoverFromDisqualifiedBlockStreak(streak int, lastBlock *externalapi.DomainHash) {
	disqualifiedStreakMu.Lock()
	shutdown := shutdownOnDisqualified
	d := disqualifiedStreakDomain
	disqualifiedStreakMu.Unlock()

	if shutdown {
		log.Criticalf("Stopping the node after %d consecutive disqualified blocks, the last %s", streak, lastBlock)
		spawn("stopNodeOnDisqualifiedBlockStreak", func() {
			signal.ShutdownRequestChannel <- struct{}{}
		})
		return
	}

	log.Criticalf("%d consecutive blocks were disqualified, the last %s. Repairing the disqualified "+
		"tip chains and resolving virtual; peer connections stay up.", streak, lastBlock)
	spawn("recoverFromDisqualifiedBlockStreak", func() {
		disqualifiedStreakMu.Lock()
		if disqualifiedRecoveryBusy {
			disqualifiedStreakMu.Unlock()
			return
		}
		if d == nil {
			disqualifiedStreakMu.Unlock()
			log.Errorf("Cannot repair disqualified tip chains before the domain is bound; stopping the node")
			signal.ShutdownRequestChannel <- struct{}{}
			return
		}
		disqualifiedRecoveryBusy = true
		disqualifiedStreakMu.Unlock()
		defer func() {
			disqualifiedStreakMu.Lock()
			disqualifiedRecoveryBusy = false
			disqualifiedStreakMu.Unlock()
		}()

		resetCount, err := recoverDisqualifiedTipChains(d.Consensus())
		if err != nil {
			log.Errorf("Failed to recover disqualified tip chains: %s", err)
			return
		}
		if resetCount == 0 {
			log.Warnf("No disqualified tip blocks were available to repair")
			return
		}
		log.Infof("Reset and attempted to re-resolve %d disqualified tip-chain blocks", resetCount)
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
// network shows up as a streak, which recoverFromDisqualifiedBlockStreak handles.
func logDisqualification(blockHash *externalapi.DomainHash, reason string) {
	log.Warnf("Block %s disqualified from chain: %s", blockHash, reason)
}
