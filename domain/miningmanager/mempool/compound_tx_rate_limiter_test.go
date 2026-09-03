package mempool

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

func checkedIntFromUint64(value uint64) int {
	parsedValue, err := strconv.ParseInt(strconv.FormatUint(value, 10), 10, 64)
	if err != nil {
		panic(err)
	}
	return int(parsedValue)
}

// Test that exactly MaxCompoundTxPerAddressPerMinute submissions within the 1-minute window
// cause the next (11th) to be rate-limited, and that when one falls out of the window,
// submissions are accepted again.
func TestCompoundTxRateLimiter_WindowAndLimit(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.MaxCompoundTxPerAddressPerMinute = 10
	cfg.CompoundTxRateLimitWindowMinutes = 1

	rtl := newCompoundTxRateLimiter(cfg)
	addr := "hoosat:qptestaddressxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	tracker := rtl.getOrCreateTracker(addr)

	base := time.Now()

	// Seed 10 submissions within the last minute
	tracker.mutex.Lock()
	for i := 0; i < checkedIntFromUint64(cfg.MaxCompoundTxPerAddressPerMinute); i++ {
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{
			timestamp: base.Add(-30*time.Second + time.Duration(i)*time.Second),
			txID:      "txid",
		})
	}
	tracker.mutex.Unlock()

	// After cleanup, all 10 remain within window
	rtl.cleanupOldSubmissions(tracker)

	if ok := rtl.checkRateLimit(addr); ok {
		t.Fatalf("expected address to be rate-limited with 10 submissions in window, but it was allowed")
	}

	// Move the oldest one beyond the 1-minute window
	tracker.mutex.Lock()
	if len(tracker.submissions) != checkedIntFromUint64(cfg.MaxCompoundTxPerAddressPerMinute) {
		t.Fatalf("unexpected seeded submissions count: got %d, want %d", len(tracker.submissions), cfg.MaxCompoundTxPerAddressPerMinute)
	}
	tracker.submissions[0].timestamp = base.Add(-61 * time.Second)
	tracker.mutex.Unlock()

	rtl.cleanupOldSubmissions(tracker)

	if ok := rtl.checkRateLimit(addr); !ok {
		t.Fatalf("expected address to be allowed after one submission expired from the window, but it was rate-limited")
	}
}

// Test that recording with a past timestamp doesn't affect current window
func TestCompoundTxRateLimiter_RecordAtPastTime(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.MaxCompoundTxPerAddressPerMinute = 10
	cfg.CompoundTxRateLimitWindowMinutes = 1

	rtl := newCompoundTxRateLimiter(cfg)
	addr := "hoosat:qptestaddressxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	tracker := rtl.getOrCreateTracker(addr)

	// Record 10 submissions 2 minutes ago; they should be cleaned out and not count now
	past := time.Now().Add(-2 * time.Minute)
	tracker.mutex.Lock()
	for range 10 {
		tracker.submissions = append(tracker.submissions, compoundTxSubmission{timestamp: past, txID: "old"})
	}
	tracker.mutex.Unlock()

	rtl.cleanupOldSubmissions(tracker)
	if ok := rtl.checkRateLimit(addr); !ok {
		t.Fatalf("expected address to be allowed after past submissions expired, but it was rate-limited")
	}
}

