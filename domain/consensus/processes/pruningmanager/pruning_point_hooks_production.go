//go:build !pruningtest

package pruningmanager

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model"

type pruningJournalHooks struct{}

func (*pruningManager) setDerivationHook(func(string))   {}
func (*pruningManager) derivationTestHook() func(string) { return nil }
func (*pruningManager) derivationHook(string)            {}

// HistoryDeletionBatchBlocks bounds the blocks one history deletion batch deletes.
const HistoryDeletionBatchBlocks = 1024

func ConfigurePruningTestHook(m model.PruningManager, h func(string)) {

}
