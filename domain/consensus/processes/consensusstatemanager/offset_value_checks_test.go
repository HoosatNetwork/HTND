package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// fakeMissingInputValidator stands in for the transaction validator on the missing-input path.
type fakeMissingInputValidator struct {
	model.TransactionValidator
	calls  int
	result error
	fee    uint64
}

func (f *fakeMissingInputValidator) ValidateTransactionWithMissingInputsAndPopulateFee(_ *model.StagingArea,
	tx *externalapi.DomainTransaction, _ *externalapi.DomainHash, _ uint64,
) error {
	f.calls++
	if f.result == nil {
		tx.StoreFee(f.fee)
	}
	return f.result
}

func TestOffsetModeValueChecksGate(t *testing.T) {
	tests := []struct {
		activation, daaScore uint64
		want                 bool
	}{
		{0, 0, true},
		{0, 231_301_175, true},
		{1_000, 999, false},
		{1_000, 1_000, true},
		{1_000, 1_001, true},
		{dagconfig.OffsetModeValueChecksNeverActive, 231_301_175, false},
		{dagconfig.OffsetModeValueChecksNeverActive, ^uint64(0) - 1, false},
	}
	for _, test := range tests {
		if got := offsetModeValueChecksActiveAt(test.activation, test.daaScore); got != test.want {
			t.Errorf("activation %d, DAA score %d: active=%t, want %t", test.activation, test.daaScore, got, test.want)
		}
	}
	if dagconfig.MainnetParams.OffsetModeValueChecksActivationDAAScore != dagconfig.OffsetModeValueChecksNeverActive {
		t.Errorf("mainnet must not activate the offset-mode value checks until a coordinated DAA score is chosen")
	}
	for _, params := range []*dagconfig.Params{&dagconfig.TestnetParams, &dagconfig.TestnetParamsB5,
		&dagconfig.TestnetParamsB10, &dagconfig.SimnetParams, &dagconfig.DevnetParams} {
		if params.OffsetModeValueChecksActivationDAAScore != 0 {
			t.Errorf("%s: offset-mode value checks should be active from genesis", params.Name)
		}
		if params.UnpricedTransactionFeeAllowance == 0 {
			t.Errorf("%s: unpriced transaction fee allowance is not set", params.Name)
		}
	}
}

