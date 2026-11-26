package server

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/pkg/errors"
)

var keyChains = []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain}

type walletAddressSet map[string]*walletAddress

func (was walletAddressSet) strings() []string {
	addresses := make([]string, 0, len(was))
	for addr := range was {
		addresses = append(addresses, addr)
	}
	return addresses
}

func (s *server) syncLoop() error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	s.warnOfUnscannableLastUsedIndexes()
	s.lock.Lock()
	s.warnOfMLDSA44ScanGap()
	s.lock.Unlock()
	err := s.collectRecentAddresses(false)
	if err != nil {
		return err
	}

	err = s.refreshUTXOs(0)
	if err != nil {
		return err
	}
	log.Infof("Wallet contained %d UTXOs after initial sync", len(s.utxosSortedByAmount))

	s.firstSyncDone.Store(true)
	log.Infof("Wallet is synced and ready for operation")

	for {
		forced := false
		select {
		case <-ticker.C:
		case <-s.forceSyncChan:
			forced = true
		}

		err := s.sync(forced)
		if err != nil {
			return err
		}
		s.checkPendingBroadcasts(time.Now())
	}
}

func (s *server) sync(forced bool) error {
	err := s.collectFarAddresses()
	if err != nil {
		return err
	}

	err = s.collectRecentAddresses(forced)
	if err != nil {
		return err
	}

	err = s.collectOldAddresses()
	if err != nil {
		return err
	}

	// err = s.refreshUTXOs()
	// if err != nil {
	// 	return err
	// }

	return nil
}

const (
	numIndexesToQueryForFarAddresses    = 100
	numIndexesToQueryForRecentAddresses = 1000
)

// scanIndexLimit is the first index no scan reaches: public BIP32 derivation stops below the hardened
// indexes (libhtnwallet/bip32), so no wallet address exists at or above it. Keeping every scanned range
// below it also keeps the range arithmetic far from uint32 overflow. A variable only so that tests can use
// a small index space.
var scanIndexLimit uint32 = 1 << 31

// scannableLastUsedIndex returns the recorded last used index of a key chain, or 0 for one no address can
// have (see scanIndexLimit), which a damaged or hand-edited keys file can hold. Such a value would wrap the
// scan's range ends around and skip every batch, the first included. Scanning up to scanIndexLimit instead would
// take days before the wallet is usable, so the scans treat it as unknown: the first sync scans from index
// 0 as for a new wallet, and a coin found on that key chain raises it as usual
// (updateAddressesAndLastUsedIndexes).
func scannableLastUsedIndex(index uint32) uint32 {
	if index >= scanIndexLimit {
		return 0
	}
	return index
}

// lastUsedIndexes returns the last used index of the external and of the internal key chain, as the scans
// use them. The caller holds s.lock.
func (s *server) lastUsedIndexes() (external, internal uint32) {
	return scannableLastUsedIndex(s.keysFile.LastUsedExternalIndex()),
		scannableLastUsedIndex(s.keysFile.LastUsedInternalIndex())
}

func (s *server) warnOfUnscannableLastUsedIndexes() {
	s.lock.RLock()
	defer s.lock.RUnlock()
	for name, index := range map[string]uint32{
		"external": s.keysFile.LastUsedExternalIndex(),
		"internal": s.keysFile.LastUsedInternalIndex(),
	} {
		if index >= scanIndexLimit {
			log.Warnf("The keys file records %d as the last used %s address index, but no address exists at "+
				"or above index %d; the address scan ignores it and starts from index 0", index, name, scanIndexLimit)
		}
	}
}

