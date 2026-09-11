package consensus_test

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

// TestRelayedBlockVerdictsAgree simulates a small network in one process to catch a node
// disqualifying a block that the node which mined it, and the rest of the network, accept.
//
// Three miners each build templates on their own virtual, from their own mempool, with their own
// coinbase, and relay every block to everyone else after a random delay - so parallel blocks, the
// same transaction mined twice, double spends sent to different miners, and reorgs all happen the way
// they do on the live network. Two observers mine nothing: one takes blocks in relay order, the other
// takes the whole DAG in a random parents-first order at the end.
//
// The invariant is the one a node relies on without ever checking: a block's UTXO verdict is a
// function of the block and its past, and nothing else. Arrival order, where a node's virtual happened
// to be, and which blocks it saw first must not change it. Every node that has resolved a block must
// have resolved it the same way.
//
// This is how reconcileWinningBranchUTXO was found: it split this network in 7 of 24 seeds and none
// once removed. The block version is pinned at 6 for the whole run, so a disagreement is not a version
// effect. Scale with HTND_RELAY_SIM_SEEDS, HTND_RELAY_SIM_ROUNDS and HTND_RELAY_SIM_SEED_START;
// HTND_RELAY_SIM_LOG=1 prints consensus warnings.
func TestRelayedBlockVerdictsAgree(t *testing.T) {
	seeds := envInt("HTND_RELAY_SIM_SEEDS", 4)
	rounds := envInt("HTND_RELAY_SIM_ROUNDS", 40)
	seedStart := envInt("HTND_RELAY_SIM_SEED_START", 1)
	if os.Getenv("HTND_RELAY_SIM_LOG") != "" {
		logger.SetLogLevels(logger.LevelWarn)
		logger.InitLogStdout(logger.LevelWarn)
	}

	previousVersion := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previousVersion)) })

	var failures []string
	for seed := seedStart; seed < seedStart+seeds; seed++ {
		failures = append(failures, runRelaySimulation(t, int64(seed), rounds)...)
	}
	if len(failures) > 0 {
		t.Fatalf("%d verdict disagreement(s):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}

type simCoin struct {
	outpoint externalapi.DomainOutpoint
	value    uint64
}

type simCoinPool struct {
	coins []simCoin
	index map[externalapi.DomainOutpoint]int
}

func (pool *simCoinPool) add(coin simCoin) {
	if _, ok := pool.index[coin.outpoint]; ok {
		return
	}
	pool.index[coin.outpoint] = len(pool.coins)
	pool.coins = append(pool.coins, coin)
}

func (pool *simCoinPool) remove(outpoint externalapi.DomainOutpoint) {
	i, ok := pool.index[outpoint]
	if !ok {
		return
	}
	last := len(pool.coins) - 1
	pool.coins[i] = pool.coins[last]
	pool.index[pool.coins[i].outpoint] = i
	pool.coins = pool.coins[:last]
	delete(pool.index, outpoint)
}

type simMempoolTx struct {
	tx   *externalapi.DomainTransaction
	born int
}

type simDelivery struct {
	block *externalapi.DomainBlock
	hash  *externalapi.DomainHash
	due   int
}

type simNode struct {
	name     string
	tc       testapi.TestConsensus
	teardown func(bool)
	coinbase *externalapi.DomainCoinbaseData
	mempool  []*simMempoolTx
	inbox    []*simDelivery
	has      map[externalapi.DomainHash]bool
	rejected map[externalapi.DomainHash]error
}

type simProduced struct {
	block          *externalapi.DomainBlock
	hash           *externalapi.DomainHash
	producer       string
	round          int
	producerStatus externalapi.BlockStatus
	transactions   int
}

// relayed returns block as a peer receives it: serialised to the wire format and back, so no
// in-memory state of the producing node (populated UTXO entries, fees, cached hashes) travels with it.
func relayed(block *externalapi.DomainBlock) *externalapi.DomainBlock {
	return appmessage.MsgBlockToDomainBlock(appmessage.DomainBlockToMsgBlock(block))
}

func (node *simNode) insert(block *externalapi.DomainBlock, hash *externalapi.DomainHash) {
	if node.has[*hash] {
		return
	}
	node.has[*hash] = true
	if err := node.tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
		node.rejected[*hash] = err
	}
}

func (node *simNode) parentsKnown(block *externalapi.DomainBlock) bool {
	for _, parent := range block.Header.DirectParents() {
		if !node.has[*parent] {
			return false
		}
	}
	return true
}

// drainInbox inserts every delivered block whose parents this node already has, repeating until
// nothing more can go in - a block that arrives before its parents waits, like an orphan does.
func (node *simNode) drainInbox(round int, ignoreDue bool) {
	for progress := true; progress; {
		progress = false
		remaining := node.inbox[:0]
		for _, delivery := range node.inbox {
			if (ignoreDue || delivery.due <= round) && node.parentsKnown(delivery.block) {
				node.insert(delivery.block, delivery.hash)
				progress = true
				continue
			}
			remaining = append(remaining, delivery)
		}
		node.inbox = remaining
	}
}

func runRelaySimulation(t *testing.T, seed int64, rounds int) []string {
	rng := rand.New(rand.NewSource(seed))

	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1} // every block past genesis is version 6
	params.BlockCoinbaseMaturity = 10
	config := &consensus.Config{Params: params}
	config.SkipProofOfWork = true

	scriptPublicKey, redeemScript := testutils.OpTrueScript()
	signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
	if err != nil {
		t.Fatalf("signature script: %+v", err)
	}

	factory := consensus.NewFactory()
	newNode := func(name string, minerID byte) *simNode {
		nodeConfig := *config
		tc, teardown, err := factory.NewTestConsensus(&nodeConfig, fmt.Sprintf("RelaySim_%d_%s", seed, name))
		if err != nil {
			t.Fatalf("NewTestConsensus %s: %+v", name, err)
		}
		node := &simNode{
			name: name, tc: tc, teardown: teardown,
			has:      map[externalapi.DomainHash]bool{*config.GenesisHash: true},
			rejected: map[externalapi.DomainHash]error{},
		}
		if minerID != 0 {
			node.coinbase = &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte{'m', minerID}}
		}
		return node
	}
	miners := []*simNode{newNode("minerA", 1), newNode("minerB", 2), newNode("minerC", 3)}
	relayObserver := newNode("relayObserver", 0)
	shuffledObserver := newNode("shuffledObserver", 0)
	everyone := append(append([]*simNode{}, miners...), relayObserver, shuffledObserver)
	defer func() {
		for _, node := range everyone {
			node.teardown(false)
		}
	}()
	// NewTestConsensus forces the process-wide block version back to 1 on every call, so this has to
	// come after the last node exists.
	constants.ForceSetBlockVersion(6)

	pool := &simCoinPool{index: map[externalapi.DomainOutpoint]int{}}
	var produced []*simProduced
	var producerProblems []string
	newSpend := func(coins []simCoin, fee uint64, outputs int) *externalapi.DomainTransaction {
		var total uint64
		inputs := make([]*externalapi.DomainTransactionInput, 0, len(coins))
		for _, coin := range coins {
			total += coin.value
			inputs = append(inputs, &externalapi.DomainTransactionInput{
				PreviousOutpoint: coin.outpoint,
				SignatureScript:  append([]byte(nil), signatureScript...),
				Sequence:         constants.MaxTxInSequenceNum,
			})
		}
		if total <= fee+uint64(outputs)*10_000 {
			return nil
		}
		remaining := total - fee
		transactionOutputs := make([]*externalapi.DomainTransactionOutput, 0, outputs)
		for i := 0; i < outputs; i++ {
			value := remaining / uint64(outputs)
			if i == outputs-1 {
				value = remaining - value*uint64(outputs-1)
			}
			transactionOutputs = append(transactionOutputs, &externalapi.DomainTransactionOutput{
				Value: value, ScriptPublicKey: scriptPublicKey,
			})
		}
		return &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion, Inputs: inputs, Outputs: transactionOutputs, Payload: []byte{},
		}
	}
	addOutputsToPool := func(transaction *externalapi.DomainTransaction) {
		id := consensushashing.TransactionID(transaction)
		for i, output := range transaction.Outputs {
			pool.add(simCoin{outpoint: externalapi.DomainOutpoint{TransactionID: *id, Index: uint32(i)}, value: output.Value})
		}
	}
	broadcast := func(transaction *externalapi.DomainTransaction, round int, to []*simNode) {
		for _, miner := range to {
			miner.mempool = append(miner.mempool, &simMempoolTx{tx: transaction, born: round})
		}
	}
	randomMiners := func() []*simNode {
		order := rng.Perm(len(miners))
		count := 1 + rng.Intn(len(miners))
		chosen := make([]*simNode, 0, count)
		for _, i := range order[:count] {
			chosen = append(chosen, miners[i])
		}
		return chosen
	}

	// A shared prefix, mined by one miner and delivered to everyone at once, so that every later fork
	// happens above a common chain the way it does on a running network - not straight off genesis,
	// which restorePastUTXO special-cases and which no mainnet reorg can reach.
	// HTND_RELAY_SIM_PREFIX=0 forks from genesis instead.
	prefix := 12
	if value, err := strconv.Atoi(os.Getenv("HTND_RELAY_SIM_PREFIX")); err == nil && value >= 0 {
		prefix = value
	}
	for i := 0; i < prefix; i++ {
		builder := miners[0]
		block, err := builder.tc.BuildBlock(builder.coinbase, nil)
		if err != nil {
			t.Fatalf("prefix BuildBlock: %+v", err)
		}
		hash := consensushashing.BlockHash(block)
		builder.has[*hash] = true
		if err := builder.tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
			t.Fatalf("prefix insert: %+v", err)
		}
		produced = append(produced, &simProduced{block: block, hash: hash, producer: builder.name, round: -1,
			producerStatus: externalapi.StatusUTXOValid})
		addOutputsToPool(block.Transactions[0])
		for _, node := range everyone {
			if node != builder && node != shuffledObserver {
				node.insert(block, hash)
			}
		}
	}

	var lastRoundTransactions []*externalapi.DomainTransaction
	for round := 0; round < rounds; round++ {
		for _, node := range everyone {
			if node != shuffledObserver {
				node.drainInbox(round, false)
			}
		}

		// The network's transaction flow.
		var thisRoundTransactions []*externalapi.DomainTransaction
		for n := rng.Intn(5); n > 0 && len(pool.coins) > 0; n-- {
			switch roll := rng.Float64(); {
			case roll < 0.15:
				// Double spend: two different transactions for one coin, sent to two different miners.
				coin := pool.coins[rng.Intn(len(pool.coins))]
				first := newSpend([]simCoin{coin}, 1_000, 1)
				second := newSpend([]simCoin{coin}, 2_000, 2)
				if first == nil || second == nil {
					continue
				}
				pool.remove(coin.outpoint)
				order := rng.Perm(len(miners))
				broadcast(first, round, []*simNode{miners[order[0]]})
				broadcast(second, round, []*simNode{miners[order[1]]})
				addOutputsToPool(first)
				addOutputsToPool(second)
			case roll < 0.30 && len(lastRoundTransactions) > 0:
				// A child of a transaction that may not be confirmed yet.
				parent := lastRoundTransactions[rng.Intn(len(lastRoundTransactions))]
				parentID := consensushashing.TransactionID(parent)
				coin := simCoin{outpoint: externalapi.DomainOutpoint{TransactionID: *parentID, Index: 0}, value: parent.Outputs[0].Value}
				child := newSpend([]simCoin{coin}, 1_500, 1+rng.Intn(2))
				if child == nil {
					continue
				}
				pool.remove(coin.outpoint)
				broadcast(child, round, randomMiners())
				addOutputsToPool(child)
				thisRoundTransactions = append(thisRoundTransactions, child)
			default:
				count := 1 + rng.Intn(2)
				var coins []simCoin
				for _, i := range rng.Perm(len(pool.coins))[:min(count, len(pool.coins))] {
					coins = append(coins, pool.coins[i])
				}
				transaction := newSpend(coins, uint64(1_000+rng.Intn(4_000)), 1+rng.Intn(2))
				if transaction == nil {
					continue
				}
				for _, coin := range coins {
					pool.remove(coin.outpoint)
				}
				broadcast(transaction, round, randomMiners())
				addOutputsToPool(transaction)
				thisRoundTransactions = append(thisRoundTransactions, transaction)
			}
		}
		lastRoundTransactions = thisRoundTransactions

		// One to three miners find a block this round, each on its own view of the DAG.
		minersThisRound := 1
		if roll := rng.Float64(); roll < 0.10 {
			minersThisRound = 3
		} else if roll < 0.40 {
			minersThisRound = 2
		}
		for _, i := range rng.Perm(len(miners))[:minersThisRound] {
			miner := miners[i]
			var selected []*externalapi.DomainTransaction
			spentInTemplate := map[externalapi.DomainOutpoint]bool{}
			kept := miner.mempool[:0]
			for _, entry := range miner.mempool {
				candidate := entry.tx.Clone()
				if err := miner.tc.ValidateTransactionAndPopulateWithConsensusData(candidate); err != nil {
					if round-entry.born < 25 {
						kept = append(kept, entry)
					}
					continue
				}
				conflicts := false
				for _, input := range candidate.Inputs {
					if spentInTemplate[input.PreviousOutpoint] {
						conflicts = true
					}
				}
				if conflicts || len(selected) >= 25 {
					kept = append(kept, entry)
					continue
				}
				for _, input := range candidate.Inputs {
					spentInTemplate[input.PreviousOutpoint] = true
				}
				selected = append(selected, candidate)
			}
			miner.mempool = kept

			block, err := miner.tc.BuildBlock(miner.coinbase, selected)
			if err != nil {
				producerProblems = append(producerProblems, fmt.Sprintf("seed %d round %d %s: BuildBlock: %v", seed, round, miner.name, err))
				continue
			}
			hash := consensushashing.BlockHash(block)
			miner.has[*hash] = true
			// A mined block reaches its own node through submitBlock, serialised like any other, so the
			// UTXO entries the mempool populated into the template's transactions do not come with it.
			if err := miner.tc.ValidateAndInsertBlock(relayed(block), true, true); err != nil {
				producerProblems = append(producerProblems, fmt.Sprintf("seed %d round %d %s rejected its own template %s: %v", seed, round, miner.name, hash, err))
				continue
			}
			info, err := miner.tc.GetBlockInfo(hash)
			if err != nil {
				t.Fatalf("GetBlockInfo: %+v", err)
			}
			produced = append(produced, &simProduced{block: block, hash: hash, producer: miner.name, round: round,
				producerStatus: info.BlockStatus, transactions: len(block.Transactions) - 1})
			addOutputsToPool(block.Transactions[0])

			for _, node := range everyone {
				if node == miner {
					continue
				}
				node.inbox = append(node.inbox, &simDelivery{block: block, hash: hash, due: round + 1 + rng.Intn(3)})
			}
		}
	}

	// Let everything arrive everywhere, then hand the shuffled observer the whole DAG in a random
	// parents-first order.
	for _, node := range everyone {
		if node != shuffledObserver {
			node.drainInbox(rounds, true)
		}
	}
	for remaining := append([]*simProduced(nil), produced...); len(remaining) > 0; {
		var ready []int
		for i, p := range remaining {
			if shuffledObserver.parentsKnown(p.block) {
				ready = append(ready, i)
			}
		}
		if len(ready) == 0 {
			t.Fatalf("seed %d: shuffled observer cannot place %d blocks", seed, len(remaining))
		}
		pick := ready[rng.Intn(len(ready))]
		shuffledObserver.insert(remaining[pick].block, remaining[pick].hash)
		remaining = append(remaining[:pick], remaining[pick+1:]...)
	}
	for _, node := range everyone {
		if err := node.tc.ResolveVirtual(func(uint64, uint64) {}); err != nil {
			producerProblems = append(producerProblems, fmt.Sprintf("seed %d %s ResolveVirtual: %v", seed, node.name, err))
		}
	}

	var failures []string

	// GHOSTDAG first: coloring is computed once per block from its past alone. If two nodes colored the
	// same block differently, every UTXO verdict on top of it can differ for that reason alone.
	short := func(hash *externalapi.DomainHash) string {
		if hash == nil {
			return "nil"
		}
		return hash.String()[:8]
	}
	for _, p := range produced {
		byNode := map[string][]string{}
		for _, node := range everyone {
			info, err := node.tc.GetBlockInfo(p.hash)
			if err != nil || !info.Exists {
				continue
			}
			summary := fmt.Sprintf("sp=%s blueScore=%d blueWork=%s dynamicK=%d blues=%d reds=%d", short(info.SelectedParent),
				info.BlueScore, info.BlueWork, info.DynamicK, len(info.MergeSetBlues), len(info.MergeSetReds))
			byNode[summary] = append(byNode[summary], node.name)
		}
		if len(byNode) > 1 {
			failures = append(failures, fmt.Sprintf("seed %d: block %s has different GHOSTDAG data on different nodes: %v", seed, short(p.hash), byNode))
			break
		}
	}

	// Then every resolved verdict.
	isResolved := func(status externalapi.BlockStatus) bool {
		return status == externalapi.StatusUTXOValid || status == externalapi.StatusDisqualifiedFromChain ||
			status == externalapi.StatusInvalid
	}
	disagreements, roots := 0, 0
	for _, p := range produced {
		verdicts := map[externalapi.BlockStatus][]string{}
		if isResolved(p.producerStatus) {
			verdicts[p.producerStatus] = append(verdicts[p.producerStatus], p.producer+"(at mining)")
		}
		var accepting, rootNode *simNode
		for _, node := range everyone {
			info, err := node.tc.GetBlockInfo(p.hash)
			if err != nil {
				t.Fatalf("GetBlockInfo: %+v", err)
			}
			if !info.Exists || !isResolved(info.BlockStatus) {
				continue
			}
			verdicts[info.BlockStatus] = append(verdicts[info.BlockStatus], node.name)
			if info.BlockStatus == externalapi.StatusUTXOValid && accepting == nil {
				accepting = node
			}
			// A root: this node disqualified the block while still holding its selected parent valid,
			// so the failure is this block's own and not inherited.
			if info.BlockStatus == externalapi.StatusDisqualifiedFromChain && rootNode == nil {
				if parentInfo, err := node.tc.GetBlockInfo(info.SelectedParent); err == nil && parentInfo.BlockStatus == externalapi.StatusUTXOValid {
					rootNode = node
				}
			}
		}
		if len(verdicts) <= 1 {
			continue
		}
		disagreements++
		report := fmt.Sprintf("seed %d: block %s mined by %s in round %d with %d transactions - verdicts %v",
			seed, p.hash, p.producer, p.round, p.transactions, verdicts)
		if rootNode != nil && accepting != nil && roots < 3 {
			roots++
			report += "\n" + compareStoredRecords(p, rootNode, accepting)
		}
		failures = append(failures, report)
	}

	tips := map[string][]string{}
	for _, node := range everyone {
		selectedParent, err := node.tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		tips[short(selectedParent)] = append(tips[short(selectedParent)], node.name)
	}
	if len(tips) > 1 {
		failures = append(failures, fmt.Sprintf("seed %d: nodes ended on different virtual selected parents: %v", seed, tips))
	}

	t.Logf("seed %d: %d blocks, %d disagreement(s), %d producer problem(s)", seed, len(produced), disagreements, len(producerProblems))
	for _, problem := range producerProblems {
		t.Logf("  %s", problem)
	}
	return failures
}