// TestMissingInputAcceptancePreActivationUnchanged pins that before activation the missing-input
// path behaves exactly as it always did - fee 0, no validation - so history accepted under the old
// rule replays identically, and that after it the validator's verdict decides.
func TestMissingInputAcceptancePreActivationUnchanged(t *testing.T) {
	tx := &externalapi.DomainTransaction{
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 1_000_000_000_000}},
	}

	t.Run("pre-activation: taken on trust with fee 0", func(t *testing.T) {
		validator := &fakeMissingInputValidator{result: errors.Wrap(ruleerrors.ErrSpendTooHigh, "over-spend")}
		tx.StoreFee(123)
		ruleErr, err := checkMissingInputAcceptance(validator, model.NewStagingArea(), tx, nil, 10, false)
		if ruleErr != nil || err != nil {
			t.Fatalf("pre-activation must accept as before, got ruleErr=%v err=%v", ruleErr, err)
		}
		if validator.calls != 0 {
			t.Fatalf("pre-activation must not validate, validator called %d times", validator.calls)
		}
		if tx.LoadFee() != 0 {
			t.Fatalf("pre-activation fee must stay 0, got %d", tx.LoadFee())
		}
	})

	t.Run("post-activation: an over-spend is a consensus rejection", func(t *testing.T) {
		validator := &fakeMissingInputValidator{result: errors.Wrap(ruleerrors.ErrSpendTooHigh, "over-spend")}
		ruleErr, err := checkMissingInputAcceptance(validator, model.NewStagingArea(), tx, nil, 10, true)
		if err != nil || !errors.Is(ruleErr, ruleerrors.ErrSpendTooHigh) {
			t.Fatalf("expected an ErrSpendTooHigh rejection, got ruleErr=%v err=%v", ruleErr, err)
		}
	})

	t.Run("post-activation: a bad signature is a consensus rejection", func(t *testing.T) {
		validator := &fakeMissingInputValidator{result: errors.Wrap(ruleerrors.ErrScriptValidation, "bad sig")}
		ruleErr, err := checkMissingInputAcceptance(validator, model.NewStagingArea(), tx, nil, 10, true)
		if err != nil || !errors.Is(ruleErr, ruleerrors.ErrScriptValidation) {
			t.Fatalf("expected an ErrScriptValidation rejection, got ruleErr=%v err=%v", ruleErr, err)
		}
	})

	t.Run("post-activation: a valid transaction keeps the real fee", func(t *testing.T) {
		validator := &fakeMissingInputValidator{fee: 880_000}
		ruleErr, err := checkMissingInputAcceptance(validator, model.NewStagingArea(), tx, nil, 10, true)
		if ruleErr != nil || err != nil {
			t.Fatalf("expected acceptance, got ruleErr=%v err=%v", ruleErr, err)
		}
		if validator.calls != 1 || tx.LoadFee() != 880_000 {
			t.Fatalf("expected one validation storing fee 880000, got %d calls and fee %d", validator.calls, tx.LoadFee())
		}
	})

	t.Run("post-activation: a non-rule failure propagates", func(t *testing.T) {
		validator := &fakeMissingInputValidator{result: errors.New("database closed")}
		ruleErr, err := checkMissingInputAcceptance(validator, model.NewStagingArea(), tx, nil, 10, true)
		if ruleErr != nil || err == nil {
			t.Fatalf("expected a propagated error, got ruleErr=%v err=%v", ruleErr, err)
		}
	})
}

func testCoinbase(values ...uint64) *externalapi.DomainTransaction {
	outputs := make([]*externalapi.DomainTransactionOutput, len(values))
	for i, value := range values {
		outputs[i] = &externalapi.DomainTransactionOutput{
			Value:           value,
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51, byte(i)}, Version: 0},
		}
	}
	return &externalapi.DomainTransaction{
		Version:      0,
		Outputs:      outputs,
		SubnetworkID: subnetworks.SubnetworkIDCoinbase,
		Payload:      []byte{0x01},
	}
}

// TestCoinbaseWithinUnpricedAllowance pins what an offset node still tolerates in a coinbase once the
// value checks are active - a coinbase short of the expected one, or over it only by the fees this
// node could not price - and that a coinbase over-pay beyond that is rejected.
func TestCoinbaseWithinUnpricedAllowance(t *testing.T) {
	expected := testCoinbase(1_266_666_667, 66_666_666)
	tests := []struct {
		name      string
		actual    *externalapi.DomainTransaction
		allowance uint64
		wantOK    bool
	}{
		{"exact match", testCoinbase(1_266_666_667, 66_666_666), 0, true},
		{"under-pay destroys nothing", testCoinbase(1_266_000_000, 66_666_666), 0, true},
		{"over-pay by one sompi with nothing unpriced", testCoinbase(1_266_666_668, 66_666_666), 0, false},
		{"subsidy over-pay of 195M HTN", testCoinbase(19_481_638_908_668_146, 66_666_666), 10_000_000, false},
		{"over-pay within the unpriced allowance", testCoinbase(1_266_666_667+880_000, 66_666_666), 10_000_000, true},
		{"over-pay at exactly the allowance", testCoinbase(1_266_666_667+10_000_000, 66_666_666), 10_000_000, true},
		{"excess spread over outputs exceeding the allowance", testCoinbase(1_266_666_667+6_000_000, 66_666_666+6_000_000), 10_000_000, false},
		{"extra output", testCoinbase(1_266_666_667, 66_666_666, 1), 10_000_000, false},
	}
	for _, test := range tests {
		err := coinbaseWithinUnpricedAllowance(test.actual, expected, test.allowance)
		if test.wantOK && err != nil {
			t.Errorf("%s: expected tolerance, got %v", test.name, err)
		}
		if !test.wantOK && !errors.Is(err, ruleerrors.ErrBadCoinbaseTransaction) {
			t.Errorf("%s: expected ErrBadCoinbaseTransaction, got %v", test.name, err)
		}
	}

	differentPayee := testCoinbase(1_266_666_667, 66_666_666)
	differentPayee.Outputs[0].ScriptPublicKey = &externalapi.ScriptPublicKey{Script: []byte{0x52}, Version: 0}
	if err := coinbaseWithinUnpricedAllowance(differentPayee, expected, ^uint64(0)); !errors.Is(err, ruleerrors.ErrBadCoinbaseTransaction) {
		t.Errorf("a coinbase paying a different script must be rejected whatever the allowance, got %v", err)
	}

	// A payload difference is not compared, as validateCoinbaseTransaction does not compare it.
	otherPayload := testCoinbase(1_266_666_667, 66_666_666)
	otherPayload.Payload = []byte{0x02}
	if err := coinbaseWithinUnpricedAllowance(otherPayload, expected, 0); err != nil {
		t.Errorf("payload must not be compared, got %v", err)
	}
}