// addressesToQueryCached is addressesToQuery, remembering the batches collectRecentAddresses and
// collectOldAddresses scan. They are scanned again and again while the daemon runs, and deriving a batch
// of 1000 indexes takes most of a second of CPU - a wallet with a few thousand used addresses kept a core
// busy deriving the same keys again. A
// batch never changes while the daemon runs: the extended public keys, the ML-DSA-44 key pools and the
// imported keys are all fixed once it has started.
//
// The far scan's ranges are not cached: they move forward by numIndexesToQueryForFarAddresses on every sync
// and are not scanned again, so keeping them would only grow memory. The caller holds s.lock, and must not
// modify the returned set.
func (s *server) addressesToQueryCached(start, end uint32) (walletAddressSet, error) {
	if end-start != numIndexesToQueryForRecentAddresses || start%numIndexesToQueryForRecentAddresses != 0 {
		return s.addressesToQuery(start, end)
	}
	if addresses, ok := s.recentAddressBatches[start]; ok {
		return addresses, nil
	}
	addresses, err := s.addressesToQuery(start, end)
	if err != nil {
		return nil, err
	}
	if s.recentAddressBatches == nil {
		s.recentAddressBatches = make(map[uint32]walletAddressSet)
	}
	s.recentAddressBatches[start] = addresses
	return addresses, nil
}

// addressesToQuery scans the addresses in the given range. Because
// each cosigner in a multisig has its own unique path for generating
// addresses it goes over all the cosigners and adds their addresses
// for each key chain. The batch starting at index 0 also holds the imported addresses.
func (s *server) addressesToQuery(start, end uint32) (walletAddressSet, error) {
	addresses := make(walletAddressSet)
	if start == 0 {
		maps.Copy(addresses, s.importedAddresses)
	}
	for index := start; index < end; index++ {
		cosignerCount, err := checkedUint32FromInt(len(s.keysFile.ExtendedPublicKeys))
		if err != nil {
			return nil, err
		}
		for cosignerIndex := range cosignerCount {
			for _, keychain := range keyChains {
				address := &walletAddress{
					index:         index,
					cosignerIndex: cosignerIndex,
					keyChain:      keychain,
				}
				addressStrings, err := s.walletAddressStringsForScan(address)
				if err != nil {
					return nil, err
				}
				for _, addressString := range addressStrings {
					addresses[addressString] = address
				}

				for _, mldsa44Addr := range s.mldsa44WalletAddressesForScan(address) {
					mldsa44Address, err := s.mldsa44Address(mldsa44Addr)
					if err != nil {
						return nil, err
					}
					addresses[mldsa44Address.String()] = mldsa44Addr
				}
			}
		}
	}

	return addresses, nil
}

// collectFarAddresses collects numIndexesToQueryForFarAddresses addresses
// from the last point it stopped in the previous call.
func (s *server) collectFarAddresses() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	scanned, err := s.collectAddresses(s.nextSyncStartIndex, s.nextSyncStartIndex+numIndexesToQueryForFarAddresses)
	if err != nil {
		return err
	}
	if !scanned {
		// Retry the same range next time; moving past it would leave it unscanned for good.
		return nil
	}

	s.nextSyncStartIndex += numIndexesToQueryForFarAddresses
	return nil
}

func (s *server) maxUsedIndexWithLock() uint32 {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.maxUsedIndex()
}

// maxUsedIndex is below scanIndexLimit, so adding a few batches to it cannot overflow.
func (s *server) maxUsedIndex() uint32 {
	external, internal := s.lastUsedIndexes()
	return max(external, internal)
}

// collectRecentAddresses covers the first sweep and then the windows around the two key chains.
// It releases the lock between batches so other wallet requests can proceed.
func (s *server) collectRecentAddresses(forced bool) error {
	index := uint32(0)
	maxUsedIndex := s.maxUsedIndexWithLock()
	for ; index < min(maxUsedIndex+numIndexesToQueryForRecentAddresses, scanIndexLimit); index += numIndexesToQueryForRecentAddresses {
		if !s.shouldPollRecentBatch(index, forced) {
			continue
		}
		scanned, err := s.collectRecentBatch(index)
		if err != nil {
			return err
		}
		if !scanned {
			// The next sync rescans from the start; nextSyncStartIndex must not claim this range.
			return nil
		}
		maxUsedIndex = s.maxUsedIndexWithLock()

		s.updateSyncingProgressLog(index, maxUsedIndex)
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if index > s.nextSyncStartIndex {
		s.nextSyncStartIndex = index
	}
	s.handoffFormerRecentBatches()
	return nil
}

func (s *server) shouldPollRecentBatch(start uint32, forced bool) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if !s.recentScannerOwnsBatch(start) {
		return false
	}
	if forced || start+numIndexesToQueryForRecentAddresses > s.nextSyncStartIndex ||
		(start == 0 && len(s.importedAddresses) > 0) {
		return true
	}
	poll := s.recentBatchPolls[start]
	return !poll.verifyUntil.IsZero() || !s.clock().Before(poll.next)
}

