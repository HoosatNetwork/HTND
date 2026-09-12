package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

func compoundTestTransaction(inputCount int, mass uint64) *externalapi.DomainTransaction {
	inputs := make([]*externalapi.DomainTransactionInput, inputCount)
	for i := range inputs {
		inputs[i] = &externalapi.DomainTransactionInput{}
	}
	return &externalapi.DomainTransaction{Inputs: inputs, Mass: mass}
}

// A consolidation is recognised by its shape - many inputs, or a large mass - and the recognition must
// not depend on whether throttling is switched on. The throttle decides whether to count a transaction
// against its sender's budget; the shape decides whether the mempool keeps it, and an operator turning
// the first off must not quietly turn the second off with it.
func TestLooksLikeCompoundTransactionIsIndependentOfTheThrottle(t *testing.T) {
	config := DefaultConfig(&dagconfig.TestnetParams)
	config.CompoundTxMinInputsThreshold = 21

	consolidation := compoundTestTransaction(21, 1000)
	ordinary := compoundTestTransaction(2, 1000)
	heavy := compoundTestTransaction(1, MaximumStandardTransactionMass/2+1)

	for _, throttling := range []bool{true, false} {
		config.CompoundTxRateLimitEnabled = throttling
		limiter := newCompoundTxRateLimiter(config)

		if !limiter.looksLikeCompoundTransaction(consolidation) {
			t.Errorf("throttling=%t: a transaction at the input threshold is a consolidation", throttling)
		}
		if !limiter.looksLikeCompoundTransaction(heavy) {
			t.Errorf("throttling=%t: a transaction over half the standard mass is a consolidation", throttling)
		}
		if limiter.looksLikeCompoundTransaction(ordinary) {
			t.Errorf("throttling=%t: a two-input transaction is not a consolidation", throttling)
		}
		if limiter.looksLikeCompoundTransaction(nil) {
			t.Errorf("throttling=%t: no transaction is not a consolidation", throttling)
		}

		// The throttle's own question still answers only when throttling is on.
		if counted := limiter.isCompoundTransaction(consolidation); counted != throttling {
			t.Errorf("throttling=%t: rate limiter counts the consolidation = %t, want %t", throttling, counted, throttling)
		}
	}
}

// The flag is only ever raised, never lowered: a transaction the caller already marked high priority
// stays that way whatever its shape.
func TestRaisePriorityIfCompoundOnlyRaises(t *testing.T) {
	config := DefaultConfig(&dagconfig.TestnetParams)
	config.CompoundTxMinInputsThreshold = 21
	mp := &mempool{config: config, compoundTxRateLimiter: newCompoundTxRateLimiter(config)}

	consolidation := compoundTestTransaction(21, 1000)
	ordinary := compoundTestTransaction(2, 1000)

	if !mp.raisePriorityIfCompound(consolidation, false) {
		t.Error("a relayed consolidation must be raised to high priority, or the mempool expires it")
	}
	if mp.raisePriorityIfCompound(ordinary, false) {
		t.Error("an ordinary relayed transaction keeps the ordinary lifetime")
	}
	if !mp.raisePriorityIfCompound(ordinary, true) {
		t.Error("priority already granted by the caller must not be taken away")
	}
}

// A zero threshold is an operator-reachable flag value (--compound-tx-inputs-threshold=0) and it
// satisfies "inputs >= threshold" for every transaction. The rate limiter is entitled to read that as
// "throttle everything". The priority rule is not entitled to read it as "nothing ever expires", which
// would switch off both expiry and eviction for the whole mempool.
func TestCompoundPriorityIgnoresADegenerateThreshold(t *testing.T) {
	config := DefaultConfig(&dagconfig.TestnetParams)
	config.CompoundTxMinInputsThreshold = 0
	config.CompoundTxRateLimitEnabled = true
	mp := &mempool{config: config, compoundTxRateLimiter: newCompoundTxRateLimiter(config)}

	ordinary := compoundTestTransaction(2, 1000)
	heavy := compoundTestTransaction(2, MaximumStandardTransactionMass/2+1)

	if mp.raisePriorityIfCompound(ordinary, false) {
		t.Error("a zero threshold must not make every transaction unexpirable")
	}
	if !mp.raisePriorityIfCompound(heavy, false) {
		t.Error("the mass rule still identifies a compound transaction when the threshold is degenerate")
	}

	// The rate limiter keeps the meaning it had before: with a zero threshold it counts everything.
	if !mp.compoundTxRateLimiter.isCompoundTransaction(ordinary) {
		t.Error("the throttle's own reading of a zero threshold must be left alone")
	}
}
