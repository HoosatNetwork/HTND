//go:build pruningtest

package pruningmanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"sync"
	"time"
)

var testLockEpoch = time.Now()

type testMeasuredLock struct {
	sync.Locker
	phase, target string
	start         time.Time
	wait          time.Duration
}

func testJournalLock(l sync.Locker) sync.Locker {
	return &testMeasuredLock{Locker: l, phase: "capture", target: "pending"}
}
func (l *testMeasuredLock) Lock() {
	a := time.Now()
	l.Locker.Lock()
	l.start = time.Now()
	l.wait = l.start.Sub(a)
}
func (l *testMeasuredLock) Unlock() {
	end := time.Now()
	l.Locker.Unlock()
	log.Infof("[PP42] phase=lock target=%s section=%s lock_wait_ns=%d lock_hold_ns=%d start_ns=%d end_ns=%d", l.target, l.phase, l.wait.Nanoseconds(), end.Sub(l.start).Nanoseconds(), l.start.Sub(testLockEpoch).Nanoseconds(), end.Sub(testLockEpoch).Nanoseconds())
}
func testLockPhase(l sync.Locker, p string) {
	if m, ok := l.(*testMeasuredLock); ok {
		m.phase = p
	}
}
func testLockTarget(l sync.Locker, p *externalapi.DomainHash) {
	if m, ok := l.(*testMeasuredLock); ok {
		m.target = p.String()
	}
}
func testHistoryTarget(pm *pruningManager, l sync.Locker, index uint64) {
	if h, e := pm.pruningStore.PruningPointByIndex(pm.databaseContext, model.NewStagingArea(), index); e == nil {
		testLockTarget(l, h)
	}
}
func testMaskRemoved(index uint64, m *database.HistoryMask) {
	r := m.TakeReads()
	log.Infof("[PP42] phase=deletion-mask-removed index=%d masked_reads_under_lock status=%d block=%d acceptance_data=%d multiset=%d utxo_diff=%d daa_added_blocks=%d", index, r[0], r[1], r[2], r[3], r[4], r[5])
}