func (s *server) collectRecentBatch(start uint32) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	requestStarted := s.clock()
	scanned, err := s.collectAddresses(start, start+numIndexesToQueryForRecentAddresses)
	if err != nil || !scanned {
		return false, err
	}

	// A recent request satisfies an old re-check only if it started at or after the deadline.
	s.finishBatchRechecks(start, requestStarted)
	if !s.recentBatchIsInWindow(start) || (start == 0 && len(s.importedAddresses) > 0) {
		return true, nil
	}

	// Only the recent path records this poll. It covers the whole logical batch,
	// including a terminal batch clipped by scanIndexLimit.
	finished := s.clock()
	next := requestStarted.Add(recentBatchRescanInterval)
	// The node may check each address at a different time and serve the result
	// through age 30 s. A slow reply can leave a mix of early and late checks.
	cacheExpired := finished.Add(usableAddressesCacheTTL + time.Nanosecond)
	slow := cacheExpired.After(next)
	poll, seen := s.recentBatchPolls[start]
	switch {
	case !seen:
		poll.verifyUntil = finished.Add(recentBatchRescanInterval)
	case !poll.verifyUntil.IsZero() && !requestStarted.Before(poll.verifyUntil):
		poll.verifyUntil = time.Time{}
	case poll.verifyUntil.IsZero() && requestStarted.Before(poll.next):
		// An early forced poll may still get a cached answer. Keep the scheduled poll.
		next = poll.next
	}
	if slow && cacheExpired.After(poll.verifyUntil) {
		// Use the regular cadence until a poll starts past the latest possible
		// expiry, so a check made early in the slow request can be found sooner.
		poll.verifyUntil = cacheExpired
	}
	poll.next = next
	if s.recentBatchPolls == nil {
		s.recentBatchPolls = make(map[uint32]recentBatchPoll)
	}
	s.recentBatchPolls[start] = poll
	return true, nil
}

// recentScannerOwnsBatch reports whether the recent scanner owns a batch, including
// during its cooldown. The caller holds s.lock.
func (s *server) recentScannerOwnsBatch(start uint32) bool {
	return start+numIndexesToQueryForRecentAddresses > s.nextSyncStartIndex || s.recentBatchIsInWindow(start)
}

// recentBatchIsInWindow includes imported batch zero and the batches near either last-used index.
// The caller holds s.lock.
func (s *server) recentBatchIsInWindow(start uint32) bool {
	if start == 0 && len(s.importedAddresses) > 0 {
		return true
	}
	end := start + numIndexesToQueryForRecentAddresses
	external, internal := s.lastUsedIndexes()
	for _, lastUsedIndex := range []uint32{external, internal} {
		if end+numIndexesToQueryForRecentAddresses > lastUsedIndex &&
			start < lastUsedIndex+numIndexesToQueryForRecentAddresses {
			return true
		}
	}
	return false
}

// handoffFormerRecentBatches keeps a departing batch's pending poll even if the
// old cursor has already passed the batch. The caller holds s.lock.
func (s *server) handoffFormerRecentBatches() {
	for start, poll := range s.recentBatchPolls {
		if s.recentBatchIsInWindow(start) {
			continue
		}
		delete(s.recentBatchPolls, start)
		// Keep the handoff and any ordinary old re-check. Each has its own
		// deadline and a scan at or past both can satisfy them together.
		pending := slices.IndexFunc(s.oldBatchRechecks, func(recheck oldBatchRecheck) bool {
			return recheck.start == start && recheck.handedOff
		})
		if pending >= 0 {
			if !poll.next.Before(s.oldBatchRechecks[pending].notBefore) {
				continue
			}
			s.oldBatchRechecks = slices.Delete(s.oldBatchRechecks, pending, pending+1)
		}
		at := sort.Search(len(s.oldBatchRechecks), func(i int) bool {
			return !s.oldBatchRechecks[i].notBefore.Before(poll.next)
		})
		s.oldBatchRechecks = slices.Insert(s.oldBatchRechecks, at, oldBatchRecheck{start: start, notBefore: poll.next, handedOff: true})
	}
}

