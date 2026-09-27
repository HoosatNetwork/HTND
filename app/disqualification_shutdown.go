package app

import (
	"fmt"
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/signal"
)

const maxConsecutiveDisqualifiedBlocks = 15

var (
	disqualifiedStreakMu     sync.Mutex
	disqualifiedStreakDomain domain.Domain
	shutdownOnDisqualified   bool
	disqualifiedRecoverBusy  bool
)

func bindDisqualifiedStreakRecovery(d domain.Domain, shutdownInstead bool) {
	disqualifiedStreakMu.Lock()
	defer disqualifiedStreakMu.Unlock()
	disqualifiedStreakDomain = d
	shutdownOnDisqualified = shutdownInstead
}

func recoverFromDisqualifiedBlockStreak(streak int, lastBlock *externalapi.DomainHash) {
	disqualifiedStreakMu.Lock()
	shutdown := shutdownOnDisqualified
	d := disqualifiedStreakDomain
	disqualifiedStreakMu.Unlock()

	if shutdown {
		log.Criticalf("Stopping the node after %d disqualified blocks, last %s.", streak, lastBlock)
		spawn("stopNodeOnDisqualifiedBlockStreak", func() {
			signal.ShutdownRequestChannel <- struct{}{}
		})
		return
	}

	log.Criticalf("%d disqualified blocks in a row, last %s. Repairing statuses. Connections stay up.",
		streak, lastBlock)

	spawn("recoverFromDisqualifiedBlockStreak", func() {
		disqualifiedStreakMu.Lock()
		if disqualifiedRecoverBusy || d == nil {
			disqualifiedStreakMu.Unlock()
			return
		}
		disqualifiedRecoverBusy = true
		disqualifiedStreakMu.Unlock()
		defer func() {
			disqualifiedStreakMu.Lock()
			disqualifiedRecoverBusy = false
			disqualifiedStreakMu.Unlock()
		}()

		resetCount, err := d.Consensus().RepairDisqualifiedTipChains()
		if err != nil {
			log.Errorf("Failed to reset disqualified chains: %s", err)
			return
		}
		if resetCount == 0 {
			log.Errorf("Nothing to reset.")
			return
		}
		log.Infof("Reset %d blocks; resolving virtual", resetCount)
		if err := d.Consensus().ResolveVirtual(nil); err != nil {
			log.Errorf("ResolveVirtual after repair failed: %s", err)
			return
		}
		log.Infof("Virtual resolved after repair")
	})
}

// panicOnDisqualification stops the node with a stack trace the moment any block is disqualified from
// the chain, whether it failed UTXO verification itself or inherited its selected parent's
// disqualification. It panics on the resolving goroutine, under the consensus lock and before the
// status is committed, so the trace shows the exact resolution path; panics.HandlePanic logs it and
// exits.
//
// This deliberately trades availability for visibility: a single invalid block relayed by any peer
// stops the node, and because the disqualification is never committed, the same block stops it
// again after a restart until it is investigated.
func panicOnDisqualification(blockHash *externalapi.DomainHash, reason string) {
	panic(fmt.Sprintf("block %s disqualified from chain: %s", blockHash, reason))
}
