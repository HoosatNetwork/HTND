//go:build !pruningtest

package pruningmanager

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"sync"
)

func testJournalLock(l sync.Locker) sync.Locker              { return l }
func testLockPhase(sync.Locker, string)                      {}
func testLockTarget(sync.Locker, *externalapi.DomainHash)    {}
func testHistoryTarget(*pruningManager, sync.Locker, uint64) {}
func testMaskRemoved(uint64, *database.HistoryMask)          {}
