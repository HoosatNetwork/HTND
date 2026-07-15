package mempool

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
)

// compoundTxSubmission represents a single compound transaction submission
type compoundTxSubmission struct {
	timestamp time.Time
	txID      string
}

// addressTxTracker tracks compound transaction submissions for a single address
type addressTxTracker struct {
	submissions []compoundTxSubmission
	mutex       sync.RWMutex
	lastSeen    time.Time
}

// compoundTxRateLimiter handles rate limiting for compound transactions per address
type compoundTxRateLimiter struct {
	config          *Config
	addressTracker  map[string]*addressTxTracker
	globalMutex     sync.RWMutex
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

func checkedDurationFromUint64Minutes(value uint64) (time.Duration, error) {
	parsedValue, err := strconv.ParseInt(strconv.FormatUint(value, 10), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(parsedValue) * time.Minute, nil
}

// newCompoundTxRateLimiter creates a new compound transaction rate limiter
func newCompoundTxRateLimiter(config *Config) *compoundTxRateLimiter {
	cleanupInterval := 5 * time.Minute
	if config.CompoundTxRateLimitWindowMinutes > 0 {
		// Run cleanup roughly 2-3 times per window
		cleanupInterval = time.Duration(config.CompoundTxRateLimitWindowMinutes) * time.Minute / 3
		if cleanupInterval < 2*time.Minute {
			cleanupInterval = 2 * time.Minute
		}
	}

	return &compoundTxRateLimiter{
		config:          config,
		addressTracker:  make(map[string]*addressTxTracker),
		globalMutex:     sync.RWMutex{},
		lastCleanup:     time.Now(),
		cleanupInterval: cleanupInterval,
	}
}

// isCompoundTransaction determines if a transaction should be considered a compound transaction
// based on the number of inputs and transaction characteristics
func (rtl *compoundTxRateLimiter) isCompoundTransaction(transaction *externalapi.DomainTransaction) bool {
	if !rtl.config.CompoundTxRateLimitEnabled {
		return false
	}

	// Consider transactions with many inputs as potential compound transactions
	if uint64(len(transaction.Inputs)) >= rtl.config.CompoundTxMinInputsThreshold {
		return true
	}

	// Also consider transactions with unusually high mass as compound
	if transaction.Mass > MaximumStandardTransactionMass/2 {
		return true
	}

	return false
}

// extractSenderAddresses extracts sender addresses from transaction inputs
func (rtl *compoundTxRateLimiter) extractSenderAddresses(transaction *externalapi.DomainTransaction) []string {
	addresses := make(map[string]bool) // Use map to avoid duplicates
	if transaction == nil {
		return nil
	}

	for _, input := range transaction.Inputs {
		if input == nil || input.UTXOEntry == nil {
			continue
		}

		scriptPublicKey := input.UTXOEntry.ScriptPublicKey()
		if scriptPublicKey == nil {
			continue
		}

		// Prefer standard address extraction (when possible) so the limiter groups by human-readable address.
		if rtl.config != nil && rtl.config.DAGParams != nil {
			scriptClass, extractedAddress, err := txscript.ExtractScriptPubKeyAddress(scriptPublicKey, rtl.config.DAGParams)
			if err == nil && extractedAddress != nil {
				// For P2SH spends, try to canonicalize to the underlying redeemScript address (e.g. P2SH-P2PKH)
				// to avoid bypassing per-address limits by switching between address encodings.
				if scriptClass == txscript.ScriptHashTy {
					if scriptHashAddr, ok := extractedAddress.(*util.AddressScriptHash); ok && scriptHashAddr != nil {
						pushes, err := txscript.PushedData(input.SignatureScript)
						if err == nil && len(pushes) > 0 {
							redeemScript := pushes[len(pushes)-1]
							if redeemScript != nil {
								redeemHash := util.HashBlake2b(redeemScript)
								if bytes.Equal(redeemHash, scriptHashAddr.ScriptAddress()) {
									redeemSPK := &externalapi.ScriptPublicKey{Script: redeemScript, Version: scriptPublicKey.Version}
									_, innerAddr, err := txscript.ExtractScriptPubKeyAddress(redeemSPK, rtl.config.DAGParams)
									if err == nil && innerAddr != nil {
										addresses[innerAddr.EncodeAddress()] = true
										continue
									}
								}
							}
						}
					}
				}

				addresses[extractedAddress.EncodeAddress()] = true
				continue
			}
		}

		// Fallback: if we can't extract an address (e.g. malformed P2PKH or missing DAG params),
		// use a stable hash of the ScriptPublicKey as the sender identifier.
		fallbackID := scriptPublicKeyIdentifier(scriptPublicKey)
		if fallbackID != "" {
			addresses[fallbackID] = true
		}
	}

	// Convert map keys to slice
	result := make([]string, 0, len(addresses))
	for addr := range addresses {
		result = append(result, addr)
	}
	return result
}

func scriptPublicKeyIdentifier(scriptPublicKey *externalapi.ScriptPublicKey) string {
	if scriptPublicKey == nil {
		return ""
	}

	buf := make([]byte, 2+len(scriptPublicKey.Script))
	binary.LittleEndian.PutUint16(buf[:2], scriptPublicKey.Version)
	copy(buf[2:], scriptPublicKey.Script)

	h := util.HashBlake2b(buf)
	return "spkblake2b:" + hex.EncodeToString(h)
}

// getOrCreateTracker gets or creates an address tracker for the given address
func (rtl *compoundTxRateLimiter) getOrCreateTracker(address string) *addressTxTracker {
	rtl.globalMutex.RLock()
	tracker, exists := rtl.addressTracker[address]
	rtl.globalMutex.RUnlock()

	if !exists {
		rtl.globalMutex.Lock()
		// Double-check after acquiring write lock
		if tracker, exists = rtl.addressTracker[address]; !exists {
			tracker = &addressTxTracker{
				submissions: make([]compoundTxSubmission, 0, 8),
				lastSeen:    time.Now(),
			}
			rtl.addressTracker[address] = tracker
		}
		rtl.globalMutex.Unlock()
	}

	// Update lastSeen (cheap under write lock)
	rtl.globalMutex.Lock()
	if t, ok := rtl.addressTracker[address]; ok {
		t.lastSeen = time.Now()
	}
	rtl.globalMutex.Unlock()

	return tracker
}

// cleanupOldSubmissions removes submissions older than the rate limit window
func (rtl *compoundTxRateLimiter) cleanupOldSubmissions(tracker *addressTxTracker) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	windowDuration, err := checkedDurationFromUint64Minutes(rtl.config.CompoundTxRateLimitWindowMinutes)
	if err != nil {
		panic(err)
	}
	cutoff := time.Now().Add(-windowDuration)

	validIndex := 0
	for i, submission := range tracker.submissions {
		if submission.timestamp.After(cutoff) {
			validIndex = i
			break
		}
		validIndex = i + 1
	}

	if validIndex > 0 {
		tracker.submissions = tracker.submissions[validIndex:]
	}
}

// cleanupEmptyTrackers removes trackers that have been empty for a long time.
// Called opportunistically from hot paths.
func (rtl *compoundTxRateLimiter) cleanupEmptyTrackers() {
	rtl.globalMutex.Lock()
	defer rtl.globalMutex.Unlock()

	if time.Since(rtl.lastCleanup) < rtl.cleanupInterval {
		return
	}
	rtl.lastCleanup = time.Now()

	windowDuration, _ := checkedDurationFromUint64Minutes(rtl.config.CompoundTxRateLimitWindowMinutes)
	cutoff := time.Now().Add(-windowDuration * 2) // conservative

	for addr, tracker := range rtl.addressTracker {
		tracker.mutex.RLock()
		empty := len(tracker.submissions) == 0
		lastSeen := tracker.lastSeen
		tracker.mutex.RUnlock()

		if empty && lastSeen.Before(cutoff) {
			delete(rtl.addressTracker, addr)
		}
	}
}

// checkRateLimit checks if the address has exceeded the compound transaction rate limit
func (rtl *compoundTxRateLimiter) checkRateLimit(address string) bool {
	if !rtl.config.CompoundTxRateLimitEnabled {
		return true // Allow if rate limiting is disabled
	}

	rtl.cleanupEmptyTrackers()

	tracker := rtl.getOrCreateTracker(address)
	rtl.cleanupOldSubmissions(tracker)

	tracker.mutex.RLock()
	currentCount := uint64(len(tracker.submissions))
	tracker.mutex.RUnlock()

	return currentCount < rtl.config.MaxCompoundTxPerAddressPerMinute
}

// recordTransaction records a compound transaction submission for rate limiting
func (rtl *compoundTxRateLimiter) recordTransaction(transaction *externalapi.DomainTransaction, txID string) {
	if !rtl.config.CompoundTxRateLimitEnabled || !rtl.isCompoundTransaction(transaction) {
		return
	}

	rtl.cleanupEmptyTrackers()

	addresses := rtl.extractSenderAddresses(transaction)

	for _, address := range addresses {
		tracker := rtl.getOrCreateTracker(address)
		rtl.cleanupOldSubmissions(tracker)

		tracker.mutex.Lock()
		// Deduplicate by txID for this address within the window
		for _, s := range tracker.submissions {
			if s.txID == txID {
				tracker.mutex.Unlock()
				goto nextAddress
			}
		}
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{
			timestamp: time.Now(),
			txID:      txID,
		})
		tracker.mutex.Unlock()
	nextAddress:
	}
}

