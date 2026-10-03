package app

import (
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