// compareStoredRecords sets a disqualifying node's stored record of one block beside an accepting
// node's: its multiset against the header commitment, which merged transactions each accepted, and
// the entry each populated for every coin those transactions spend. Same acceptance with a different
// multiset means a spent coin was populated differently - usually with a different DAA stamp.
func compareStoredRecords(p *simProduced, rejecting, accepting *simNode) string {
	lines := []string{fmt.Sprintf("    root: disqualified by %s, accepted by %s", rejecting.name, accepting.name)}
	commitment := p.block.Header.UTXOCommitment()
	for _, node := range []*simNode{rejecting, accepting} {
		stored, err := node.tc.MultisetStore().Get(node.tc.DatabaseContext(), model.NewStagingArea(), p.hash)
		if err != nil {
			lines = append(lines, fmt.Sprintf("    %s: no stored multiset (%v)", node.name, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("    %s: multiset matches header commitment: %t", node.name, stored.Hash().Equal(commitment)))
	}

	type acceptance struct {
		accepted bool
		entries  map[externalapi.DomainOutpoint]externalapi.UTXOEntry
	}
	acceptanceOf := func(node *simNode) map[string]acceptance {
		result := map[string]acceptance{}
		data, err := node.tc.AcceptanceDataStore().Get(node.tc.DatabaseContext(), model.NewStagingArea(), p.hash)
		if err != nil {
			return result
		}
		for _, blockAcceptance := range data {
			for i, transactionAcceptance := range blockAcceptance.TransactionAcceptanceData {
				key := fmt.Sprintf("merged %s tx#%d %s", blockAcceptance.BlockHash.String()[:8], i,
					consensushashing.TransactionID(transactionAcceptance.Transaction).String()[:12])
				entries := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
				for _, input := range transactionAcceptance.Transaction.Inputs {
					entries[input.PreviousOutpoint] = input.UTXOEntry
				}
				result[key] = acceptance{accepted: transactionAcceptance.IsAccepted, entries: entries}
			}
		}
		return result
	}
	describe := func(entry externalapi.UTXOEntry) string {
		if entry == nil {
			return "<nil>"
		}
		return fmt.Sprintf("amount=%d stamp=%d coinbase=%t", entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase())
	}
	onRejecting, onAccepting := acceptanceOf(rejecting), acceptanceOf(accepting)
	for key, there := range onAccepting {
		here, present := onRejecting[key]
		if !present || here.accepted != there.accepted {
			lines = append(lines, fmt.Sprintf("    %s: accepted by %s=%t, by %s=%t", key, accepting.name, there.accepted,
				rejecting.name, present && here.accepted))
			continue
		}
		if !there.accepted {
			continue
		}
		for outpoint, entry := range there.entries {
			if other := here.entries[outpoint]; other == nil || entry == nil || !other.Equal(entry) {
				lines = append(lines, fmt.Sprintf("    %s spends %s:%d - %s populated it as %s, %s as %s", key,
					outpoint.TransactionID.String()[:12], outpoint.Index, accepting.name, describe(entry), rejecting.name, describe(other)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func restoredPastSet(tc testapi.TestConsensus, blockHash *externalapi.DomainHash) (map[externalapi.DomainOutpoint]externalapi.UTXOEntry, error) {
	iterator, err := tc.ConsensusStateManager().RestorePastUTXOSetIterator(model.NewStagingArea(), blockHash)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	set := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		set[*outpoint] = entry
	}
	return set, nil
}

// restoredPastMultisetHash rebuilds blockHash's past UTXO set from the node's stored diffs and hashes
// it. For a valid block it must equal the block's own header UTXO commitment.
func restoredPastMultisetHash(tc testapi.TestConsensus, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	set, err := restoredPastSet(tc, blockHash)
	if err != nil {
		return nil, err
	}
	hashed := multiset.New()
	for outpoint, entry := range set {
		serialized, err := utxo.SerializeUTXO(entry, &outpoint)
		if err != nil {
			return nil, err
		}
		hashed.Add(serialized)
	}
	return hashed.Hash(), nil
}