// collectOldAddresses rescans one batch of the used addresses collectRecentAddresses skips, and the next
// sync the batch after it. An address that was empty when the daemon started can receive coins at any time,
// so every used index has to be scanned again; taking one batch per sync keeps the work of a sync the same
// however many addresses the wallet has used.
//
// The batches are rescanned in rounds from index 0. A round ends where the last used index + 1000 was when
// it started (rescanRoundEnd); indexes used later wait for the next round. If the end moved with the last
// used index, a wallet using more than 1000 new indexes per sync would keep the cursor from ever wrapping
// back to the batches behind it. With successful scans, a 75,000-index round takes at most 76 scan
// opportunities plus a wrap. Discovery can also wait for a re-check. Tracked addresses are in s.addressSet
// and are read directly.
//
// The node answers GetUsableAddresses from a cache shared by all its clients for up to
// usableAddressesCacheTTL, so an answer can predate a coin that arrived before the scan. Each batch
// scanned here is checked again after oldBatchRecheckDelay, unless another full scan supersedes the
// pending check. Every sync drains the due checks, including checks a recent poll has not satisfied.
func (s *server) collectOldAddresses() error {
	scanned, err := s.collectNextOldBatch()
	if err != nil || !scanned {
		// A closed route: the due re-checks stay queued for the next sync.
		return err
	}
	for {
		rechecked, err := s.recheckOldBatch()
		if err != nil || !rechecked {
			return err
		}
	}
}

// collectNextOldBatch scans the batch at the rescan cursor and moves the cursor on, past the batches
// collectRecentAddresses scans. It reports false only when a closed route skipped the scan.
func (s *server) collectNextOldBatch() (scanned bool, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	roundStarted := false
	for {
		if s.nextRescanIndex >= s.rescanRoundEnd {
			if roundStarted {
				// Every batch of the round is one collectRecentAddresses scans, in a wallet with few addresses.
				return true, nil
			}
			roundStarted = true
			s.nextRescanIndex = 0
			s.rescanRoundEnd = min(s.maxUsedIndex()+numIndexesToQueryForRecentAddresses, scanIndexLimit)
		}
		start := s.nextRescanIndex
		if s.recentScannerOwnsBatch(start) {
			s.nextRescanIndex = start + numIndexesToQueryForRecentAddresses
			continue
		}
		requestStarted := s.clock()
		scanned, err := s.collectAddresses(start, start+numIndexesToQueryForRecentAddresses)
		if err != nil || !scanned {
			// A skipped scan is retried by the next sync; moving on would leave the batch a round late.
			return false, err
		}
		s.nextRescanIndex = start + numIndexesToQueryForRecentAddresses
		// Replace any ordinary re-check from the previous round. Keep a handed-off
		// poll if this scan started too early to satisfy it.
		s.oldBatchRechecks = slices.DeleteFunc(s.oldBatchRechecks, func(recheck oldBatchRecheck) bool {
			return recheck.start == start && (!recheck.handedOff || !requestStarted.Before(recheck.notBefore))
		})
		s.oldBatchRechecks = append(s.oldBatchRechecks, oldBatchRecheck{start: start, notBefore: s.clock().Add(oldBatchRecheckDelay)})
		return true, nil
	}
}

const (
	// usableAddressesCacheTTL is how long the node may answer GetUsableAddresses from its cache
	// (app/rpc/rpchandlers/get_usable_addresses.go). The re-check relies on it; a node caching longer
	// would make the re-check's answer stale too.
	usableAddressesCacheTTL = 30 * time.Second
	// oldBatchRecheckDelay waits past entries the previous request could have
	// populated, with an extra tick of margin.
	oldBatchRecheckDelay = usableAddressesCacheTTL + 2*time.Second
	// The recent poll interval reduces repeated full-batch RPCs. The node does
	// not report cache age or eviction, so the interval is not a freshness bound.
	recentBatchRescanInterval = 32 * time.Second
)

type recentBatchPoll struct {
	// Poll on every sync during verification after entry or a slow reply.
	// Earlier requests, such as far scans, can warm entries before a batch enters.
	verifyUntil time.Time
	next        time.Time
}

