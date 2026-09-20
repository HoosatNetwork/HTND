package mempool

import (
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
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
}

// compoundTxRateLimiter handles rate limiting for compound transactions per address
type compoundTxRateLimiter struct {
	config         *Config
	addressTracker map[string]*addressTxTracker
	globalMutex    sync.RWMutex
}

// newCompoundTxRateLimiter creates a new compound transaction rate limiter
func newCompoundTxRateLimiter(config *Config) *compoundTxRateLimiter {
	return &compoundTxRateLimiter{
		config:         config,
		addressTracker: make(map[string]*addressTxTracker),
		globalMutex:    sync.RWMutex{},
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

	for _, input := range transaction.Inputs {
		if input.UTXOEntry != nil && input.UTXOEntry.ScriptPublicKey() != nil {
			_, extractedAddress, err := txscript.ExtractScriptPubKeyAddress(
				input.UTXOEntry.ScriptPublicKey(), rtl.config.DAGParams)
			if err != nil {
				continue
			}
			addresses[extractedAddress.EncodeAddress()] = true
		}
	}

	// Convert map keys to slice
	result := make([]string, 0, len(addresses))
	for addr := range addresses {
		result = append(result, addr)
	}
	return result
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
				submissions: make([]compoundTxSubmission, 0),
				mutex:       sync.RWMutex{},
			}
			rtl.addressTracker[address] = tracker
		}
		rtl.globalMutex.Unlock()
	}

	return tracker
}

// cleanupOldSubmissions removes submissions older than the rate limit window
func (rtl *compoundTxRateLimiter) cleanupOldSubmissions(tracker *addressTxTracker) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	windowDuration := time.Duration(rtl.config.CompoundTxRateLimitWindowMinutes) * time.Minute
	cutoff := time.Now().Add(-windowDuration)

	// Find the first submission within the window
	validIndex := 0
	for i, submission := range tracker.submissions {
		if submission.timestamp.After(cutoff) {
			validIndex = i
			break
		}
		validIndex = i + 1
	}

	// Keep only recent submissions
	if validIndex > 0 {
		tracker.submissions = tracker.submissions[validIndex:]
	}
}

// checkRateLimit checks if the address has exceeded the compound transaction rate limit
func (rtl *compoundTxRateLimiter) checkRateLimit(address string) bool {
	if !rtl.config.CompoundTxRateLimitEnabled {
		return true // Allow if rate limiting is disabled
	}

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

	addresses := rtl.extractSenderAddresses(transaction)
	rateLimitedAddresses := make([]string, 0)

	for _, address := range addresses {
		if !rtl.checkRateLimit(address) {
			rateLimitedAddresses = append(rateLimitedAddresses, address)
		}
	}

	return len(rateLimitedAddresses) > 0, rateLimitedAddresses
}