func TestCompoundTxRateLimiterExtractSenderAddresses_FallbackToScriptHash(t *testing.T) {
	config := &Config{
		CompoundTxRateLimitEnabled: true,
		DAGParams:                  &dagconfig.MainnetParams,
	}
	rtl := newCompoundTxRateLimiter(config)

	// Construct a malformed P2PKH script: it parses and is recognized as P2PKH,
	// but contains a pubkey-hash of invalid length, so ExtractScriptPubKeyAddress
	// returns (PubKeyHashTy, nil, nil).
	malformedPubKeyHash := make([]byte, 31)
	script, err := txscript.NewScriptBuilder().
		AddOp(txscript.OpDup).
		AddOp(txscript.OpBlake2b).
		AddData(malformedPubKeyHash).
		AddOp(txscript.OpEqualVerify).
		AddOp(txscript.OpCheckSig).
		Script()
	if err != nil {
		t.Fatalf("unexpected script builder error: %v", err)
	}

	tx := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			nil, // ensure nil inputs are ignored safely
			{
				UTXOEntry: &testUTXOEntry{scriptPublicKey: &externalapi.ScriptPublicKey{Script: script, Version: 0}},
			},
		},
	}

	ids, _ := rtl.extractSenderAddresses(tx)
	if len(ids) != 1 {
		t.Fatalf("expected exactly 1 sender identifier, got %d (%v)", len(ids), ids)
	}
	if !strings.HasPrefix(ids[0], "spkblake2b:") {
		t.Fatalf("expected fallback identifier with prefix 'spkblake2b:', got %q", ids[0])
	}
}

func TestCompoundTxRateLimiterExtractSenderAddresses_P2SHCanonicalizesToRedeemScriptAddress(t *testing.T) {
	config := &Config{
		CompoundTxRateLimitEnabled: true,
		DAGParams:                  &dagconfig.MainnetParams,
	}
	rtl := newCompoundTxRateLimiter(config)

	pubKeyHash := make([]byte, 32)
	for i := range pubKeyHash {
		pubKeyHash[i] = 0x11
	}

	redeemScript, err := txscript.NewScriptBuilder().
		AddOp(txscript.OpDup).
		AddOp(txscript.OpBlake2b).
		AddData(pubKeyHash).
		AddOp(txscript.OpEqualVerify).
		AddOp(txscript.OpCheckSig).
		Script()
	if err != nil {
		t.Fatalf("unexpected redeemScript builder error: %v", err)
	}

	p2shScript, err := txscript.PayToScriptHashScript(redeemScript)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %v", err)
	}

	signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
	if err != nil {
		t.Fatalf("PayToScriptHashSignatureScript: %v", err)
	}

	innerClass, innerAddr, err := txscript.ExtractScriptPubKeyAddress(&externalapi.ScriptPublicKey{Script: redeemScript, Version: 0}, config.DAGParams)
	if err != nil {
		t.Fatalf("ExtractScriptPubKeyAddress(inner): %v", err)
	}
	if innerAddr == nil || innerClass != txscript.PubKeyHashTy {
		t.Fatalf("unexpected inner extraction: class=%v addr=%v", innerClass, innerAddr)
	}

	tx := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{
				SignatureScript: signatureScript,
				UTXOEntry:       &testUTXOEntry{scriptPublicKey: &externalapi.ScriptPublicKey{Script: p2shScript, Version: 0}},
			},
		},
	}

	ids, _ := rtl.extractSenderAddresses(tx)
	if len(ids) != 1 {
		t.Fatalf("expected exactly 1 sender identifier, got %d (%v)", len(ids), ids)
	}
	if ids[0] != innerAddr.EncodeAddress() {
		t.Fatalf("expected canonical sender %q, got %q", innerAddr.EncodeAddress(), ids[0])
	}
}