// recordTransactionAt records a compound transaction with a specific timestamp (used for accepted orphans)
func (rtl *compoundTxRateLimiter) recordTransactionAt(transaction *externalapi.DomainTransaction, txID string, ts time.Time) {
	if !rtl.config.CompoundTxRateLimitEnabled || !rtl.isCompoundTransaction(transaction) {
		return
	}

	rtl.cleanupEmptyTrackers()

	addresses := rtl.extractSenderAddresses(transaction)

	for _, address := range addresses {
		tracker := rtl.getOrCreateTracker(address)
		rtl.cleanupOldSubmissions(tracker)

		tracker.mutex.Lock()
		for _, s := range tracker.submissions {
			if s.txID == txID {
				tracker.mutex.Unlock()
				goto nextAddress
			}
		}
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{
			timestamp: ts,
			txID:      txID,
		})
		tracker.mutex.Unlock()
	nextAddress:
	}
}

// isRateLimited checks if a transaction should be rate limited
func (rtl *compoundTxRateLimiter) isRateLimited(transaction *externalapi.DomainTransaction) (bool, []string) {
	if !rtl.config.CompoundTxRateLimitEnabled || !rtl.isCompoundTransaction(transaction) {
		return false, nil
	}

	rtl.cleanupEmptyTrackers()

	addresses := rtl.extractSenderAddresses(transaction)
	rateLimitedAddresses := make([]string, 0)

	for _, address := range addresses {
		if !rtl.checkRateLimit(address) {
			rateLimitedAddresses = append(rateLimitedAddresses, address)
		}
	}

	return len(rateLimitedAddresses) > 0, rateLimitedAddresses
}
