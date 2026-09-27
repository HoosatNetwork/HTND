package consensusstatemanager

import (
	"fmt"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// maxReportedMergeSetBlocks caps the merge set listing in a disqualification report. A merge set can
// hold hundreds of blocks; the first few are what tell whose view the block was built on.
const maxReportedMergeSetBlocks = 32

// disqualificationReport describes a block that verifyUTXO just disqualified, for onDisqualification.
// It is only built when that callback is set, since it re-runs every check.
//
// What it is for: telling apart "this block is wrong" from "this node's UTXO history differs from the
// block's miner's". So it names the miner of the block, of its selected parent, of every merge set
// block and of the pruning point (the coinbase payload carries the node's version tag), reports every
// check rather than only the first that failed, and states each input of the toleration decision.
func (csm *consensusStateManager) disqualificationReport(stagingArea *model.StagingArea,
	block *externalapi.DomainBlock, blockHash, selectedParentHash *externalapi.DomainHash,
	pastUTXOSet externalapi.UTXODiff, acceptanceData externalapi.AcceptanceData, multiset model.Multiset,
) string {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, "\n  "+format, args...)
	}
	header := block.Header

	line("block %s: version %d, DAA score %d, blue score %d, time %s, %d parents, %d transactions",
		blockHash, header.Version(), header.DAAScore(), header.BlueScore(),
		time.UnixMilli(header.TimeInMilliseconds()).UTC().Format(time.RFC3339Nano),
		len(header.DirectParents()), len(block.Transactions))
	line("miner: %s", coinbaseTag(block))

	// Every check, independently. verifyUTXO stops at the first failure.
	computedCommitment := multiset.Hash()
	line("check utxo-commitment: header %s, computed %s -> %s", header.UTXOCommitment(), computedCommitment,
		passFail(computedCommitment.Equal(header.UTXOCommitment())))
	computedAcceptedIDMerkleRoot := calculateAcceptedIDMerkleRoot(acceptanceData, header.Version())
	line("check accepted-id-merkle-root: header %s, computed %s -> %s", header.AcceptedIDMerkleRoot(),
		computedAcceptedIDMerkleRoot, passFail(computedAcceptedIDMerkleRoot.Equal(header.AcceptedIDMerkleRoot())))
	if len(block.Transactions) > 0 {
		csm.reportCoinbase(stagingArea, line, block, blockHash, acceptanceData)
	}
	line("check block-transactions-vs-past-utxo: %s", errorOrPass(csm.validateBlockTransactionsAgainstPastUTXO(
		stagingArea, block, pastUTXOSet, acceptanceData, nil)))

	// The toleration decision's inputs.
	strict, strictErr := csm.utxoCommitmentIsStrictFor(stagingArea, blockHash)
	carriesOffsetOnly, arithmeticProblem := blockOnlyCarriesTheInheritedOffset(acceptanceData, pastUTXOSet,
		header.DAAScore(), func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool) {
			return csm.virtualUTXOEntry(stagingArea, outpoint)
		})
	if arithmeticProblem == "" {
		arithmeticProblem = "none"
	}
	line("toleration: offset baseline signal %t, strict commitment gate %t (err %v), acceptance data agrees with "+
		"UTXO diff %t (problem: %s)", csm.blockInheritsKnownUTXOCommitmentOffset(stagingArea, blockHash), strict,
		strictErr, carriesOffsetOnly, arithmeticProblem)

	line("selected parent %s", csm.describeChainBlock(stagingArea, selectedParentHash))

	health := csm.UTXOSetHealth(stagingArea)
	if health.Checked {
		line("pruning point %s: stored multiset %s, header commitment %s, baseline verified %t", health.PruningPoint,
			health.StoredMultiset, health.HeaderCommitment, health.BaselineVerified)
		if ppBlock, err := csm.blockStore.Block(csm.databaseContext, stagingArea, health.PruningPoint); err == nil {
			line("pruning point miner: %s", coinbaseTag(ppBlock))
		}
	} else {
		line("pruning point: baseline not checked (genesis, or unreadable)")
	}

	ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if err != nil {
		line("merge set: GHOSTDAG data unreadable: %s", err)
		return b.String()
	}
	blues := make(map[externalapi.DomainHash]bool, len(ghostdagData.MergeSetBlues()))
	for _, blue := range ghostdagData.MergeSetBlues() {
		blues[*blue] = true
	}
	line("merge set: %d blues, %d reds", len(ghostdagData.MergeSetBlues()), len(ghostdagData.MergeSetReds()))
	for i, blockAcceptance := range acceptanceData {
		if i == maxReportedMergeSetBlocks {
			line("  ... %d more merge set blocks not listed", len(acceptanceData)-i)
			break
		}
		accepted := 0
		for _, transactionAcceptance := range blockAcceptance.TransactionAcceptanceData {
			if transactionAcceptance.IsAccepted {
				accepted++
			}
		}
		color := "red"
		if blues[*blockAcceptance.BlockHash] {
			color = "blue"
		}
		tag := "body unreadable"
		var daaScore uint64
		if merged, err := csm.blockStore.Block(csm.databaseContext, stagingArea, blockAcceptance.BlockHash); err == nil {
			tag = coinbaseTag(merged)
			daaScore = merged.Header.DAAScore()
		}
		line("  [%d] %s %s DAA %d, %d/%d transactions accepted, miner: %s", i, blockAcceptance.BlockHash, color,
			daaScore, accepted, len(blockAcceptance.TransactionAcceptanceData), tag)
	}
	return b.String()
}

