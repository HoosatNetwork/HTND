package consensusstatemanager

import (
	"bytes"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// Offset-mode value checks (HardForkGates.OffsetModeValueChecksVersion, block version 11).
//
// A node whose UTXO baseline is offset tolerates what it cannot reproduce. Before activation that
// toleration also covered two things that move value and that the node CAN check:
//
//  1. A transaction accepted despite missing inputs (maybeAcceptTransaction) skipped
//     ValidateTransactionInContextAndPopulateFee entirely - no input/output sum check, no signature
//     check even on the inputs that were found, no maturity, no sequence locks, no sigops - and every
//     output was created at full value. One real input plus one made-up outpoint could mint any
//     amount and spend anyone's coin.
//  2. A coinbase that did not match the expected one (ErrBadCoinbaseTransaction) was tolerated
//     outright, so a coinbase could pay any amount.
//
// From activation, (1) runs every check the found inputs can decide and requires outputs <= found
// inputs; (2) is tolerated only when the coinbase has the expected shape and exceeds the expected
// amounts by no more than the fees this node could not price. Why not exact coinbase equality: the
// expected coinbase on an offset node is computed from ITS acceptance data, in which a transaction
// with missing inputs carries only its verifiable fee, and a transaction it rejected carries none.
// A miner that holds those coins legitimately claims more. Requiring equality would disqualify
// honest blocks and split every offset node off the network; bounding the excess keeps them on it
// while making subsidy over-pay impossible and fee over-pay small and bounded.
//
// The gate is the block version, activated through the network's POWScores like every earlier
// hard fork: blocks of version >= HardForkGates.OffsetModeValueChecksVersion (11) get the checks, blocks
// of version 10 and below keep the old behaviour exactly, so history accepted before activation
// replays exactly as it was accepted during IBD, reorgs and virtual resolution.
//
// The version is derived from the DAA score of the block being validated or merging with
// constants.BlockVersionForDAAScore, as dagconfig.HardForkActive requires, rather than read from a
// peer-supplied header field. For a real block the two are identical (checkBlockVersion rejects a
// header whose version differs from the one its DAA score implies); for virtual, which has no
// header, the derived version is the version a block built on the same parents carries, so a
// block's own verdict and the virtual view that produced it always agree.

// offsetModeValueChecksActive reports whether the offset-mode value checks apply to a block (or
// virtual) with the given DAA score on this network.
func (csm *consensusStateManager) offsetModeValueChecksActive(blockDAAScore uint64) bool {
	return offsetModeValueChecksActiveForVersion(*csm.hardForkGates, constants.BlockVersionForDAAScore(csm.powScores, blockDAAScore))
}

// offsetModeValueChecksActiveForVersion reports whether the offset-mode value checks apply to a block
// of the given version under gates.
func offsetModeValueChecksActiveForVersion(gates dagconfig.HardForkGates, blockVersion uint16) bool {
	return dagconfig.HardForkActive(gates.OffsetModeValueChecksVersion, blockVersion)
}

// checkMissingInputAcceptance decides the fee and validity of a transaction on the missing-input
// path. Below version 11 it keeps the old behaviour: fee 0, no checks. From it, the found inputs
// are validated and outputs may not exceed them. ruleErr is a consensus rejection of the
// transaction; err is anything else, which the caller must propagate.
func checkMissingInputAcceptance(validator model.TransactionValidator, stagingArea *model.StagingArea,
	transaction *externalapi.DomainTransaction, blockHash *externalapi.DomainHash, blockDAAScore uint64,
	active bool,
) (ruleErr error, err error) {
	if !active {
		transaction.StoreFee(0)
		return nil, nil
	}
	validationErr := validator.ValidateTransactionWithMissingInputsAndPopulateFee(
		stagingArea, transaction, blockHash, blockDAAScore)
	if validationErr == nil {
		return nil, nil
	}
	if errors.As(validationErr, &(ruleerrors.RuleError{})) {
		return validationErr, nil
	}
	return nil, validationErr
}

// notTolerable marks a verifyUTXO step failure that the offset toleration must not wave through.
// It unwraps to the underlying error, so it is still a RuleError to every caller.
type notTolerable struct {
	error
}

func (e notTolerable) Unwrap() error {
	return e.error
}

func isNotTolerable(err error) bool {
	var marker notTolerable
	return errors.As(err, &marker)
}

// unpricedTransactionCount counts the merge-set transactions whose fee this node could not compute:
// non-coinbase transactions it did not accept, and accepted ones missing an input entry (accepted
// despite missing inputs). A miner with a more complete UTXO set may legitimately have collected
// fees on them that this node's expected coinbase does not contain.
func unpricedTransactionCount(acceptanceData externalapi.AcceptanceData) uint64 {
	count := uint64(0)
	for _, blockAcceptanceData := range acceptanceData {
		for _, transactionAcceptance := range blockAcceptanceData.TransactionAcceptanceData {
			transaction := transactionAcceptance.Transaction
			if transaction == nil || transactionhelper.IsCoinBase(transaction) {
				continue
			}
			if !transactionAcceptance.IsAccepted {
				count++
				continue
			}
			if len(transactionAcceptance.TransactionInputUTXOEntries) != len(transaction.Inputs) {
				count++
				continue
			}
			for _, entry := range transactionAcceptance.TransactionInputUTXOEntries {
				if entry == nil {
					count++
					break
				}
			}
		}
	}
	return count
}

// coinbaseWithinUnpricedAllowance reports whether actual differs from expected only in ways an offset
// node cannot rule on: every field and every output script must match, each output may be smaller
// than expected (value not claimed destroys nothing), and the outputs may exceed the expected ones
// by at most allowance in total. The payload is not compared, as validateCoinbaseTransaction does not
// compare it either.
func coinbaseWithinUnpricedAllowance(actual, expected *externalapi.DomainTransaction, allowance uint64,
	requireExact bool,
) error {
	if actual.Version != expected.Version || actual.LockTime != expected.LockTime ||
		!actual.SubnetworkID.Equal(&expected.SubnetworkID) || actual.Gas != expected.Gas ||
		len(actual.Inputs) != len(expected.Inputs) {
		return errors.Wrap(ruleerrors.ErrBadCoinbaseTransaction,
			"coinbase transaction fields differ from the expected coinbase")
	}
	if len(actual.Outputs) != len(expected.Outputs) {
		return errors.Wrapf(ruleerrors.ErrBadCoinbaseTransaction,
			"coinbase has %d outputs, expected %d", len(actual.Outputs), len(expected.Outputs))
	}
	excess := uint64(0)
	for i, output := range actual.Outputs {
		expectedOutput := expected.Outputs[i]
		if output.ScriptPublicKey.Version != expectedOutput.ScriptPublicKey.Version ||
			!bytes.Equal(output.ScriptPublicKey.Script, expectedOutput.ScriptPublicKey.Script) {
			return errors.Wrapf(ruleerrors.ErrBadCoinbaseTransaction,
				"coinbase output %d pays a different script than expected", i)
		}
		if requireExact && output.Value != expectedOutput.Value {
			return errors.Wrapf(ruleerrors.ErrBadCoinbaseTransaction,
				"coinbase output %d pays %d, expected exactly %d", i, output.Value, expectedOutput.Value)
		}
		if output.Value <= expectedOutput.Value {
			continue
		}
		outputExcess := output.Value - expectedOutput.Value
		if outputExcess > allowance || excess > allowance-outputExcess {
			return errors.Wrapf(ruleerrors.ErrBadCoinbaseTransaction,
				"coinbase output %d pays %d, %d more than expected (%d); only %d sompi of fees this node "+
					"could not price may be claimed beyond the expected coinbase",
				i, output.Value, outputExcess, expectedOutput.Value, allowance)
		}
		excess += outputExcess
	}
	return nil
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > ^uint64(0)/b {
		return ^uint64(0)
	}
	return a * b
}

// checkCoinbaseOnOffsetBaseline is what replaces the wholesale toleration of ErrBadCoinbaseTransaction
// once the offset-mode value checks are active. nil means the mismatch is explained by fees this node
// could not price and may be tolerated; otherwise the coinbase creates value and the error is marked
// notTolerable.
func (csm *consensusStateManager) checkCoinbaseOnOffsetBaseline(stagingArea *model.StagingArea,
	block *externalapi.DomainBlock, blockHash *externalapi.DomainHash,
	coinbaseTransaction *externalapi.DomainTransaction, acceptanceData externalapi.AcceptanceData,
	requireExact bool,
) error {
	expected, err := csm.expectedCoinbaseTransaction(stagingArea, block, blockHash, coinbaseTransaction, acceptanceData)
	if err != nil {
		return err
	}
	allowance := saturatingMul(unpricedTransactionCount(acceptanceData), csm.unpricedTransactionFeeAllowance)
	if err := coinbaseWithinUnpricedAllowance(coinbaseTransaction, expected, allowance, requireExact); err != nil {
		return notTolerable{errors.Wrapf(err, "block %s", blockHash)}
	}
	return nil
}
