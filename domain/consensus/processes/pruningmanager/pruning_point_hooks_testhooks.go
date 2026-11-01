//go:build pruningtest

package pruningmanager

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model"

type pruningJournalHooks struct{ hook func(string) }

func (p *pruningManager) setDerivationHook(h func(string)) { p.hook = h }
func (p *pruningManager) derivationTestHook() func(string) { return p.hook }
func (p *pruningManager) derivationHook(s string) {
	if p.hook != nil {
		p.hook(s)
	}
}

// HistoryDeletionBatchBlocks bounds the blocks one history deletion batch deletes. It
// is variable only in lab builds, so tests can split a small job into many batches.
var HistoryDeletionBatchBlocks = 1024

func ConfigurePruningTestHook(m model.PruningManager, h func(string)) {
	m.(*pruningManager).setDerivationHook(h)
}
