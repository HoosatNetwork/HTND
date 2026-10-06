package transactionvalidator

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// prewarmInputBudget caps how many inputs one PrewarmScriptCaches call verifies. The Schnorr and
// ECDSA caches hold sigCacheSize entries with random eviction, so prewarming more than a fraction of
// that would evict the earlier results before the sequential pass reads them back.
const prewarmInputBudget = sigCacheSize / 2

// PrewarmScriptCaches verifies the scripts of transactions on every core, only to record the
// signatures that verify in the signature caches. Failures are ignored: the caller's sequential
// validation reaches the same transactions and reports them itself. Every input must already carry
// its UTXO entry; a transaction with a missing one simply verifies the inputs it has.
//
// The caches can only answer "this exact signature hash, key and signature verified", so warming
// them with a transaction validated against a different UTXO view than the sequential pass uses
// cannot change any verdict - it only fails to help.
func (v *transactionValidator) PrewarmScriptCaches(transactions []*externalapi.DomainTransaction, povDAAScore uint64) {
	inputs := 0
	for i, transaction := range transactions {
		inputs += len(transaction.Inputs)
		if inputs > prewarmInputBudget {
			transactions = transactions[:i]
			break
		}
	}
	if len(transactions) == 0 {
		return
	}

	flags := v.scriptFlagsForDAAScore(povDAAScore)
	workers := min(runtime.GOMAXPROCS(0), len(transactions))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(transactions) {
					return
				}
				_ = v.validateTransactionScripts(transactions[i], flags)
			}
		}()
	}
	wg.Wait()
}
