package server

import (
	"fmt"
	"maps"
	"slices"
	"sort"
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
	err := s.collectRecentAddresses()
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
		select {
		case <-ticker.C:
		case <-s.forceSyncChan:
		}

		err := s.sync()
		if err != nil {
			return err
		}
		s.checkPendingBroadcasts(time.Now())
	}
}

func (s *server) sync() error {
	err := s.collectFarAddresses()
	if err != nil {
		return err
	}

	err = s.collectRecentAddresses()
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
// addresses it goes over all the cosigners and add their addresses
// for each key chain. The batch starting at index 0, which every sync rescans, also holds the
// addresses of the imported keys.
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

// collectRecentAddresses collects addresses from used addresses until
// the address with the index of the last used address + numIndexesToQueryForRecentAddresses.
// collectRecentAddresses scans addresses in batches of numIndexesToQueryForRecentAddresses,
// and releases the lock between scans.
//
// It scans the batches recentBatchIsDue names: every batch on the first sync after the daemon starts, and
// from then on only the ones around the last used index of each key chain. It used to scan every batch on
// every sync, so a single-key wallet with 75,000 used indexes sent the node 76 calls with about 456,000
// address strings on every sync. collectOldAddresses rescans the batches skipped here, one per sync.
func (s *server) collectRecentAddresses() error {
	index := uint32(0)
	maxUsedIndex := s.maxUsedIndexWithLock()
	for ; index < maxUsedIndex+numIndexesToQueryForRecentAddresses; index += numIndexesToQueryForRecentAddresses {
		if !s.recentBatchIsDueWithLock(index) {
			continue
		}
		scanned, err := s.collectAddressesWithLock(index, index+numIndexesToQueryForRecentAddresses)
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
	if index > s.nextSyncStartIndex {
		s.nextSyncStartIndex = index
	}
	s.lock.Unlock()

	return nil
}

func (s *server) recentBatchIsDueWithLock(start uint32) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.recentBatchIsDue(start)
}

// recentBatchIsDue returns whether collectRecentAddresses scans the batch starting at start on this sync:
//   - a batch not scanned since the daemon started, so that the first sync scans every used index;
//   - the batch holding the last used index of a key chain, the one before it and the one after it, so that
//     the newest numIndexesToQueryForRecentAddresses addresses of each key chain, and the ones about to be
//     handed out, are scanned on every sync, as they always were;
//   - the batch starting at index 0 of a wallet with imported keys, which holds their addresses.
//
// The caller holds s.lock, and start is at most one batch above scanIndexLimit.
func (s *server) recentBatchIsDue(start uint32) bool {
	end := start + numIndexesToQueryForRecentAddresses
	if end > s.nextSyncStartIndex {
		return true
	}
	if start == 0 && len(s.importedAddresses) > 0 {
		return true
	}
	external, internal := s.lastUsedIndexes()
	for _, lastUsedIndex := range []uint32{external, internal} {
		if end+numIndexesToQueryForRecentAddresses > lastUsedIndex &&
			start < lastUsedIndex+numIndexesToQueryForRecentAddresses {
			return true
		}
	}
	return false
}

// collectOldAddresses rescans one batch of the used addresses collectRecentAddresses skips, and the next
// sync the batch after it. An address that was empty when the daemon started can receive coins at any time,
// so every used index has to be scanned again; taking one batch per sync keeps the work of a sync the same
// however many addresses the wallet has used.
//
// The batches are rescanned in rounds from index 0. A round ends where the last used index + 1000 was when
// it started (rescanRoundEnd); indexes used later wait for the next round. If the end moved with the last
// used index, a wallet using more than 1000 new indexes per sync would keep the cursor from ever wrapping
// back to the batches behind it. Coins reaching an address below the end of the round under way are found
// within one round: at most one sync for each numIndexesToQueryForRecentAddresses indexes up to that end,
// plus one - about two and a half minutes for 75,000. An address that has held a coin since the daemon
// started is in s.addressSet and does not wait for this.
//
// The node answers GetUsableAddresses from a cache shared by all its clients for up to
// usableAddressesCacheTTL, so the answer for a batch can predate a coin that arrived just before the scan,
// and the next round may be much longer than this one. So each batch scanned here is scanned once more
// oldBatchRecheckDelay later, unless the round comes back to it first; that answer is newer than the first
// scan. Every sync runs all the re-checks that are due, so a burst of forced syncs leaves no backlog. At
// most one is pending per batch, none is run for the cursor's batch or for a batch collectRecentAddresses
// scans, so a sync never asks about more batches than collectRecentAddresses alone used to.
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
		if s.recentBatchIsDue(start) {
			s.nextRescanIndex = start + numIndexesToQueryForRecentAddresses
			continue
		}
		scanned, err := s.collectAddresses(start, start+numIndexesToQueryForRecentAddresses)
		if err != nil || !scanned {
			// A skipped scan is retried by the next sync; moving on would leave the batch a round late.
			return false, err
		}
		s.nextRescanIndex = start + numIndexesToQueryForRecentAddresses
		// This scan is newer than a re-check of the batch still pending from the round before.
		s.oldBatchRechecks = slices.DeleteFunc(s.oldBatchRechecks, func(recheck oldBatchRecheck) bool {
			return recheck.start == start
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
	// oldBatchRecheckDelay is when collectOldAddresses scans a batch again: past the node's cache, with a
	// margin for the clocks of the wallet and the node differing.
	oldBatchRecheckDelay = usableAddressesCacheTTL + 2*time.Second
)

type oldBatchRecheck struct {
	start     uint32
	notBefore time.Time
}

func (s *server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// recheckOldBatch scans the first queued old batch whose re-check is due, dropping on the way the ones
// collectRecentAddresses scans on every sync anyway. It reports whether it scanned one; after a closed
// route it reports false and keeps the batch queued.
func (s *server) recheckOldBatch() (scanned bool, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	for len(s.oldBatchRechecks) > 0 && !s.clock().Before(s.oldBatchRechecks[0].notBefore) {
		start := s.oldBatchRechecks[0].start
		if s.recentBatchIsDue(start) {
			s.oldBatchRechecks = s.oldBatchRechecks[1:]
			continue
		}
		scanned, err := s.collectAddresses(start, start+numIndexesToQueryForRecentAddresses)
		if err != nil || !scanned {
			return false, err
		}
		s.oldBatchRechecks = s.oldBatchRechecks[1:]
		return true, nil
	}
	return false, nil
}

func (s *server) collectAddressesWithLock(start, end uint32) (scanned bool, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.collectAddresses(start, end)
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
			if walletAddress.index > lastUsedExternalIndex {
				lastUsedExternalIndex = walletAddress.index
			}
			continue
		}

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
		return s.keysFile.SetLastUsedInternalIndex(lastUsedInternalIndex)
	}
	return nil
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

func (s *server) forceSync() {
	// Technically if two callers check the `if` simultaneously they will both spawn a
	// goroutine, but we don't care about the small redundancy in such a rare case.
	if len(s.forceSyncChan) == 0 {
		go func() {
			s.forceSyncChan <- struct{}{}
		}()
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
