package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/pkg/errors"
)

// utxosurveyReset turns the survey off again after a test that switched it on.
func utxosurveyReset(t *testing.T) {
	utxosurvey.Reset()
	t.Cleanup(utxosurvey.Reset)
}

// TestErrMissingTxOutSeparatesDoubleSpendFromAbsentCoin pins the distinction the whole toleration
// path depends on.
//
// ErrMissingTxOut covers two unrelated findings. An input already spent in the view being validated
// against is a double spend - a real rule violation, and rejecting it is the point of the check. An
// input merely absent from this node's UTXO set says nothing about the transaction; it says this
// node is missing a coin the network has. A node whose set is known to be incomplete tolerates the
// second so it can sync at all, and tolerating the first with it would mean silently accepting
// blocks that spend the same coin twice.
func TestErrMissingTxOutSeparatesDoubleSpendFromAbsentCoin(t *testing.T) {
	absent := outpoint(1, 0)
	spent := outpoint(2, 1)

	t.Run("absent coin only", func(t *testing.T) {
		err := ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{absent})
		var missingTxOut ruleerrors.ErrMissingTxOut
		if !errors.As(err, &missingTxOut) {
			t.Fatal("expected an ErrMissingTxOut")
		}
		if missingTxOut.HasDoubleSpend() {
			t.Error("a coin this node simply does not hold is not a double spend")
		}
		if len(missingTxOut.MissingOutpoints) != 1 {
			t.Errorf("the full list must still carry every unresolved input: %+v", missingTxOut.MissingOutpoints)
		}
	})

	t.Run("already-spent coin", func(t *testing.T) {
		err := ruleerrors.NewErrMissingOrSpentTxOut(
			[]*externalapi.DomainOutpoint{absent, spent}, []*externalapi.DomainOutpoint{spent})
		var missingTxOut ruleerrors.ErrMissingTxOut
		if !errors.As(err, &missingTxOut) {
			t.Fatal("expected an ErrMissingTxOut")
		}
		if !missingTxOut.HasDoubleSpend() {
			t.Fatal("an input already spent in this view is a double spend")
		}
		if len(missingTxOut.SpentOutpoints) != 1 || !missingTxOut.SpentOutpoints[0].Equal(spent) {
			t.Errorf("the double spend must be named exactly: %+v", missingTxOut.SpentOutpoints)
		}
		// Existing consumers read MissingOutpoints and must keep seeing every unresolved input, or
		// separating the two findings would quietly change what they act on.
		if len(missingTxOut.MissingOutpoints) != 2 {
			t.Errorf("the full list must be unchanged for existing consumers: %+v",
				missingTxOut.MissingOutpoints)
		}
	})
}

// TestRejectionReasonNamesADoubleSpend checks the acceptance path labels the two apart. Before this,
// a transaction rejected as an ordinary duplicate - the same transaction in several blocks, which a
// DAG produces constantly - was recorded identically to one this node could not accept because it
// lacked a coin, and a survey read 82% of absent coins as self-inflicted damage on that basis.
func TestRejectionReasonNamesADoubleSpend(t *testing.T) {
	t.Setenv("HTND_UTXO_SURVEY", "/dev/null")
	utxosurveyReset(t)

	spent := outpoint(3, 0)
	doubleSpend := newTransactionRejection("missing-input", ruleerrors.NewErrMissingOrSpentTxOut(
		[]*externalapi.DomainOutpoint{spent}, []*externalapi.DomainOutpoint{spent}))
	if doubleSpend == nil || doubleSpend.reason != "double-spend" {
		t.Errorf("an already-spent input must be labelled a double spend, got %+v", doubleSpend)
	}

	absent := outpoint(4, 0)
	shortage := newTransactionRejection("missing-input",
		ruleerrors.NewErrMissingTxOut([]*externalapi.DomainOutpoint{absent}))
	if shortage == nil || shortage.reason != "missing-input" {
		t.Errorf("a coin this node lacks must stay labelled missing-input, got %+v", shortage)
	}
}
