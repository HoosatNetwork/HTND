package transactionvalidator_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/kaspanet/go-secp256k1"
	"github.com/pkg/errors"
)

// TestValidateTransactionWithMissingInputs pins the checks a transaction accepted despite missing
// inputs must pass once the offset-mode value checks are active: it may not create more than the
// inputs this node holds, and those inputs must be properly signed and mature. The missing input
// counts for nothing.
func TestValidateTransactionWithMissingInputs(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, tearDown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestValidateTransactionWithMissingInputs")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer tearDown(false)

		privateKey, err := secp256k1.GenerateSchnorrKeyPair()
		if err != nil {
			t.Fatalf("GenerateSchnorrKeyPair: %+v", err)
		}
		publicKey, err := privateKey.SchnorrPublicKey()
		if err != nil {
			t.Fatalf("SchnorrPublicKey: %+v", err)
		}
		publicKeySerialized, err := publicKey.Serialize()
		if err != nil {
			t.Fatalf("Serialize: %+v", err)
		}
		address, err := util.NewAddressPublicKey(publicKeySerialized[:], consensusConfig.Prefix)
		if err != nil {
			t.Fatalf("NewAddressPublicKey: %+v", err)
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatalf("PayToAddrScript: %+v", err)
		}

		const foundAmount = 100_000_000 // 1 HTN
		const povDAAScore = 10_000
		foundOutpoint := externalapi.DomainOutpoint{TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0}
		missingOutpoint := externalapi.DomainOutpoint{TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x02}), Index: 7}

		// build returns a transaction spending one found input worth foundAmount (with the given
		// coinbase flag and DAA score) and one missing input, paying outputValue. sign controls whether
		// the found input carries a valid signature.
		var build func(outputValue uint64, isCoinbase bool, entryDAAScore uint64, sign bool) *externalapi.DomainTransaction
		build = func(outputValue uint64, isCoinbase bool, entryDAAScore uint64, sign bool) *externalapi.DomainTransaction {
			tx := &externalapi.DomainTransaction{
				Version: constants.MaxTransactionVersion,
				Inputs: []*externalapi.DomainTransactionInput{
					{
						PreviousOutpoint: foundOutpoint,
						Sequence:         constants.MaxTxInSequenceNum,
						SigOpCount:       1,
						UTXOEntry:        utxo.NewUTXOEntry(foundAmount, scriptPublicKey, isCoinbase, entryDAAScore),
					},
					{
						PreviousOutpoint: missingOutpoint,
						Sequence:         constants.MaxTxInSequenceNum,
						SigOpCount:       1,
					},
				},
				Outputs:      []*externalapi.DomainTransactionOutput{{Value: outputValue, ScriptPublicKey: scriptPublicKey}},
				SubnetworkID: subnetworks.SubnetworkIDNative,
			}
			if sign {
				signatureScript, err := txscript.SignatureScript(tx, 0, consensushashing.SigHashAll, privateKey,
					&consensushashing.SighashReusedValues{})
				if err != nil {
					t.Fatalf("SignatureScript: %+v", err)
				}
				tx.Inputs[0].SignatureScript = signatureScript
			} else {
				// A well-formed signature by the right key over a different transaction.
				other := build(1, isCoinbase, entryDAAScore, true)
				tx.Inputs[0].SignatureScript = other.Inputs[0].SignatureScript
			}
			return tx
		}

		validator := tc.TransactionValidator()
		validate := func(tx *externalapi.DomainTransaction) error {
			return validator.ValidateTransactionWithMissingInputsAndPopulateFee(model.NewStagingArea(), tx,
				consensusConfig.GenesisHash, povDAAScore)
		}

		t.Run("outputs within the found inputs are accepted and the verifiable fee stored", func(t *testing.T) {
			tx := build(foundAmount-2_500, false, 1, true)
			if err := validate(tx); err != nil {
				t.Fatalf("expected a valid transaction, got %+v", err)
			}
			if fee := tx.LoadFee(); fee != 2_500 {
				t.Fatalf("stored fee %d, want 2500 (found inputs minus outputs)", fee)
			}
		})

		t.Run("outputs exactly equal to the found inputs are accepted", func(t *testing.T) {
			if err := validate(build(foundAmount, false, 1, true)); err != nil {
				t.Fatalf("expected a valid transaction, got %+v", err)
			}
		})

		t.Run("over-spending the found inputs is rejected", func(t *testing.T) {
			// The missing input could be worth anything; it is worth nothing here.
			err := validate(build(foundAmount+1, false, 1, true))
			if !errors.Is(err, ruleerrors.ErrSpendTooHigh) {
				t.Fatalf("expected ErrSpendTooHigh, got %+v", err)
			}
		})

		t.Run("a bad signature on a found input is rejected", func(t *testing.T) {
			err := validate(build(foundAmount/2, false, 1, false))
			if !errors.Is(err, ruleerrors.ErrScriptValidation) {
				t.Fatalf("expected ErrScriptValidation, got %+v", err)
			}
		})

		t.Run("an immature coinbase found input is rejected", func(t *testing.T) {
			err := validate(build(foundAmount/2, true, povDAAScore, true))
			if !errors.Is(err, ruleerrors.ErrImmatureSpend) {
				t.Fatalf("expected ErrImmatureSpend, got %+v", err)
			}
		})

		t.Run("a wrong sigop count on a found input is rejected", func(t *testing.T) {
			tx := build(foundAmount/2, false, 1, true)
			tx.Inputs[0].SigOpCount = 2
			err := validate(tx)
			if !errors.Is(err, ruleerrors.ErrWrongSigOpCount) {
				t.Fatalf("expected ErrWrongSigOpCount, got %+v", err)
			}
		})

		t.Run("a transaction with no found input is not valid by default", func(t *testing.T) {
			tx := build(1, false, 1, true)
			tx.Inputs[0].UTXOEntry = nil
			err := validate(tx)
			if !errors.As(err, &ruleerrors.ErrMissingTxOut{}) {
				t.Fatalf("expected ErrMissingTxOut, got %+v", err)
			}
		})
	})
}
