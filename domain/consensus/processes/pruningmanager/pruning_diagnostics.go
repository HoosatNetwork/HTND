package pruningmanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"sync"
	"time"
)

func ConfigurePruningDiagnostics(manager model.PruningManager, enabled bool) {
	manager.(*pruningManager).pruningDiagnostics = enabled
}

type diagnosticLocker struct {
	sync.Locker
	owner string
	start time.Time
	wait  time.Duration
}

func (pm *pruningManager) diagnosticLock(l sync.Locker, owner string) sync.Locker {
	if !pm.pruningDiagnostics {
		return l
	}
	return &diagnosticLocker{Locker: l, owner: owner}
}
func (l *diagnosticLocker) Lock() {
	a := time.Now()
	l.Locker.Lock()
	l.start = time.Now()
	l.wait = l.start.Sub(a)
}
func (l *diagnosticLocker) Unlock() {
	elapsed := time.Since(l.start)
	l.Locker.Unlock()
	log.Infof("Pruning diagnostics: owner=%s consensus_lock_wait=%s consensus_lock_hold=%s", l.owner, l.wait, elapsed)
}