type oldBatchRecheck struct {
	start     uint32
	notBefore time.Time
	handedOff bool
}

func (s *server) finishBatchRechecks(start uint32, requestStarted time.Time) {
	s.oldBatchRechecks = slices.DeleteFunc(s.oldBatchRechecks, func(recheck oldBatchRecheck) bool {
		return recheck.start == start && !requestStarted.Before(recheck.notBefore)
	})
}

func (s *server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// recheckOldBatch scans the first due check. A closed route leaves the check queued.
func (s *server) recheckOldBatch() (scanned bool, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	requestStarted := s.clock()
	if len(s.oldBatchRechecks) == 0 || requestStarted.Before(s.oldBatchRechecks[0].notBefore) {
		return false, nil
	}
	start := s.oldBatchRechecks[0].start
	scanned, err = s.collectAddresses(start, start+numIndexesToQueryForRecentAddresses)
	if err != nil || !scanned {
		return false, err
	}
	s.finishBatchRechecks(start, requestStarted)
	return true, nil
}

// collectAddresses scans the given index range and reports whether it did. A closed RPC route (the client
// is reconnecting) skips the scan without an error; callers must then not treat the range as scanned. The
// part of the range at or above scanIndexLimit holds no addresses and is not asked about.
func (s *server) collectAddresses(start, end uint32) (scanned bool, err error) {
	end = min(end, scanIndexLimit)
	if start >= end {
		return true, nil
	}
	addressSet, err := s.addressesToQueryCached(start, end)
	if err != nil {
		return false, err
	}

	getUsableAddressesResponse, err := s.backgroundRPCClient.GetUsableAddresses(addressSet.strings())
	if err != nil {
		if errors.Is(err, router.ErrRouteClosed) {
			log.Warnf("Route is closed during GetUsableAddresses; ignoring and skipping sync cycle.")
			return false, nil
		}
		return false, err
	}

	err = s.updateAddressesAndLastUsedIndexes(addressSet, getUsableAddressesResponse)
	if err != nil {
		if errors.Is(err, router.ErrRouteClosed) {
			log.Warnf("Route is closed during GetUsableAddresses; ignoring and skipping sync cycle.")
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (s *server) updateAddressesAndLastUsedIndexes(requestedAddressSet walletAddressSet,
	getUsableAddressesResponse *appmessage.GetUsableAddressesResponseMessage,
) error {
	// From the values the scans use: a discovery must raise a key chain's last used index even when the keys
	// file holds one no address can have, or that chain's recent window and rescan round never move.
	scannedExternalIndex, scannedInternalIndex := s.lastUsedIndexes()
	lastUsedExternalIndex, lastUsedInternalIndex := scannedExternalIndex, scannedInternalIndex
	var observedReceiveZero, observedChangeZero bool

	for _, address := range getUsableAddressesResponse.Addresses {
		walletAddress, ok := requestedAddressSet[address]
		if !ok {
			return errors.Errorf("Got result from address %s even though it wasn't requested", address)
		}

		s.addressSet[address] = walletAddress

		// An imported key is not at an index of the wallet's key chains.
		if walletAddress.imported != nil {
			continue
		}

		if walletAddress.keyChain == libhtnwallet.ExternalKeychain {
			observedReceiveZero = observedReceiveZero || walletAddress.index == 0
			if walletAddress.index > lastUsedExternalIndex {
				lastUsedExternalIndex = walletAddress.index
			}
			continue
		}

		observedChangeZero = observedChangeZero || walletAddress.index == 0
		if walletAddress.index > lastUsedInternalIndex {
			lastUsedInternalIndex = walletAddress.index
		}
	}

	// Only a discovery writes the keys file; an impossible recorded value stays until one replaces it.
	if lastUsedExternalIndex != scannedExternalIndex {
		err := s.keysFile.SetLastUsedExternalIndex(lastUsedExternalIndex)
		if err != nil {
			return err
		}
	}
	if lastUsedInternalIndex != scannedInternalIndex {
		err := s.keysFile.SetLastUsedInternalIndex(lastUsedInternalIndex)
		if err != nil {
			return err
		}
	}
	// Index zero is also the unset counter value. Keep actual discoveries in
	// memory so the scan can report gaps there without changing the keys file.
	s.observedReceiveZero = s.observedReceiveZero || observedReceiveZero
	s.observedChangeZero = s.observedChangeZero || observedChangeZero
	s.warnOfMLDSA44ScanGap()
	return nil
}

// mldsa44ScanGap describes incomplete cosigner exchanges or recorded use beyond
// the ML-DSA-44 hashes available to the scanner. Use beyond the pool can be entirely
// secp256k1 activity. ML-DSA-44-only history outside the pools is invisible to the scan.
// The caller holds s.lock.
func (s *server) mldsa44ScanGap() (gap string, partialExchange bool) {
	if s.keysFile.IsImported() {
		return "", false
	}

	var receiveCoverage, changeCoverage uint32
	addressType := "ML-DSA-44"
	if !s.isMultisig() {
		if pool := s.keysFile.MLDSA44; pool != nil {
			receiveCoverage = uint32(len(pool.ExternalPublicKeyHashes))
			changeCoverage = uint32(len(pool.InternalPublicKeyHashes))
		}
	} else {
		if !libhtnwallet.SupportsMLDSA44Multisig(len(s.keysFile.ExtendedPublicKeys), s.keysFile.MinimumSignatures) {
			return "", false
		}
		addressType = "ML-DSA-44 multisig"
		if len(s.keysFile.MLDSA44Cosigners) != 0 {
			var missing int
			var endings []string
			var missingLocal bool
			localKeys, localKeysKnown := s.keysFile.KnownLocalCosignerKeys()
			for i, publicKey := range s.keysFile.ExtendedPublicKeys {
				local := slices.Contains(localKeys, publicKey)
				pool := s.keysFile.MLDSA44Cosigners[publicKey]
				if pool == nil {
					missing++
					missingLocal = missingLocal || local
					// Never print a complete key, even if an invalid, short key was loaded.
					if len(publicKey) > 8 {
						endings = append(endings, "..."+publicKey[len(publicKey)-8:])
					}
					continue
				}
				partialExchange = partialExchange || (localKeysKnown && !local)
				receive := uint32(len(pool.ExternalPublicKeyHashes))
				change := uint32(len(pool.InternalPublicKeyHashes))
				if i == 0 {
					receiveCoverage, changeCoverage = receive, change
				} else {
					receiveCoverage, changeCoverage = min(receiveCoverage, receive), min(changeCoverage, change)
				}
			}
			if missing != 0 {
				details := ""
				if len(endings) != 0 {
					details = " (extended public key endings: " + strings.Join(endings, ", ") + ")"
				}
				gap = fmt.Sprintf("The ML-DSA-44 keys of %d of the wallet's %d cosigners are missing%s. "+
					"ML-DSA-44 multisig addresses can be neither built nor scanned until every pool is available. "+
					"Stop the daemon and use `htnwallet %s` for missing peer pools.",
					missing, len(s.keysFile.ExtendedPublicKeys), details, importMLDSA44KeysSubCmdName)
				if missingLocal {
					gap += fmt.Sprintf(" Generate any missing local pools with `htnwallet %s --count <n>` "+
						"using n above the highest index to recover.", generateMLDSA44KeysSubCmdName)
				} else if !localKeysKnown {
					gap += fmt.Sprintf(" If a local pool is missing, run `htnwallet %s --count <n>` "+
						"using n above the highest index to recover.", generateMLDSA44KeysSubCmdName)
				}
				return gap, partialExchange
			}
		}
	}

	var gaps []string
	lastReceive, lastChange := s.lastUsedIndexes()
	for _, chain := range []struct {
		name               string
		lastUsed, coverage uint32
		observedZero       bool
	}{
		{"receive", lastReceive, receiveCoverage, s.observedReceiveZero},
		{"change", lastChange, changeCoverage, s.observedChangeZero},
	} {
		if (chain.lastUsed != 0 || chain.observedZero) && chain.lastUsed >= chain.coverage {
			gaps = append(gaps, fmt.Sprintf("%s %s addresses from index %d on are not scanned (last used %s index %d)",
				chain.name, addressType, chain.coverage, chain.name, chain.lastUsed))
		}
	}
	if len(gaps) == 0 {
		return "", false
	}

	gap = strings.Join(gaps, "; ") + ". Coins there are missed if those addresses were used. " +
		"Recovery needs a known highest index or longer pools from backup."
	if len(s.keysFile.EncryptedMnemonics) != 0 {
		gap += fmt.Sprintf(" Stop the daemon and run `htnwallet %s --count <n>` with n above the highest index to recover.",
			generateMLDSA44KeysSubCmdName)
	}
	if s.isMultisig() {
		gap += fmt.Sprintf(" Import longer pools for the missing ranges with `htnwallet %s`.",
			importMLDSA44KeysSubCmdName)
	}
	return gap, false
}

// warnOfMLDSA44ScanGap logs at most once per daemon run. A partial exchange is a warning
// if ML-DSA-44 was active at startup. The caller holds s.lock.
func (s *server) warnOfMLDSA44ScanGap() {
	if s.warnedOfMLDSA44ScanGap {
		return
	}
	gap, partialExchange := s.mldsa44ScanGap()
	if gap == "" {
		return
	}
	s.warnedOfMLDSA44ScanGap = true
	if partialExchange && s.mldsa44ActiveAtStartup {
		log.Warnf("%s", gap)
		return
	}
	log.Infof("%s", gap)
}

func (s *server) usedOutpointHasExpired(outpointBroadcastTime time.Time) bool {
	// If the node returns a UTXO we previously attempted to spend and enough time has passed, we assume
	// that the network rejected or lost the previous transaction and allow a reuse. We set this time
	// interval to 60 minutes to avoid double-spend attempts with transactions still in mempool.
	// We also verify that a full refresh UTXO operation started after this time point and has already
	// completed, in order to make sure that indeed this state reflects a state obtained following the required wait time.
	return s.startTimeOfLastCompletedRefresh.After(outpointBroadcastTime.Add(60 * time.Minute))
}

// updateUTXOSet clears the current UTXO set, and re-fills it with the given entries
func (s *server) updateUTXOSet(entries []*appmessage.UTXOsByAddressesEntry, mempoolEntries []*appmessage.MempoolEntryByAddress, refreshStart time.Time) error {
	utxos := make([]*walletUTXO, 0, len(entries))

	exclude := make(map[appmessage.RPCOutpoint]struct{})
	for _, entriesByAddress := range mempoolEntries {
		for _, entry := range entriesByAddress.Sending {
			for _, input := range entry.Transaction.Inputs {
				exclude[*input.PreviousOutpoint] = struct{}{}
			}
		}
	}

	for _, entry := range entries {
		if _, ok := exclude[*entry.Outpoint]; ok {
			continue
		}

		outpoint, err := appmessage.RPCOutpointToDomainOutpoint(entry.Outpoint)
		if err != nil {
			return err
		}

		utxoEntry, err := appmessage.RPCUTXOEntryToUTXOEntry(entry.UTXOEntry)
		if err != nil {
			return err
		}

		// The caller holds s.lock, which every writer of addressSet takes. The one unlocked call is syncLoop's
		// initial refresh, which runs before firstSyncDone lets any request reach a writer.
		address, ok := s.addressSet[entry.Address]
		if !ok {
			return errors.Errorf("Got result from address %s even though it wasn't requested", entry.Address)
		}
		utxos = append(utxos, &walletUTXO{
			Outpoint:  outpoint,
			UTXOEntry: utxoEntry,
			address:   address,
		})
	}

	sort.Slice(utxos, func(i, j int) bool { return utxos[i].UTXOEntry.Amount() > utxos[j].UTXOEntry.Amount() })
	s.startTimeOfLastCompletedRefresh = refreshStart
	s.utxosSortedByAmount = utxos
	// log.Infof("Updating %d UTXOs", len(s.utxosSortedByAmount))

	// Cleanup expired used outpoints to avoid a memory leak
	for outpoint, broadcastTime := range s.usedOutpoints {
		if s.usedOutpointHasExpired(broadcastTime) {
			delete(s.usedOutpoints, outpoint)
		}
	}

	return nil
}

func (s *server) refreshUTXOs(limit uint32) error {
	refreshStart := time.Now()

	// The caller holds s.lock, which every writer of addressSet takes. The one unlocked call is syncLoop's
	// initial refresh, which runs before firstSyncDone lets any request reach a writer.
	addresses := s.addressSet.strings()
	// It's important to check the mempool before calling `GetUTXOsByAddresses`:
	// If we would do it the other way around an output can be spent in the mempool
	// and not in consensus, and between the calls its spending transaction will be
	// added to consensus and removed from the mempool, so `getUTXOsByAddressesResponse`
	// will include an obsolete output.
	// Include both transaction-pool and orphan-pool spends so we can exclude UTXOs that are
	// already being spent anywhere in the node mempool before building a new transaction.
	mempoolEntriesByAddresses, err := s.backgroundRPCClient.GetMempoolEntriesByAddresses(addresses, true, false)
	if err != nil {
		if errors.Is(err, router.ErrRouteClosed) {
			log.Warnf("Route is closed during GetUsableAddresses; ignoring and skipping sync cycle.")
			return nil
		}
		return err
	}

	getUTXOsByAddressesResponse, err := s.backgroundRPCClient.GetUTXOsByAddresses(addresses, limit)
	if err != nil {
		if errors.Is(err, router.ErrRouteClosed) {
			log.Warnf("Route is closed during GetUsableAddresses; ignoring and skipping sync cycle.")
			return nil
		}
		return err
	}
	// log.Infof("Got %d UTXOs from node", len(getUTXOsByAddressesResponse.Entries))

	err = s.updateUTXOSet(getUTXOsByAddressesResponse.Entries, mempoolEntriesByAddresses.Entries, refreshStart)
	if err != nil {
		return err
	}
	s.limitOfLastCompletedRefresh = limit
	s.utxoSetIsStale = false
	return nil
}

// newForceSyncChan makes the channel forceSync and syncLoop share. It holds one pending request: forceSync
// drops a request only while one is pending, so with an unbuffered channel it would drop every request made
// while syncLoop is busy.
func newForceSyncChan() chan struct{} {
	return make(chan struct{}, 1)
}

// forceSync asks syncLoop for a sync now. Requests made before syncLoop takes the pending one are one sync.
// The channel used to be unbuffered, so its length was always 0 and every broadcast request queued a
// goroutine and a sync of its own.
func (s *server) forceSync() {
	select {
	case s.forceSyncChan <- struct{}{}:
	default: // A sync is already pending.
	}
}

func (s *server) isSynced() bool {
	return s.nextSyncStartIndex > s.maxUsedIndex() && s.firstSyncDone.Load()
}

func (s *server) formatSyncStateReport() string {
	maxUsedIndex := max(s.nextSyncStartIndex, s.maxUsedIndex())

	if s.nextSyncStartIndex < s.maxUsedIndex() {
		return fmt.Sprintf("scanned %d out of %d addresses (%.2f%%)",
			s.nextSyncStartIndex, maxUsedIndex, float64(s.nextSyncStartIndex)*100.0/float64(maxUsedIndex))
	}
	return "loading the wallet UTXO set"
}

func (s *server) updateSyncingProgressLog(currProcessedAddresses, currMaxUsedAddresses uint32) {
	if currMaxUsedAddresses > s.maxUsedAddressesForLog {
		s.maxUsedAddressesForLog = currMaxUsedAddresses
		if s.isLogFinalProgressLineShown {
			log.Infof("An additional set of previously used addresses found, processing...")
			s.maxProcessedAddressesForLog = 0
			s.isLogFinalProgressLineShown = false
		}
	}

	if currProcessedAddresses > s.maxProcessedAddressesForLog {
		s.maxProcessedAddressesForLog = currProcessedAddresses
	}

	if s.maxProcessedAddressesForLog >= s.maxUsedAddressesForLog {
		if !s.isLogFinalProgressLineShown {
			log.Infof("Finished scanning recent addresses")
			s.isLogFinalProgressLineShown = true
		}
	} else {
		percentProcessed := float64(s.maxProcessedAddressesForLog) / float64(s.maxUsedAddressesForLog) * 100.0

		log.Infof("%d addresses of %d processed (%.2f%%)...",
			s.maxProcessedAddressesForLog, s.maxUsedAddressesForLog, percentProcessed)
	}
}
