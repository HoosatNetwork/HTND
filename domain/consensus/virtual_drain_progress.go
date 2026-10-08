package consensus

import (
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
)

// virtualDrainProgressInterval is how often a pending-virtual drain reports its progress.
const virtualDrainProgressInterval = 10 * time.Second

// virtualDrainProgress reports on the loops that resolve all pending virtual before a block is
// inserted or built. After a restart in the middle of resolving virtual, the first relayed block
// drains the whole backlog in one of these loops, which can take hours. They call
// resolveVirtualChunkNoLock directly, so neither ResolveVirtual's progress lines nor the
// slow-chunk lines appear, and without this the node logged nothing at all until the backlog was
// gone - indistinguishable from a hang.
//
// It reports at most once per virtualDrainProgressInterval, and reports completion only if it
// reported progress, so the common one-chunk drain stays silent.
type virtualDrainProgress struct {
	reason        string
	start         time.Time
	lastReport    time.Time
	startDAAScore uint64
	chunks        int
	reported      bool
	now           func() time.Time
	logf          func(format string, args ...any)
}

func newVirtualDrainProgress(reason string, startDAAScore uint64) *virtualDrainProgress {
	now := time.Now()
	return &virtualDrainProgress{
		reason:        reason,
		start:         now,
		lastReport:    now,
		startDAAScore: startDAAScore,
		now:           time.Now,
		logf:          log.Infof,
	}
}

// chunkResolved records one resolved chunk. targetDAAScore is an estimate of where virtual will end
// up; 0, or a target at or below the start, leaves the percentage out.
func (p *virtualDrainProgress) chunkResolved(daaScore, targetDAAScore uint64) {
	p.chunks++
	now := p.now()
	if now.Sub(p.lastReport) < virtualDrainProgressInterval {
		return
	}
	p.lastReport = now
	p.reported = true

	elapsed := now.Sub(p.start)
	rate := float64(0)
	if daaScore > p.startDAAScore && elapsed > 0 {
		rate = float64(daaScore-p.startDAAScore) / elapsed.Seconds()
	}
	if targetDAAScore > p.startDAAScore {
		percent := min(100*float64(saturatingSub(daaScore, p.startDAAScore))/float64(targetDAAScore-p.startDAAScore), 100)
		p.logf("Resolving pending virtual %s: DAA score %d of about %d (%.0f%%), %d chunks in %s, %.0f DAA/s",
			p.reason, daaScore, targetDAAScore, percent, p.chunks, elapsed.Round(time.Second), rate)
		return
	}
	p.logf("Resolving pending virtual %s: DAA score %d, %d chunks in %s, %.0f DAA/s",
		p.reason, daaScore, p.chunks, elapsed.Round(time.Second), rate)
}

// finished reports the end of a drain that reported progress.
func (p *virtualDrainProgress) finished(daaScore uint64) {
	if !p.reported {
		return
	}
	p.logf("Resolved pending virtual %s: DAA score %d to %d, %d chunks in %s",
		p.reason, p.startDAAScore, daaScore, p.chunks, p.now().Sub(p.start).Round(time.Second))
}

func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// virtualDrainDAAScoresNoLock returns virtual's DAA score and, as the drain's estimated end, the DAA
// score of the headers selected tip. Progress reporting must never fail a drain, so a score that
// cannot be read is 0. Must be called with s.lock held.
func (s *consensus) virtualDrainDAAScoresNoLock() (virtualDAAScore, targetDAAScore uint64) {
	stagingArea := model.NewStagingArea()
	if score, err := s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, model.VirtualBlockHash); err == nil {
		virtualDAAScore = score
	}
	if tip, err := s.headersSelectedTipStore.HeadersSelectedTip(s.databaseContext, stagingArea); err == nil && tip != nil {
		if score, err := s.daaBlocksStore.DAAScore(s.databaseContext, stagingArea, tip); err == nil {
			targetDAAScore = score
		}
	}
	return virtualDAAScore, targetDAAScore
}
