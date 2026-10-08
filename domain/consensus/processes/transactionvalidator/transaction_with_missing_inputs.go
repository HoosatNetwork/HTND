package transactionvalidator

import (
	"math"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/pkg/errors"
)

// ValidateTransactionWithMissingInputsAndPopulateFee is ValidateTransactionInContextAndPopulateFee for
// a transaction some of whose inputs this node's UTXO set does not hold - the transactions
// consensusStateManager accepts despite missing inputs on a chain with an offset baseline.
//
// Every check that can be decided from the inputs that ARE present is run on them, exactly as
// ValidateTransactionInContextAndPopulateFee would: amounts, coinbase maturity, sequence locks,
// sigop counts, and the scripts (signatures). A signature commits to the spent entry's own script
// and amount only (consensushashing.CalculateSignatureHash reads txIn.UTXOEntry of the input being
// signed, never another input's), so a found input's signature is fully verifiable without the
// missing entries.
//
// The value rule is the conservative one: the outputs may not exceed the inputs this node can see.
// Missing inputs contribute nothing, because nothing about them - their amount, their owner, whether
// they exist at all - can be checked here. The fee stored is the verifiable one, found inputs minus
// outputs, which is never more than the true fee.
//
// Missing inputs are skipped, not reported: the caller has already decided the transaction is on the
// missing-input path, and ErrMissingTxOut here would only repeat that.
func (v *transactionValidator) ValidateTransactionWithMissingInputsAndPopulateFee(
	_ *model.StagingArea,
	tx *externalapi.DomainTransaction,
	_ *externalapi.DomainHash,
	povDAAScore uint64,
) error {
	foundInputs := 0
	totalSompiIn := uint64(0)
	for _, input := range tx.Inputs {
		if input.UTXOEntry == nil {
			continue
		}
		foundInputs++
		var err error
		totalSompiIn, err = v.checkEntryAmounts(input.UTXOEntry, totalSompiIn)
		if err != nil {
			return err
		}
	}
	if foundInputs == 0 {
		// Nothing verifiable at all. consensusStateManager never gets here (it requires a resolved
		// input), but a transaction with no found input must not be read as valid by default.
		missing := make([]*externalapi.DomainOutpoint, 0, len(tx.Inputs))
		for _, input := range tx.Inputs {
			missing = append(missing, &input.PreviousOutpoint)
		}
		return ruleerrors.NewErrMissingTxOut(missing)
	}

	totalSompiOut, err := v.checkTransactionOutputAmounts(tx, totalSompiIn)
	if err != nil {
		return err
	}
	tx.StoreFee(totalSompiIn - totalSompiOut)

	if err := v.checkFoundInputsCoinbaseMaturity(tx, povDAAScore); err != nil {
		return err
	}
	if err := v.checkFoundInputsSequenceLock(tx, povDAAScore); err != nil {
		return err
	}
	if err := v.validateFoundInputsSigOpCounts(tx); err != nil {
		return err
	}
	return v.validateFoundInputsScripts(tx)
}

func (v *transactionValidator) checkFoundInputsCoinbaseMaturity(tx *externalapi.DomainTransaction, povDAAScore uint64) error {
	for i, input := range tx.Inputs {
		utxoEntry := input.UTXOEntry
		if utxoEntry == nil || !utxoEntry.IsCoinbase() {
			continue
		}
		originDAAScore := utxoEntry.BlockDAAScore()
		if originDAAScore+v.blockCoinbaseMaturity > povDAAScore {
			return errors.Wrapf(ruleerrors.ErrImmatureSpend, "input %d tried to spend coinbase "+
				"transaction output %s from DAA score %d to DAA score %d before required maturity of %d",
				i, input.PreviousOutpoint, originDAAScore, povDAAScore, v.blockCoinbaseMaturity)
		}
	}
	return nil
}

func (v *transactionValidator) checkFoundInputsSequenceLock(tx *externalapi.DomainTransaction, povDAAScore uint64) error {
	lock := &sequenceLock{BlockDAAScore: -1}
	for _, input := range tx.Inputs {
		utxoEntry := input.UTXOEntry
		if utxoEntry == nil {
			continue
		}
		if input.Sequence&constants.SequenceLockTimeDisabled == constants.SequenceLockTimeDisabled {
			continue
		}
		inputDAAScore := utxoEntry.BlockDAAScore()
		if inputDAAScore == constants.UnacceptedDAAScore {
			continue
		}
		relativeLockUnsigned := input.Sequence & constants.SequenceLockTimeMask
		if relativeLockUnsigned > math.MaxInt64 || inputDAAScore > math.MaxInt64 {
			return errors.Errorf("sequence lock of input spending %s is out of range", input.PreviousOutpoint)
		}
		blockDAAScore := int64(inputDAAScore) + int64(relativeLockUnsigned) - 1
		if blockDAAScore > lock.BlockDAAScore {
			lock.BlockDAAScore = blockDAAScore
		}
	}
	if !v.sequenceLockActive(lock, povDAAScore) {
		return errors.Wrapf(ruleerrors.ErrUnfinalizedTx, "block contains transaction whose input "+
			"sequence locks are not met")
	}
	return nil
}

func (v *transactionValidator) validateFoundInputsSigOpCounts(tx *externalapi.DomainTransaction) error {
	for i, input := range tx.Inputs {
		if input.UTXOEntry == nil {
			continue
		}
		sigOpCount := txscript.GetPreciseSigOpCount(input.SignatureScript, input.UTXOEntry.ScriptPublicKey())
		if sigOpCount != int(input.SigOpCount) {
			return errors.Wrapf(ruleerrors.ErrWrongSigOpCount,
				"input %d specifies SigOpCount %d while actual SigOpCount is %d",
				i, input.SigOpCount, sigOpCount)
		}
	}
	return nil
}

func (v *transactionValidator) validateFoundInputsScripts(tx *externalapi.DomainTransaction) error {
	sighashReusedValues := &consensushashing.SighashReusedValues{}
	for i, input := range tx.Inputs {
		utxoEntry := input.UTXOEntry
		if utxoEntry == nil {
			continue
		}
		scriptPubKey := utxoEntry.ScriptPublicKey()
		vm := v.enginePool.Get().(*txscript.Engine)
		err := vm.Init(scriptPubKey, tx, i, txscript.ScriptNoFlags, v.sigCache, v.sigCacheECDSA, v.mldsa44Cache, sighashReusedValues)
		if err != nil {
			vm.Reset()
			v.enginePool.Put(vm)
			return errors.Wrapf(ruleerrors.ErrScriptMalformed, "failed to parse input %d which references "+
				"output %s - %s", i, input.PreviousOutpoint, err)
		}
		if err := vm.Execute(); err != nil {
			vm.Reset()
			v.enginePool.Put(vm)
			return errors.Wrapf(ruleerrors.ErrScriptValidation, "failed to validate input %d which "+
				"references output %s - %s", i, input.PreviousOutpoint, err)
		}
		vm.Reset()
		v.enginePool.Put(vm)
	}
	return nil
}
