package mempool

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
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

	ids := rtl.extractSenderAddresses(tx)
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

	ids := rtl.extractSenderAddresses(tx)
	if len(ids) != 1 {
		t.Fatalf("expected exactly 1 sender identifier, got %d (%v)", len(ids), ids)
	}
	if ids[0] != innerAddr.EncodeAddress() {
		t.Fatalf("expected canonical sender %q, got %q", innerAddr.EncodeAddress(), ids[0])
	}
}