// maxReportedCoinbaseOutputs caps the per-output coinbase comparison in a disqualification report.
const maxReportedCoinbaseOutputs = 16

// reportCoinbase compares the block's coinbase against the one this node expects, output by output,
// since the check itself only says that they differ. Built the same way validateCoinbaseTransaction
// builds it, including taking the payload from the block.
func (csm *consensusStateManager) reportCoinbase(stagingArea *model.StagingArea, line func(string, ...any),
	block *externalapi.DomainBlock, blockHash *externalapi.DomainHash, acceptanceData externalapi.AcceptanceData,
) {
	coinbase := block.Transactions[0]
	_, coinbaseData, _, err := csm.coinbaseManager.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(coinbase,
		block.Header.Version())
	if err != nil {
		line("check coinbase-transaction: FAIL: coinbase data unreadable: %s", err)
		return
	}
	expected, _, err := csm.coinbaseManager.ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea, blockHash,
		coinbaseData, acceptanceData)
	if err != nil {
		line("check coinbase-transaction: expected coinbase could not be built: %s", err)
		return
	}
	expected.Payload = coinbase.Payload
	matches := consensushashing.TransactionHash(coinbase).Equal(consensushashing.TransactionHash(expected))
	var actualTotal, expectedTotal uint64
	for _, output := range coinbase.Outputs {
		actualTotal += output.Value
	}
	for _, output := range expected.Outputs {
		expectedTotal += output.Value
	}
	line("check coinbase-transaction: %s - %d outputs paying %d, expected %d outputs paying %d",
		passFail(matches), len(coinbase.Outputs), actualTotal, len(expected.Outputs), expectedTotal)
	if matches {
		return
	}
	outputs := max(len(coinbase.Outputs), len(expected.Outputs))
	for i := 0; i < outputs && i < maxReportedCoinbaseOutputs; i++ {
		actual, wanted := "none", "none"
		if i < len(coinbase.Outputs) {
			actual = fmt.Sprintf("%d to %x", coinbase.Outputs[i].Value, coinbase.Outputs[i].ScriptPublicKey.Script)
		}
		if i < len(expected.Outputs) {
			wanted = fmt.Sprintf("%d to %x", expected.Outputs[i].Value, expected.Outputs[i].ScriptPublicKey.Script)
		}
		marker := ""
		if actual != wanted {
			marker = " <- differs"
		}
		line("  coinbase output %d: actual %s, expected %s%s", i, actual, wanted, marker)
	}
	if outputs > maxReportedCoinbaseOutputs {
		line("  ... %d more coinbase outputs not listed", outputs-maxReportedCoinbaseOutputs)
	}
}

// describeChainBlock summarises a chain block for a disqualification report: its status, whether its
// stored multiset reproduces its own header commitment, and its miner.
func (csm *consensusStateManager) describeChainBlock(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) string {
	parts := []string{blockHash.String()}
	if status, err := csm.blockStatusStore.Get(csm.databaseContext, stagingArea, blockHash); err == nil {
		parts = append(parts, "status "+status.String())
	}
	header, headerErr := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, blockHash)
	if headerErr == nil {
		parts = append(parts, fmt.Sprintf("DAA %d", header.DAAScore()))
		if stored, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, blockHash); err == nil {
			parts = append(parts, fmt.Sprintf("stored multiset matches its header %t",
				stored.Hash().Equal(header.UTXOCommitment())))
		} else {
			parts = append(parts, "no stored multiset")
		}
	}
	if block, err := csm.blockStore.Block(csm.databaseContext, stagingArea, blockHash); err == nil {
		parts = append(parts, "miner: "+coinbaseTag(block))
	}
	return strings.Join(parts, ", ")
}

// coinbaseTag identifies who templated a block: the trailing printable text of its coinbase payload,
// where the node's version tag and the stratum bridge name are written, and the script of its first
// output (the payout address).
func coinbaseTag(block *externalapi.DomainBlock) string {
	if len(block.Transactions) == 0 {
		return "no coinbase"
	}
	coinbase := block.Transactions[0]
	payload := coinbase.Payload
	start := len(payload)
	for start > 0 && payload[start-1] >= 0x20 && payload[start-1] <= 0x7e {
		start--
	}
	text := string(payload[start:])
	const maxTagLength = 120
	if len(text) > maxTagLength {
		text = text[len(text)-maxTagLength:]
	}
	script := "none"
	if len(coinbase.Outputs) > 0 && coinbase.Outputs[0].ScriptPublicKey != nil {
		script = fmt.Sprintf("%x", coinbase.Outputs[0].ScriptPublicKey.Script)
	}
	return fmt.Sprintf("%q, payout script %s", text, script)
}

func passFail(ok bool) string {
	if ok {
		return "pass"
	}
	return "FAIL"
}

func errorOrPass(err error) string {
	if err == nil {
		return "pass"
	}
	return "FAIL: " + err.Error()
}