func TestUnpricedTransactionCount(t *testing.T) {
	entry := utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{}, false, 1)
	regular := func(inputs int) *externalapi.DomainTransaction {
		tx := &externalapi.DomainTransaction{SubnetworkID: subnetworks.SubnetworkIDNative}
		for i := 0; i < inputs; i++ {
			tx.Inputs = append(tx.Inputs, &externalapi.DomainTransactionInput{})
		}
		return tx
	}
	acceptanceData := externalapi.AcceptanceData{
		{TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
			{Transaction: testCoinbase(1), IsAccepted: true},                                                                // coinbase: never counted
			{Transaction: regular(2), IsAccepted: true, TransactionInputUTXOEntries: []externalapi.UTXOEntry{entry, entry}}, // priced
			{Transaction: regular(2), IsAccepted: true, TransactionInputUTXOEntries: []externalapi.UTXOEntry{entry, nil}},   // missing input
			{Transaction: regular(1), IsAccepted: false},                                                                    // not accepted here
		}},
		{TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
			{Transaction: regular(3), IsAccepted: true, TransactionInputUTXOEntries: []externalapi.UTXOEntry{entry}}, // entries short
		}},
	}
	if got := unpricedTransactionCount(acceptanceData); got != 3 {
		t.Fatalf("unpriced transactions: got %d, want 3", got)
	}
	if got := saturatingMul(3, 10_000_000); got != 30_000_000 {
		t.Fatalf("saturatingMul(3, 1e7) = %d", got)
	}
	if got := saturatingMul(^uint64(0), 2); got != ^uint64(0) {
		t.Fatalf("saturatingMul must saturate, got %d", got)
	}
}

// TestNotTolerableIsStillARuleError pins that marking a coinbase failure notTolerable keeps it a
// RuleError, so the block is disqualified rather than the error being treated as a node fault.
func TestNotTolerableIsStillARuleError(t *testing.T) {
	err := error(notTolerable{errors.Wrap(ruleerrors.ErrBadCoinbaseTransaction, "over-pay")})
	wrapped := errors.Wrap(err, "coinbase-transaction check failed")
	if !isNotTolerable(wrapped) {
		t.Fatalf("expected the marker to survive wrapping")
	}
	if !errors.As(wrapped, &ruleerrors.RuleError{}) || !errors.Is(wrapped, ruleerrors.ErrBadCoinbaseTransaction) {
		t.Fatalf("expected a notTolerable coinbase failure to remain an ErrBadCoinbaseTransaction RuleError")
	}
	if isNotTolerable(errors.Wrap(ruleerrors.ErrBadCoinbaseTransaction, "plain")) {
		t.Fatalf("a plain RuleError must not read as notTolerable")
	}
}