// TestCompoundTxRateLimiter_CleanupWithOutOfOrderTimestamps pins the fix for the pruning bug that
// made the limiter permanently sticky. cleanupOldSubmissions used to scan for the first entry newer
// than the cutoff and drop everything before it, which is only correct when submissions is sorted
// ascending. recordTransactionAt appends promoted orphans with their ORIGINAL arrival time, so an
// expired entry can land after fresh ones - and the old scan then stopped at index 0 and pruned
// nothing, leaving the address rate-limited long after its window had passed.
func TestCompoundTxRateLimiter_CleanupWithOutOfOrderTimestamps(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.MaxCompoundTxPerAddressPerMinute = 2
	cfg.CompoundTxRateLimitWindowMinutes = 1

	rtl := newCompoundTxRateLimiter(cfg)
	addr := "hoosat:qpoutofordertestxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	tracker := rtl.getOrCreateTracker(addr)

	base := time.Now()
	// A fresh entry first, then an expired one appended after it - the order recordTransactionAt
	// produces when an orphan submitted minutes ago is finally promoted.
	tracker.mutex.Lock()
	tracker.submissions = append(tracker.submissions,
		compoundTxSubmission{timestamp: base.Add(-5 * time.Second), txID: "fresh"},
		compoundTxSubmission{timestamp: base.Add(-10 * time.Minute), txID: "expired"},
	)
	tracker.mutex.Unlock()

	rtl.cleanupOldSubmissions(tracker)

	tracker.mutex.RLock()
	remaining := make([]string, 0, len(tracker.submissions))
	for _, submission := range tracker.submissions {
		remaining = append(remaining, submission.txID)
	}
	tracker.mutex.RUnlock()

	if len(remaining) != 1 || remaining[0] != "fresh" {
		t.Fatalf("expected only the in-window submission to survive cleanup, got %v", remaining)
	}
	if !rtl.checkRateLimit(addr) {
		t.Fatalf("address should be allowed again once the expired submission is pruned")
	}
}

// TestCompoundTxRateLimiter_UnattributableInputsFailClosed pins that a compound transaction whose
// inputs carry no UTXO entries is rejected rather than waved through. extractSenderAddresses used
// to skip such inputs silently; with every input skipped it returned an empty address set, so
// isRateLimited found nothing over its limit and reported "not limited" - the limiter switching
// itself off for exactly the transactions it could not understand, with no log line.
func TestCompoundTxRateLimiter_UnattributableInputsFailClosed(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	cfg.CompoundTxMinInputsThreshold = 2

	rtl := newCompoundTxRateLimiter(cfg)

	tx := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{UTXOEntry: nil},
			{UTXOEntry: nil},
			{UTXOEntry: nil},
		},
	}

	addresses, unattributed := rtl.extractSenderAddresses(tx)
	if len(addresses) != 0 {
		t.Fatalf("expected no attributable addresses, got %v", addresses)
	}
	if unattributed != 3 {
		t.Fatalf("expected all 3 inputs reported as unattributable, got %d", unattributed)
	}

	isLimited, limitedAddresses := rtl.isRateLimited(tx)
	if !isLimited {
		t.Fatalf("a compound transaction with no attributable sender must fail closed, not bypass the limiter")
	}
	if len(limitedAddresses) == 0 {
		t.Fatalf("expected a reason to be reported alongside the rejection")
	}
}

// TestCompoundTxRateLimiter_ExtractSenderAddressesIsDeterministic pins that the bucket set does not
// depend on Go's randomized map iteration order.
func TestCompoundTxRateLimiter_ExtractSenderAddressesIsDeterministic(t *testing.T) {
	cfg := DefaultConfig(&dagconfig.TestnetParams)
	cfg.CompoundTxRateLimitEnabled = true
	rtl := newCompoundTxRateLimiter(cfg)

	inputs := make([]*externalapi.DomainTransactionInput, 0, 8)
	for i := range 8 {
		script := []byte{0xaa, 0x20, byte(i)}
		inputs = append(inputs, &externalapi.DomainTransactionInput{
			UTXOEntry: utxo.NewUTXOEntry(1000, &externalapi.ScriptPublicKey{Script: script, Version: 0}, false, 1),
		})
	}
	tx := &externalapi.DomainTransaction{Inputs: inputs}

	first, _ := rtl.extractSenderAddresses(tx)
	for range 20 {
		next, _ := rtl.extractSenderAddresses(tx)
		if strings.Join(first, ",") != strings.Join(next, ",") {
			t.Fatalf("extractSenderAddresses is not deterministic:\nfirst: %v\nnext:  %v", first, next)
		}
	}
}
