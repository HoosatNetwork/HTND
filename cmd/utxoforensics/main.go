// Command utxoforensics inspects a node database offline to answer one question: when a block's
// UTXO commitment doesn't match its header, which side is wrong and why.
//
// It never writes consensus data, but the database engine itself replays write-ahead logs on open,
// so ALWAYS point it at a COPY of datadir2, never at a live node's directory. The directory must be
// a pebble datadir2 (the format htnd writes); opening it with any other engine will destroy it.
//
// Modes:
//
//	-block <hash>   Replay the block's own stored acceptance data onto its selected parent's stored
//	                multiset under both candidate DAA-stamp rules - the merging block's DAA score
//	                (consensus, see utxo.AcceptedUTXOBlockDAAScore) and the merge-set block's own -
//	                and report which one reproduces the header commitment.
//	-scan N         The same, for the pruning point and the next N selected-chain blocks.
//	-basecheck      Hash the served pruning point UTXO set and compare it to the pruning point's own
//	                header commitment; if it agrees, walk forward from it applying acceptance data
//	                under each rule, which discriminates the rules against real mined headers.
//	-pphistory      Every pruning point this database has had, with its header commitment and stored
//	                multiset, and whether the served bucket is merely stale by an advancement.
//	-reconstruct    Rebuild the pruning point's absolute UTXO set from virtual's UTXO table plus the
//	                stored diff chain and diff it entry-by-entry against the served bucket, which
//	                separates "the set has the wrong members" from "the set has the wrong values".
//
// A node whose per-block multisets match their headers but whose served bucket does not is serving a
// broken pruning point UTXO set to every peer that syncs from it, and -reconstruct names the
// offending outpoints.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/acceptancedatastore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/consensusstatestore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/daablocksstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/headersselectedchainstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/headersselectedtipstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/multisetstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/utxodiffstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/processes/coinbasemanager"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/prefixmanager"
	infradatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/pebble"
	"github.com/pkg/errors"
)

var (
	dbPath     = flag.String("db", "", "path to a COPY of a datadir2 (pebble)")
	surveyPath = flag.String("survey", "", "cluster a UTXO survey JSONL file (written by a node run with "+
		"HTND_UTXO_SURVEY) and print the classification table: failures by error and by A/B/C class, their "+
		"spread over DAA scores, whether the pruning point import was already offset, which outpoints block "+
		"more than one block, and which of those are present under disagreeing SerializeUTXO preimages - "+
		"i.e. handling rather than loss. Needs no -db.")
	blockArg       = flag.String("block", "", "block hash to analyze")
	scanN          = flag.Int("scan", 0, "also scan N selected-chain blocks up from the pruning point")
	prefixOverride = flag.Int("prefix", -1, "read this database prefix (0 or 1) instead of the active one - "+
		"needed to inspect the staging consensus an IBD-with-headers-proof is still building, which lives "+
		"under the inactive prefix until it is committed")
	prefixOverride2 = flag.Int("prefix2", -1, "prefix override for -db2 (see -prefix)")
	srcA            = flag.String("src", "bucket", "which UTXO set of -db to use for -diffsets: \"bucket\" (the "+
		"served pruning point set) or \"imported\" (the raw set as received from a peer, before it is copied "+
		"into the served bucket)")
	srcB    = flag.String("src2", "bucket", "which UTXO set of -db2 to use for -diffsets (see -src)")
	dbPath2 = flag.String("db2", "", "second database COPY; with -diffsets, its pruning point UTXO set is "+
		"compared entry-by-entry against -db's")
	diffSets = flag.Bool("diffsets", false, "diff the pruning point UTXO sets of -db and -db2 (which must be at "+
		"the same pruning point) - the direct test of whether two peers serve the same set")
	reconstruct = flag.Bool("reconstruct", false, "rebuild the pruning point's absolute UTXO set from virtual's "+
		"UTXO table plus the stored diff chain, verify it against the pruning point's header commitment, and "+
		"diff it entry-by-entry against the served bucket")
	ppHistory = flag.Bool("pphistory", false, "list every pruning point by index with its header commitment and "+
		"stored multiset, and test whether the served bucket hash equals any of them (i.e. whether the bucket "+
		"is simply stale by one or more pruning point advancements)")
	recover_ = flag.Bool("recover", false, "with -replaycheck: try every combination of the disagreements found "+
		"between the materialised table and the acceptance replay as corrections to the pruning point set, "+
		"looking for one that reproduces the pruning point's header commitment")
	replayCheck = flag.Bool("replaycheck", false, "replay every chain block's stored acceptance data from the "+
		"pruning point up to virtual onto the pruning point UTXO set, and diff the result against virtual's "+
		"materialised UTXO table - both are enumerable sets built from the same starting point, so the "+
		"starting point's own errors cancel and what remains is the materialised table's drift")
	supplyByDAA = flag.Uint64("supplybydaa", 0, "bucket size in DAA scores. Reports how much of this "+
		"node's supply carries a stamp in each bucket, with a running total, so growth since a point "+
		"in time can be accounted for rather than guessed at")

	daaWindow = flag.String("daawindow", "", "\"from-to\" DAA score range. Groups every coin this "+
		"node holds whose stamp falls in that range by address, largest first - which addresses "+
		"gained, how much, and in how many coins. Only coins still UNSPENT are visible, so this is "+
		"what was received and kept, not everything that moved")

	addressUTXOs = flag.String("addressutxos", "", "list the coins one address holds in this node's "+
		"CONSENSUS UTXO set - amount, DAA score and whether each was minted by a coinbase. Answers "+
		"where an address's balance actually came from, which -balancecheck can only point at")

	referenceDAAScore = flag.Uint64("referencedaascore", 0, "the DAA score the -balancecheck "+
		"reference snapshot was taken at. When zero it is estimated from the snapshot's timestamp and "+
		"the network's target block rate, which is only as good as the network having run at its "+
		"target rate. Supply it when you know it and the expected-emission figure becomes exact")

	balanceCheck = flag.String("balancecheck", "", "path to a reference balance snapshot CSV "+
		"(script_public_key_address,balance) and compare this node's UTXO set against it per address. "+
		"Answers how far a datadir has drifted from a known-good point in time, which is how you pick "+
		"the least corrupted one to rebaseline from. Balances are summed from the CONSENSUS UTXO set, "+
		"not the utxoindex, so an index bug cannot flatter the result")

	indexCheck = flag.Bool("indexcheck", false, "compare every entry in this node's utxoindex "+
		"against the consensus UTXO set in the same database. The index is what GetUtxosByAddresses "+
		"answers from - wallet balances, explorer pages - and it is derived from consensus, so any "+
		"disagreement is the index being wrong about the node's own state. Reports entries whose "+
		"amount or BlockDAAScore differ, and entries the index holds that consensus does not")

	stampCheck = flag.Bool("stampcheck", false, "compare every unspent coin's BlockDAAScore against "+
		"the DAA score of the chain block this node's own acceptance data says accepted it. "+
		"AcceptedUTXOBlockDAAScore is the identity, so the two must be equal by definition - a "+
		"disagreement is the node contradicting itself, with no reorg or cross-node explanation "+
		"available. BlockDAAScore is part of the SerializeUTXO preimage, so each disagreement is a "+
		"coin whose commitment contribution is permanently wrong")
	stampCheckDepth = flag.Int("stampdepth", 400000, "chain blocks back from the tip for -stampcheck")

	virtualCheck = flag.Bool("virtualcheck", false, "hash virtual's materialised UTXO table and compare it to "+
		"virtual's own stored multiset - the same quantity maintained by two different mechanisms, so a "+
		"mismatch localises the drift to the materialised table rather than to the multiset chain")
	hasOutpoints = flag.String("hasoutpoints", "", "path to a file of \"txid:index\" lines; report which of "+
		"them the pruning point UTXO set of -db holds. Answers whether two nodes' gaps are the SAME coins "+
		"without needing them at the same pruning point: take the coins one node found missing and ask "+
		"another node's set for them. Present in the other set means the gaps differ and a bundle built "+
		"from one node is not correct for the other; absent in both means one shared gap.")

	baseTest = flag.Bool("basecheck", false, "hash the stored pruning point UTXO set, compare it to the pruning "+
		"point's header commitment, and - if it matches - use it as a network-sourced base to discriminate the "+
		"two DAA-stamp rules on the next selected-chain blocks")
)

type stores struct {
	db         model.DBManager
	headers    model.BlockHeaderStore
	blocks     model.BlockStore
	accept     model.AcceptanceDataStore
	ms         model.MultisetStore
	gd         model.GHOSTDAGDataStore
	daa        model.DAABlocksStore
	chain      model.HeadersSelectedChainStore
	pruning    model.PruningStore
	state      model.ConsensusStateStore
	diffs      model.UTXODiffStore
	headersTip model.HeaderSelectedTipStore
}

func main() {
	flag.Parse()

	// Clustering a survey reads only the JSONL file the node wrote; there is nothing to open a
	// database for, and requiring one would mean carrying a copy of a datadir around to answer
	// questions the survey already contains the answers to.
	if *surveyPath != "" {
		records, err := utxosurvey.Read(*surveyPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read survey: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(utxosurvey.Summarize(records))
		if *dbPath == "" {
			return
		}
	}

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "-db is required")
		os.Exit(2)
	}

	db, err := pebble.NewPebbleDB(*dbPath, 256)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	s, err := openStores(db, *prefixOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stores: %v\n", err)
		os.Exit(1)
	}
	sa := model.NewStagingArea()

	if *blockArg != "" {
		h, err := externalapi.NewDomainHashFromString(*blockArg)
		if err != nil {
			panic(err)
		}
		analyze(s, sa, h)
	}

	if *diffSets {
		if *dbPath2 == "" {
			fmt.Fprintln(os.Stderr, "-diffsets requires -db2")
			os.Exit(2)
		}
		db2, err := pebble.NewPebbleDB(*dbPath2, 256)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db2: %v\n", err)
			os.Exit(1)
		}
		defer db2.Close()
		s2, err := openStores(db2, *prefixOverride2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stores for db2: %v\n", err)
			os.Exit(1)
		}
		diffPruningPointSets(s, s2, sa)
	}

	if *replayCheck {
		replayForwardCheck(s, sa)
	}

	if *virtualCheck {
		virtualTableCheck(s, sa)
	}

	if *stampCheck {
		stampConsistencyCheck(s, sa, *stampCheckDepth)
	}

	if *indexCheck {
		utxoIndexConsistencyCheck(db, s, sa)
	}

	if *balanceCheck != "" {
		balanceComparison(s, sa, *balanceCheck)
	}

	if *addressUTXOs != "" {
		listAddressUTXOs(s, sa, *addressUTXOs)
	}

	if *daaWindow != "" {
		addressesGainingInDAAWindow(s, sa, *daaWindow)
	}

	if *supplyByDAA > 0 {
		supplyHistogramByDAAScore(s, sa, *supplyByDAA)
	}

	if *reconstruct {
		reconstructPruningPointSet(s, sa)
	}

	if *ppHistory {
		pruningPointHistory(s, sa)
	}

	if *baseTest {
		baseCheck(s, sa)
	}

	if *hasOutpoints != "" {
		lookUpOutpoints(s, *hasOutpoints)
	}

	if *scanN > 0 {
		pp, err := s.pruning.PruningPoint(s.db, sa)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pruning point: %v\n", err)
			return
		}
		fmt.Printf("\n=== pruning point %s\n", pp)
		analyze(s, sa, pp)
		idx, err := s.chain.GetIndexByHash(s.db, sa, pp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "chain index of pruning point: %v\n", err)
			return
		}
		for i, checked := idx+1, 0; checked < *scanN; i++ {
			h, err := s.chain.GetHashByIndex(s.db, sa, i)
			if err != nil {
				fmt.Printf("stop at chain index %d: %v\n", i, err)
				break
			}
			if analyze(s, sa, h) {
				checked++
			}
		}
	}
}

func analyze(s *stores, sa *model.StagingArea, blockHash *externalapi.DomainHash) bool {
	header, err := s.headers.BlockHeader(s.db, sa, blockHash)
	if err != nil {
		fmt.Printf("%s: no header (%v)\n", blockHash, err)
		return false
	}
	gd, err := s.gd.Get(s.db, sa, blockHash, false)
	if err != nil {
		fmt.Printf("%s: no ghostdag data (%v)\n", blockHash, err)
		return false
	}
	acceptanceData, err := s.accept.Get(s.db, sa, blockHash)
	if err != nil {
		fmt.Printf("%s: no acceptance data (%v)\n", blockHash, err)
		return false
	}
	parentMS, err := s.ms.Get(s.db, sa, gd.SelectedParent())
	if err != nil {
		fmt.Printf("%s: no multiset for selected parent %s (%v)\n", blockHash, gd.SelectedParent(), err)
		return false
	}
	storedMS, storedMSErr := s.ms.Get(s.db, sa, blockHash)

	msMerging := parentMS.Clone()  // v2.16.0 / mainnet rule
	msCreating := parentMS.Clone() // post-96efc0d3d master rule

	var scores []string
	for _, bad := range acceptanceData {
		creating, err := s.ownDAAScore(sa, bad.BlockHash)
		if err != nil {
			fmt.Printf("%s: no DAA score for merge-set block %s (%v)\n", blockHash, bad.BlockHash, err)
			return false
		}
		accepted := 0
		for _, tad := range bad.TransactionAcceptanceData {
			if tad.IsAccepted {
				accepted++
			}
		}
		scores = append(scores, fmt.Sprintf("%s daa=%d accepted=%d/%d",
			bad.BlockHash, creating, accepted, len(bad.TransactionAcceptanceData)))
		for i, tad := range bad.TransactionAcceptanceData {
			if !tad.IsAccepted {
				continue
			}
			if err := addTx(msMerging, tad.Transaction, header.DAAScore(), i == 0); err != nil {
				fmt.Printf("%s: %v\n", blockHash, err)
				return false
			}
			if err := addTx(msCreating, tad.Transaction, creating, i == 0); err != nil {
				fmt.Printf("%s: %v\n", blockHash, err)
				return false
			}
		}
	}

	expected := header.UTXOCommitment()
	mergingHash := msMerging.Hash()
	creatingHash := msCreating.Hash()

	verdict := "NEITHER RULE MATCHES"
	switch {
	case mergingHash.Equal(expected) && creatingHash.Equal(expected):
		verdict = "BOTH (merge set is DAA-degenerate)"
	case mergingHash.Equal(expected):
		verdict = "MERGING-block rule matches header (v2.16.0 / mainnet)"
	case creatingHash.Equal(expected):
		verdict = "CREATING-block rule matches header (post-96efc0d3d master)"
	}

	stored := "<none>"
	if storedMSErr == nil {
		stored = storedMS.Hash().String()
	}

	fmt.Printf("block %s\n  daaScore=%d blueScore=%d selectedParent=%s mergeSetEntries=%d\n"+
		"  header commitment : %s\n  merging-block rule: %s\n  creating-block rule: %s\n"+
		"  stored multiset   : %s\n  => %s\n",
		blockHash, header.DAAScore(), gd.BlueScore(), gd.SelectedParent(), len(acceptanceData),
		expected, mergingHash, creatingHash, stored, verdict)
	for _, sc := range scores {
		fmt.Printf("     mergeset: %s\n", sc)
	}
	return true
}

func (s *stores) ownDAAScore(sa *model.StagingArea, blockHash *externalapi.DomainHash) (uint64, error) {
	header, err := s.headers.BlockHeader(s.db, sa, blockHash)
	if err != nil {
		return s.daa.DAAScore(s.db, sa, blockHash)
	}
	return header.DAAScore(), nil
}

func addTx(ms model.Multiset, transaction *externalapi.DomainTransaction, daaScore uint64, isCoinbase bool) error {
	transactionID := consensushashing.TransactionID(transaction)
	for _, input := range transaction.Inputs {
		if input.UTXOEntry == nil {
			return fmt.Errorf("input of %s has no UTXO entry in stored acceptance data", transactionID)
		}
		serialized, err := utxo.SerializeUTXO(input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
		ms.Remove(serialized)
	}
	for i, output := range transaction.Outputs {
		if i > math.MaxUint32 {
			return fmt.Errorf("output index overflow")
		}
		outpoint := &externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(i)}
		entry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, daaScore)
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return err
		}
		ms.Add(serialized)
	}
	return nil
}

// baseCheck rebuilds a multiset from the stored pruning point UTXO set - which arrived over the wire
// from a peer running the released node, so its BlockDAAScore stamps are the network's, not this
// node's - and checks it against the pruning point's own header commitment. If they agree, that
// multiset is a trustworthy, offset-free base, and applying the next chain blocks' acceptance data to
// it under each candidate DAA-stamp rule says which rule mainnet headers were actually produced with.
func baseCheck(s *stores, sa *model.StagingArea) {
	pp, err := s.pruning.PruningPoint(s.db, sa)
	if err != nil {
		fmt.Printf("pruning point: %v\n", err)
		return
	}
	ppHeader, err := s.headers.BlockHeader(s.db, sa, pp)
	if err != nil {
		fmt.Printf("pruning point header: %v\n", err)
		return
	}

	if importedMS, err := s.pruning.ImportedPruningPointMultiset(s.db); err == nil {
		fmt.Printf("  imported (peer-sourced) pruning point multiset still present: %s\n", importedMS.Hash())
	} else {
		fmt.Printf("  imported (peer-sourced) pruning point multiset: not available (%v)\n", err)
	}
	if importedIter, err := s.pruning.ImportedPruningPointUTXOIterator(s.db); err == nil {
		imported := multiset.New()
		n := 0
		for ok := importedIter.First(); ok; ok = importedIter.Next() {
			outpoint, entry, err := importedIter.Get()
			if err != nil {
				break
			}
			serialized, err := utxo.SerializeUTXO(entry, outpoint)
			if err != nil {
				break
			}
			imported.Add(serialized)
			n++
		}
		importedIter.Close()
		fmt.Printf("  imported (peer-sourced) pruning point UTXO set: %d entries, hash %s\n", n, imported.Hash())
	} else {
		fmt.Printf("  imported (peer-sourced) pruning point UTXO set: not available (%v)\n", err)
	}

	iterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("pruning point UTXO iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	base := multiset.New()
	count := 0
	daaHistogram := map[uint64]int{}
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Printf("pruning point UTXO iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			fmt.Printf("SerializeUTXO: %v\n", err)
			return
		}
		base.Add(serialized)
		count++
		if entry.BlockDAAScore() > ppHeader.DAAScore()-200 {
			daaHistogram[entry.BlockDAAScore()]++
		}
	}

	baseHash := base.Hash()
	fmt.Printf("\n=== pruning point UTXO set base check\n  pruning point   : %s (daaScore=%d)\n"+
		"  entries         : %d\n  header commitment: %s\n  set hash         : %s\n  => %s\n",
		pp, ppHeader.DAAScore(), count, ppHeader.UTXOCommitment(), baseHash,
		map[bool]string{true: "MATCH - this set is a network-correct, offset-free base",
			false: "MISMATCH - the stored pruning point UTXO set does not hash to its own header"}[baseHash.Equal(ppHeader.UTXOCommitment())])

	if !baseHash.Equal(ppHeader.UTXOCommitment()) {
		// The bucket is wrong. Is it *stale* - i.e. does it hash to some other chain block's correct
		// multiset - or is it corrupt in a way that corresponds to no block at all? That distinction
		// says whether the bucket was left pointing at the wrong point or genuinely lost/gained entries.
		idx, err := s.chain.GetIndexByHash(s.db, sa, pp)
		if err != nil {
			fmt.Printf("  (could not locate pruning point on the selected chain: %v)\n", err)
			return
		}
		window := uint64(20000)
		lo := uint64(0)
		if idx > window {
			lo = idx - window
		}
		matched := false
		for i := lo; i <= idx+window; i++ {
			h, err := s.chain.GetHashByIndex(s.db, sa, i)
			if err != nil {
				continue
			}
			ms, err := s.ms.Get(s.db, sa, h)
			if err != nil {
				continue
			}
			if ms.Hash().Equal(baseHash) {
				if i == idx {
					fmt.Printf("  the bucket hash equals THIS node's own stored multiset for the pruning " +
						"point - bucket and per-block multiset chain agree with each other and both differ " +
						"from the header, i.e. the offset was inherited (imported), not introduced by the " +
						"bucket's own maintenance\n")
				} else {
					fmt.Printf("  the bucket hash equals the stored multiset of chain block %s at index %d "+
						"(pruning point is at index %d, offset %+d) - the bucket is STALE, not corrupt\n",
						h, i, idx, int64(i)-int64(idx))
				}
				matched = true
				break
			}
		}
		if !matched {
			fmt.Printf("  the bucket hash matches no chain block's stored multiset within +/-%d of the "+
				"pruning point - the bucket contents correspond to no point on the chain\n", window)
		}
		return
	}

	// Walk forward from the pruning point applying each chain block's acceptance data to a copy of the
	// verified base under each rule, comparing to that block's own header commitment at every step.
	msMerging := base.Clone()
	msCreating := base.Clone()
	idx, err := s.chain.GetIndexByHash(s.db, sa, pp)
	if err != nil {
		fmt.Printf("chain index of pruning point: %v\n", err)
		return
	}
	for i := idx + 1; i <= idx+uint64(max(*scanN, 5)); i++ {
		blockHash, err := s.chain.GetHashByIndex(s.db, sa, i)
		if err != nil {
			fmt.Printf("stop at chain index %d: %v\n", i, err)
			return
		}
		header, err := s.headers.BlockHeader(s.db, sa, blockHash)
		if err != nil {
			fmt.Printf("%s: no header (%v)\n", blockHash, err)
			return
		}
		acceptanceData, err := s.accept.Get(s.db, sa, blockHash)
		if err != nil {
			fmt.Printf("%s: no acceptance data (%v)\n", blockHash, err)
			return
		}
		for _, bad := range acceptanceData {
			creating, err := s.ownDAAScore(sa, bad.BlockHash)
			if err != nil {
				fmt.Printf("%s: no DAA score for %s (%v)\n", blockHash, bad.BlockHash, err)
				return
			}
			for j, tad := range bad.TransactionAcceptanceData {
				if !tad.IsAccepted {
					continue
				}
				if err := addTx(msMerging, tad.Transaction, header.DAAScore(), j == 0); err != nil {
					fmt.Printf("%s: %v\n", blockHash, err)
					return
				}
				if err := addTx(msCreating, tad.Transaction, creating, j == 0); err != nil {
					fmt.Printf("%s: %v\n", blockHash, err)
					return
				}
			}
		}
		mh, ch := msMerging.Hash(), msCreating.Hash()
		verdict := "NEITHER"
		switch {
		case mh.Equal(header.UTXOCommitment()) && ch.Equal(header.UTXOCommitment()):
			verdict = "BOTH (DAA-degenerate merge set)"
		case mh.Equal(header.UTXOCommitment()):
			verdict = "MERGING-block rule reproduces the header"
		case ch.Equal(header.UTXOCommitment()):
			verdict = "CREATING-block rule reproduces the header"
		}
		fmt.Printf("  chain[%d] %s daa=%d\n    header  : %s\n    merging : %s\n    creating: %s\n    => %s\n",
			i, blockHash, header.DAAScore(), header.UTXOCommitment(), mh, ch, verdict)
	}
}

// pruningPointHistory walks every pruning point this database has ever had, printing for each its own
// header commitment and the per-block multiset this node stored for it, and then checks the served
// bucket's hash against all of them. A bucket that hashes to an OLDER pruning point's value was
// simply never advanced; a bucket that matches none of them lost or gained entries.
func pruningPointHistory(s *stores, sa *model.StagingArea) {
	currentIndex, err := s.pruning.CurrentPruningPointIndex(s.db, sa)
	if err != nil {
		fmt.Printf("current pruning point index: %v\n", err)
		return
	}
	fmt.Printf("\n=== pruning point history (current index %d)\n", currentIndex)

	type ppRecord struct {
		index      uint64
		hash       *externalapi.DomainHash
		commitment *externalapi.DomainHash
		stored     *externalapi.DomainHash
	}
	var records []ppRecord
	for i := uint64(0); i <= currentIndex; i++ {
		hash, err := s.pruning.PruningPointByIndex(s.db, sa, i)
		if err != nil {
			fmt.Printf("  [%d] <unavailable: %v>\n", i, err)
			continue
		}
		rec := ppRecord{index: i, hash: hash}
		if header, err := s.headers.BlockHeader(s.db, sa, hash); err == nil {
			rec.commitment = header.UTXOCommitment()
		}
		if ms, err := s.ms.Get(s.db, sa, hash); err == nil {
			rec.stored = ms.Hash()
		}
		agree := rec.commitment != nil && rec.stored != nil && rec.stored.Equal(rec.commitment)
		fmt.Printf("  [%d] %s\n       header=%s\n       stored=%s  (agree=%t)\n",
			i, hash, rec.commitment, rec.stored, agree)
		records = append(records, rec)
	}

	iterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("  pruning point UTXO iterator: %v\n", err)
		return
	}
	defer iterator.Close()
	bucket := multiset.New()
	count := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Printf("  bucket iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			fmt.Printf("  SerializeUTXO: %v\n", err)
			return
		}
		bucket.Add(serialized)
		count++
	}
	bucketHash := bucket.Hash()
	fmt.Printf("  served bucket: %d entries, hash %s\n", count, bucketHash)
	for _, rec := range records {
		if rec.commitment != nil && bucketHash.Equal(rec.commitment) {
			fmt.Printf("  => bucket equals pruning point [%d] %s's HEADER commitment - the bucket is stale "+
				"by %d advancement(s)\n", rec.index, rec.hash, currentIndex-rec.index)
			return
		}
		if rec.stored != nil && bucketHash.Equal(rec.stored) {
			fmt.Printf("  => bucket equals pruning point [%d] %s's STORED multiset - stale by %d "+
				"advancement(s)\n", rec.index, rec.hash, currentIndex-rec.index)
			return
		}
	}
	fmt.Printf("  => bucket matches NO pruning point, past or present, by header or stored multiset\n")
}

type entryFingerprint struct {
	amount        uint64
	daaScore      uint64
	isCoinbase    bool
	scriptVersion uint16
	scriptSum     uint64
}

func fingerprint(entry externalapi.UTXOEntry) entryFingerprint {
	var sum uint64 = 1469598103934665603
	for _, b := range entry.ScriptPublicKey().Script {
		sum ^= uint64(b)
		sum *= 1099511628211
	}
	return entryFingerprint{
		amount: entry.Amount(), daaScore: entry.BlockDAAScore(), isCoinbase: entry.IsCoinbase(),
		scriptVersion: entry.ScriptPublicKey().Version, scriptSum: sum,
	}
}

// reconstructPruningPointSet rebuilds the pruning point's absolute UTXO set the way the node itself
// would for any block - virtual's own ground-truth UTXO table combined with the stored diff chain
// walked back from the pruning point - and checks it against the pruning point's header commitment.
// The served bucket is maintained by a completely different mechanism (UpdatePruningPointUTXOSet
// applying a per-advancement diff), so if the reconstruction matches the header and the bucket does
// not, the bucket is the thing that is wrong, and diffing the two says exactly how.
func reconstructPruningPointSet(s *stores, sa *model.StagingArea) {
	pp, err := s.pruning.PruningPoint(s.db, sa)
	if err != nil {
		fmt.Printf("pruning point: %v\n", err)
		return
	}
	ppHeader, err := s.headers.BlockHeader(s.db, sa, pp)
	if err != nil {
		fmt.Printf("pruning point header: %v\n", err)
		return
	}
	fmt.Printf("\n=== reconstructing the absolute UTXO set of pruning point %s\n", pp)

	// Walk the diff chain from the pruning point up to virtual, exactly like restorePastUTXO.
	var diffs []externalapi.UTXODiff
	next := pp
	for {
		diff, err := s.diffs.UTXODiff(s.db, sa, next)
		if err != nil {
			break
		}
		diffs = append(diffs, diff)
		next, err = s.diffs.UTXODiffChild(s.db, sa, next)
		if err != nil || next == nil {
			break
		}
	}
	fmt.Printf("  diff chain from pruning point to virtual: %d hops\n", len(diffs))

	accumulated := utxo.NewMutableUTXODiff()
	for i := len(diffs) - 1; i >= 0; i-- {
		if err := accumulated.WithDiffInPlace(diffs[i]); err != nil {
			fmt.Printf("  merging diff %d failed: %v\n", i, err)
			return
		}
	}
	fmt.Printf("  accumulated diff: toAdd=%d toRemove=%d\n",
		accumulated.ToAdd().Len(), accumulated.ToRemove().Len())

	virtualIterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer virtualIterator.Close()
	iterator, err := utxo.IteratorWithDiff(virtualIterator, accumulated.ToImmutable())
	if err != nil {
		fmt.Printf("  IteratorWithDiff: %v\n", err)
		return
	}
	defer iterator.Close()

	reconstructed := multiset.New()
	expected := make(map[externalapi.DomainOutpoint]entryFingerprint)
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Printf("  reconstruction iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			fmt.Printf("  SerializeUTXO: %v\n", err)
			return
		}
		reconstructed.Add(serialized)
		expected[*outpoint] = fingerprint(entry)
	}
	reconstructedHash := reconstructed.Hash()
	fmt.Printf("  reconstructed: %d entries, hash %s\n  header commitment: %s\n  => reconstruction %s the header\n",
		len(expected), reconstructedHash, ppHeader.UTXOCommitment(),
		map[bool]string{true: "MATCHES", false: "does NOT match"}[reconstructedHash.Equal(ppHeader.UTXOCommitment())])

	bucketIterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("  bucket iterator: %v\n", err)
		return
	}
	defer bucketIterator.Close()

	var extraInBucket, valueMismatch, bucketCount int
	var examplesExtra, examplesMismatch []string
	for ok := bucketIterator.First(); ok; ok = bucketIterator.Next() {
		outpoint, entry, err := bucketIterator.Get()
		if err != nil {
			fmt.Printf("  bucket iterator.Get: %v\n", err)
			return
		}
		bucketCount++
		want, ok2 := expected[*outpoint]
		if !ok2 {
			extraInBucket++
			if len(examplesExtra) < 8 {
				examplesExtra = append(examplesExtra, fmt.Sprintf("%s:%d amount=%d daa=%d coinbase=%t",
					&outpoint.TransactionID, outpoint.Index, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase()))
			}
			continue
		}
		got := fingerprint(entry)
		if got != want {
			valueMismatch++
			if len(examplesMismatch) < 8 {
				examplesMismatch = append(examplesMismatch, fmt.Sprintf(
					"%s:%d bucket{amount=%d daa=%d coinbase=%t} want{amount=%d daa=%d coinbase=%t}",
					&outpoint.TransactionID, outpoint.Index, got.amount, got.daaScore, got.isCoinbase,
					want.amount, want.daaScore, want.isCoinbase))
			}
		}
		delete(expected, *outpoint)
	}
	missingFromBucket := len(expected)

	fmt.Printf("  bucket entries: %d | missing from bucket: %d | extra in bucket: %d | value mismatches: %d\n",
		bucketCount, missingFromBucket, extraInBucket, valueMismatch)
	for _, e := range examplesExtra {
		fmt.Printf("    extra-in-bucket : %s\n", e)
	}
	for _, e := range examplesMismatch {
		fmt.Printf("    value-mismatch  : %s\n", e)
	}
	shown := 0
	var missingDAAMin, missingDAAMax uint64 = ^uint64(0), 0
	for outpoint, want := range expected {
		if want.daaScore < missingDAAMin {
			missingDAAMin = want.daaScore
		}
		if want.daaScore > missingDAAMax {
			missingDAAMax = want.daaScore
		}
		if shown < 8 {
			o := outpoint
			fmt.Printf("    missing-from-bucket: %s:%d amount=%d daa=%d coinbase=%t\n",
				&o.TransactionID, o.Index, want.amount, want.daaScore, want.isCoinbase)
			shown++
		}
	}
	if missingFromBucket > 0 {
		fmt.Printf("    missing entries span DAA scores %d..%d (pruning point DAA score is %d)\n",
			missingDAAMin, missingDAAMax, ppHeader.DAAScore())
	}
}

func openStores(db *pebble.DB, prefixFlag int) (*stores, error) {
	var prefixBytes []byte
	if prefixFlag >= 0 {
		if prefixFlag > 1 {
			return nil, errors.Errorf("prefix must be 0 or 1, got %d", prefixFlag)
		}
		prefixBytes = []byte{byte(prefixFlag)}
		fmt.Printf("using prefix override %d\n", prefixFlag)
	} else {
		activePrefix, exists, err := prefixmanager.ActivePrefix(db)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errors.New("no active database prefix - is this a pebble datadir2?")
		}
		prefixBytes = activePrefix.Serialize()
	}
	dbManager := consensusdatabase.New(db)
	pb := consensusdatabase.MakeBucket(prefixBytes)

	bs, err := blockstore.New(dbManager, pb, 100, false)
	if err != nil {
		return nil, err
	}
	bhs, err := blockheaderstore.New(dbManager, pb, 100, false)
	if err != nil {
		return nil, err
	}
	return &stores{
		db: dbManager, headers: bhs, blocks: bs,
		accept:     acceptancedatastore.New(pb, 100, false),
		ms:         multisetstore.New(pb, 100, false),
		gd:         ghostdagdatastore.New(pb.Bucket([]byte{0}), 100, false),
		daa:        daablocksstore.New(pb, 100, 100, false),
		chain:      headersselectedchainstore.New(pb, 100, false),
		pruning:    pruningstore.New(pb, 2, false),
		state:      consensusstatestore.New(pb, 100, false),
		diffs:      utxodiffstore.New(pb, 100, false),
		headersTip: headersselectedtipstore.New(pb),
	}, nil
}

// diffPruningPointSets compares, entry by entry, the pruning point UTXO sets two databases hold. Run
// against two nodes that fetched the same pruning point from different peers, it answers the question
// a hash comparison cannot: do peers serve the SAME (possibly wrong) set, or different ones - and if
// different, exactly which outpoints and whether they differ in membership or only in values such as
// BlockDAAScore.
func diffPruningPointSets(a, b *stores, sa *model.StagingArea) {
	ppA, errA := a.pruning.PruningPoint(a.db, sa)
	ppB, errB := b.pruning.PruningPoint(b.db, sa)
	if errA != nil || errB != nil {
		fmt.Printf("pruning points: %v / %v\n", errA, errB)
		return
	}
	fmt.Printf("\n=== pruning point UTXO set comparison\n  db  pruning point: %s (source: %s)\n"+
		"  db2 pruning point: %s (source: %s)\n", ppA, *srcA, ppB, *srcB)
	if !ppA.Equal(ppB) {
		fmt.Printf("  the two databases are at DIFFERENT pruning points - their sets are not comparable\n")
		return
	}
	if header, err := a.headers.BlockHeader(a.db, sa, ppA); err == nil {
		fmt.Printf("  header commitment: %s\n", header.UTXOCommitment())
	}

	setA := make(map[externalapi.DomainOutpoint]entryFingerprint)
	iterA, err := utxoSetIterator(a, *srcA)
	if err != nil {
		fmt.Printf("  db bucket iterator: %v\n", err)
		return
	}
	msA := multiset.New()
	for ok := iterA.First(); ok; ok = iterA.Next() {
		outpoint, entry, err := iterA.Get()
		if err != nil {
			fmt.Printf("  db bucket iterator.Get: %v\n", err)
			iterA.Close()
			return
		}
		setA[*outpoint] = fingerprint(entry)
		if serialized, err := utxo.SerializeUTXO(entry, outpoint); err == nil {
			msA.Add(serialized)
		}
	}
	iterA.Close()

	iterB, err := utxoSetIterator(b, *srcB)
	if err != nil {
		fmt.Printf("  db2 bucket iterator: %v\n", err)
		return
	}
	defer iterB.Close()
	msB := multiset.New()
	var countB, onlyInB, valueDiff, daaOnlyDiff int
	daaDelta := map[int64]int{}
	var examplesOnlyB, examplesValue []string
	for ok := iterB.First(); ok; ok = iterB.Next() {
		outpoint, entry, err := iterB.Get()
		if err != nil {
			fmt.Printf("  db2 bucket iterator.Get: %v\n", err)
			return
		}
		countB++
		if serialized, err := utxo.SerializeUTXO(entry, outpoint); err == nil {
			msB.Add(serialized)
		}
		want, ok2 := setA[*outpoint]
		if !ok2 {
			onlyInB++
			if len(examplesOnlyB) < 8 {
				examplesOnlyB = append(examplesOnlyB, fmt.Sprintf("%s:%d amount=%d daa=%d coinbase=%t",
					&outpoint.TransactionID, outpoint.Index, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase()))
			}
			continue
		}
		got := fingerprint(entry)
		if got != want {
			valueDiff++
			sameExceptDAA := got.amount == want.amount && got.isCoinbase == want.isCoinbase &&
				got.scriptVersion == want.scriptVersion && got.scriptSum == want.scriptSum
			if sameExceptDAA {
				daaOnlyDiff++
				daaDelta[int64(want.daaScore)-int64(got.daaScore)]++
			}
			if len(examplesValue) < 8 {
				examplesValue = append(examplesValue, fmt.Sprintf(
					"%s:%d db{amount=%d daa=%d} db2{amount=%d daa=%d} differsOnlyInDAAScore=%t",
					&outpoint.TransactionID, outpoint.Index, want.amount, want.daaScore,
					got.amount, got.daaScore, sameExceptDAA))
			}
		}
		delete(setA, *outpoint)
	}
	onlyInA := len(setA)

	fmt.Printf("  db  set hash: %s\n  db2 set hash: %s\n", msA.Hash(), msB.Hash())
	fmt.Printf("  db2 entries: %d | only in db: %d | only in db2: %d | value differences: %d (of which "+
		"BlockDAAScore-only: %d)\n", countB, onlyInA, onlyInB, valueDiff, daaOnlyDiff)
	for _, e := range examplesOnlyB {
		fmt.Printf("    only-in-db2: %s\n", e)
	}
	for _, e := range examplesValue {
		fmt.Printf("    value-diff : %s\n", e)
	}
	if len(daaDelta) > 0 {
		deltas := make([]int64, 0, len(daaDelta))
		for d := range daaDelta {
			deltas = append(deltas, d)
		}
		sort.Slice(deltas, func(i, j int) bool { return daaDelta[deltas[i]] > daaDelta[deltas[j]] })
		fmt.Printf("    BlockDAAScore delta (db minus db2) distribution over %d entries, %d distinct values:\n",
			daaOnlyDiff, len(daaDelta))
		for i, d := range deltas {
			if i >= 10 {
				fmt.Printf("      ... and %d more distinct deltas\n", len(deltas)-10)
				break
			}
			fmt.Printf("      %+d : %d entries (%.2f%%)\n", d, daaDelta[d],
				100*float64(daaDelta[d])/float64(daaOnlyDiff))
		}
	}
	shown := 0
	for outpoint, want := range setA {
		if shown >= 8 {
			break
		}
		o := outpoint
		fmt.Printf("    only-in-db : %s:%d amount=%d daa=%d coinbase=%t\n",
			&o.TransactionID, o.Index, want.amount, want.daaScore, want.isCoinbase)
		shown++
	}
}

// utxoSetIterator picks between the served pruning point bucket and the raw imported set - the one a
// peer actually sent, which an IBD keeps under the staging prefix until it is committed and cleared.
// Comparing one node's imported set against another's lets two peers' answers for the same pruning
// point be diffed directly.
func utxoSetIterator(s *stores, src string) (externalapi.ReadOnlyUTXOSetIterator, error) {
	switch src {
	case "bucket":
		return s.pruning.PruningPointUTXOIterator(s.db)
	case "imported":
		return s.pruning.ImportedPruningPointUTXOIterator(s.db)
	default:
		return nil, errors.Errorf("unknown UTXO set source %q (want \"bucket\" or \"imported\")", src)
	}
}

// virtualTableCheck compares virtual's materialised UTXO table (consensusStateStore - the real
// key/value table that RPC balances and the served pruning point set are both built from) against
// virtual's own stored multiset (multisetStore, maintained incrementally from acceptance data, and
// the value a miner puts in a block header via newBlockUTXOCommitment).
//
// They are two representations of one UTXO set kept by entirely separate code. When a node's
// per-block multiset chain reproduces mainnet header commitments but its materialised sets do not,
// this is the check that says so directly, without needing a correct reference set from anywhere.
func virtualTableCheck(s *stores, sa *model.StagingArea) {
	fmt.Printf("\n=== virtual UTXO table vs virtual's stored multiset\n")

	storedMultiset, err := s.ms.Get(s.db, sa, model.VirtualBlockHash)
	if err != nil {
		fmt.Printf("  virtual has no stored multiset (%v)\n", err)
		return
	}

	iterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	table := multiset.New()
	count := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Printf("  virtual iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			fmt.Printf("  SerializeUTXO: %v\n", err)
			return
		}
		table.Add(serialized)
		count++
	}

	tableHash := table.Hash()
	storedHash := storedMultiset.Hash()
	fmt.Printf("  virtual UTXO table : %d entries, hash %s\n  virtual stored multiset: %s\n  => %s\n",
		count, tableHash, storedHash,
		map[bool]string{
			true: "AGREE - the materialised table and the incremental multiset are the same set; any " +
				"disagreement with the network is in what was accepted, not in the bookkeeping",
			false: "DISAGREE - virtual's materialised UTXO table has drifted from the multiset the node " +
				"itself commits to. RPC balances are served from the table; block templates commit to " +
				"the multiset. This is a local bug, independent of anything a peer supplied.",
		}[tableHash.Equal(storedHash)])
}

// replayForwardCheck rebuilds the UTXO set implied by this node's own acceptance data - the same
// data the per-block multiset chain is computed from, and the representation proven to reproduce
// mainnet header commitments - and diffs it entry-by-entry against virtual's materialised UTXO
// table, which is maintained separately by applying virtual UTXO diffs.
//
// Both start from the pruning point bucket, so whatever is wrong with that bucket cancels out. What
// is left is exactly how far the materialised table has drifted from the acceptance history, which
// is what decides whether the drift is a handful of repairable entries or wholesale divergence.
func replayForwardCheck(s *stores, sa *model.StagingArea) {
	pp, err := s.pruning.PruningPoint(s.db, sa)
	if err != nil {
		fmt.Printf("pruning point: %v\n", err)
		return
	}
	virtualGHOSTDAG, err := s.gd.Get(s.db, sa, model.VirtualBlockHash, false)
	if err != nil {
		fmt.Printf("virtual ghostdag data: %v\n", err)
		return
	}
	tip := virtualGHOSTDAG.SelectedParent()
	fmt.Printf("\n=== acceptance-data replay vs virtual's materialised UTXO table\n"+
		"  pruning point: %s\n  selected tip : %s\n", pp, tip)

	// Collect the selected chain from the tip down to the pruning point, then apply it forwards.
	var chain []*externalapi.DomainHash
	for current := tip; !current.Equal(pp); {
		chain = append(chain, current)
		gd, err := s.gd.Get(s.db, sa, current, false)
		if err != nil {
			fmt.Printf("  ghostdag data for %s: %v\n", current, err)
			return
		}
		current = gd.SelectedParent()
		if current == nil {
			fmt.Printf("  walked off the end of the selected chain without reaching the pruning point\n")
			return
		}
		if len(chain) > 5_000_000 {
			fmt.Printf("  selected chain walk did not terminate\n")
			return
		}
	}
	fmt.Printf("  chain blocks from pruning point to tip: %d\n", len(chain))

	set := make(map[externalapi.DomainOutpoint]entryFingerprint)
	bucketIterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("  bucket iterator: %v\n", err)
		return
	}
	for ok := bucketIterator.First(); ok; ok = bucketIterator.Next() {
		outpoint, entry, err := bucketIterator.Get()
		if err != nil {
			fmt.Printf("  bucket iterator.Get: %v\n", err)
			bucketIterator.Close()
			return
		}
		set[*outpoint] = fingerprint(entry)
	}
	bucketIterator.Close()
	fmt.Printf("  starting from the pruning point bucket: %d entries\n", len(set))

	apply := func(blockHash *externalapi.DomainHash, mergingDAAScore uint64) bool {
		acceptanceData, err := s.accept.Get(s.db, sa, blockHash)
		if err != nil {
			fmt.Printf("  no acceptance data for %s: %v\n", blockHash, err)
			return false
		}
		for _, bad := range acceptanceData {
			for i, tad := range bad.TransactionAcceptanceData {
				if !tad.IsAccepted {
					continue
				}
				transaction := tad.Transaction
				transactionID := consensushashing.TransactionID(transaction)
				for _, input := range transaction.Inputs {
					delete(set, input.PreviousOutpoint)
				}
				for outIdx, output := range transaction.Outputs {
					outpoint := externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(outIdx)}
					set[outpoint] = fingerprint(utxo.NewUTXOEntry(
						output.Value, output.ScriptPublicKey, i == 0, mergingDAAScore))
				}
			}
		}
		return true
	}

	for i := len(chain) - 1; i >= 0; i-- {
		blockHash := chain[i]
		daaScore, err := s.ownDAAScore(sa, blockHash)
		if err != nil {
			fmt.Printf("  no DAA score for %s: %v\n", blockHash, err)
			return
		}
		if !apply(blockHash, daaScore) {
			return
		}
	}
	fmt.Printf("  after replaying the chain to the tip: %d entries\n", len(set))

	// Virtual's own merge set sits on top of the tip.
	virtualDAAScore, err := s.daa.DAAScore(s.db, sa, model.VirtualBlockHash)
	if err != nil {
		fmt.Printf("  virtual DAA score: %v\n", err)
		return
	}
	if !apply(model.VirtualBlockHash, virtualDAAScore) {
		return
	}
	fmt.Printf("  after applying virtual's own acceptance data (daaScore %d): %d entries\n",
		virtualDAAScore, len(set))

	tableIterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO table iterator: %v\n", err)
		return
	}
	defer tableIterator.Close()

	var tableCount, onlyInTable, valueDiff, daaOnlyDiff int
	var tableOnlyEntries []outpointAndEntry
	daaDelta := map[int64]int{}
	var examplesTable, examplesValue []string
	for ok := tableIterator.First(); ok; ok = tableIterator.Next() {
		outpoint, entry, err := tableIterator.Get()
		if err != nil {
			fmt.Printf("  virtual table iterator.Get: %v\n", err)
			return
		}
		tableCount++
		want, ok2 := set[*outpoint]
		if !ok2 {
			onlyInTable++
			if len(tableOnlyEntries) < 64 {
				tableOnlyEntries = append(tableOnlyEntries, outpointAndEntry{*outpoint, entry})
			}
			if len(examplesTable) < 6 {
				examplesTable = append(examplesTable, fmt.Sprintf("%s:%d amount=%d daa=%d coinbase=%t",
					&outpoint.TransactionID, outpoint.Index, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase()))
			}
			continue
		}
		got := fingerprint(entry)
		if got != want {
			valueDiff++
			if got.amount == want.amount && got.isCoinbase == want.isCoinbase &&
				got.scriptVersion == want.scriptVersion && got.scriptSum == want.scriptSum {
				daaOnlyDiff++
				daaDelta[int64(got.daaScore)-int64(want.daaScore)]++
			}
			if len(examplesValue) < 6 {
				examplesValue = append(examplesValue, fmt.Sprintf(
					"%s:%d table{amount=%d daa=%d} replay{amount=%d daa=%d}",
					&outpoint.TransactionID, outpoint.Index, got.amount, got.daaScore, want.amount, want.daaScore))
			}
		}
		delete(set, *outpoint)
	}
	onlyInReplay := len(set)

	fmt.Printf("  virtual table: %d entries | only in table: %d | only in replay: %d | value differences: %d "+
		"(BlockDAAScore-only: %d)\n", tableCount, onlyInTable, onlyInReplay, valueDiff, daaOnlyDiff)
	for _, e := range examplesTable {
		fmt.Printf("    only-in-table : %s\n", e)
	}
	for _, e := range examplesValue {
		fmt.Printf("    value-diff    : %s\n", e)
	}
	shown := 0
	for outpoint, want := range set {
		if shown >= 6 {
			break
		}
		o := outpoint
		fmt.Printf("    only-in-replay: %s:%d amount=%d daa=%d coinbase=%t\n",
			&o.TransactionID, o.Index, want.amount, want.daaScore, want.isCoinbase)
		shown++
	}
	if len(daaDelta) > 0 {
		deltas := make([]int64, 0, len(daaDelta))
		for d := range daaDelta {
			deltas = append(deltas, d)
		}
		sort.Slice(deltas, func(i, j int) bool { return daaDelta[deltas[i]] > daaDelta[deltas[j]] })
		fmt.Printf("    BlockDAAScore delta (table minus replay), %d distinct values:\n", len(deltas))
		for i, d := range deltas {
			if i >= 6 {
				break
			}
			fmt.Printf("      %+d : %d entries\n", d, daaDelta[d])
		}
	}

	if *recover_ {
		replayOnly := make([]externalapi.DomainOutpoint, 0, len(set))
		for outpoint := range set {
			replayOnly = append(replayOnly, outpoint)
		}
		tryRecoverPruningPointSet(s, sa, pp, tableOnlyEntries, replayOnly)
	}
}

type outpointAndEntry struct {
	outpoint externalapi.DomainOutpoint
	entry    externalapi.UTXOEntry
}

// tryRecoverPruningPointSet asks whether the UTXO set the network actually committed to is within
// reach of the set this node holds. The materialised table and the acceptance replay disagree on a
// handful of outpoints; the true set is plausibly the served bucket with some subset of those
// disagreements corrected. Every subset is applied to the bucket's multiset and hashed against the
// pruning point's header commitment. A hit means the correct set is recoverable locally, with no
// peer involved and no resync from genesis.
func tryRecoverPruningPointSet(s *stores, sa *model.StagingArea, pp *externalapi.DomainHash,
	tableOnly []outpointAndEntry, replayOnly []externalapi.DomainOutpoint) {
	header, err := s.headers.BlockHeader(s.db, sa, pp)
	if err != nil {
		fmt.Printf("  recover: pruning point header: %v\n", err)
		return
	}
	target := header.UTXOCommitment()

	wanted := make(map[externalapi.DomainOutpoint]bool, len(replayOnly))
	for _, o := range replayOnly {
		wanted[o] = true
	}
	replayEntries := make([]outpointAndEntry, 0, len(replayOnly))

	bucketIterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Printf("  recover: bucket iterator: %v\n", err)
		return
	}
	base := multiset.New()
	for ok := bucketIterator.First(); ok; ok = bucketIterator.Next() {
		outpoint, entry, err := bucketIterator.Get()
		if err != nil {
			bucketIterator.Close()
			fmt.Printf("  recover: bucket iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			bucketIterator.Close()
			fmt.Printf("  recover: SerializeUTXO: %v\n", err)
			return
		}
		base.Add(serialized)
		if wanted[*outpoint] {
			replayEntries = append(replayEntries, outpointAndEntry{*outpoint, entry})
		}
	}
	bucketIterator.Close()

	// Each candidate is one correction: remove an entry the table has and the replay does not, or
	// add back one the replay has and the table does not.
	type correction struct {
		serialized []byte
		add        bool
		label      string
	}
	var candidates []correction
	for _, oe := range tableOnly {
		serialized, err := utxo.SerializeUTXO(oe.entry, &oe.outpoint)
		if err != nil {
			continue
		}
		candidates = append(candidates, correction{serialized, true,
			fmt.Sprintf("+%s:%d(daa=%d)", &oe.outpoint.TransactionID, oe.outpoint.Index, oe.entry.BlockDAAScore())})
	}
	for _, oe := range replayEntries {
		serialized, err := utxo.SerializeUTXO(oe.entry, &oe.outpoint)
		if err != nil {
			continue
		}
		candidates = append(candidates, correction{serialized, false,
			fmt.Sprintf("-%s:%d(daa=%d)", &oe.outpoint.TransactionID, oe.outpoint.Index, oe.entry.BlockDAAScore())})
	}

	if len(candidates) == 0 || len(candidates) > 20 {
		fmt.Printf("  recover: %d candidate corrections - %s\n", len(candidates),
			map[bool]string{true: "nothing to try", false: "too many to enumerate exhaustively"}[len(candidates) == 0])
		return
	}

	fmt.Printf("  recover: bucket hashes to %s, header wants %s, trying all %d combinations of %d "+
		"candidate corrections\n", base.Hash(), target, 1<<len(candidates), len(candidates))
	for mask := 0; mask < 1<<len(candidates); mask++ {
		trial := base.Clone()
		var applied []string
		for i, c := range candidates {
			if mask&(1<<i) == 0 {
				continue
			}
			if c.add {
				trial.Add(c.serialized)
			} else {
				trial.Remove(c.serialized)
			}
			applied = append(applied, c.label)
		}
		if trial.Hash().Equal(target) {
			fmt.Printf("  recover: MATCH - the pruning point's committed UTXO set is the served bucket with "+
				"these %d corrections applied: %v\n", len(applied), applied)
			return
		}
	}
	fmt.Printf("  recover: no combination of these corrections reproduces the header commitment - the true " +
		"set differs from this node's by more than the disagreements found here\n")
}

// lookUpOutpoints reports which of the given outpoints the pruning point UTXO set holds.
//
// It exists to compare two nodes' gaps when they are not at the same pruning point, which -diffsets
// requires and which peers rarely oblige. The coins one node reported missing are looked up in
// another node's served set: if that set holds them, the two nodes are missing different coins and no
// single repaired set is correct for both; if neither holds them, the gap is shared and one bundle
// serves everyone. That distinction decides whether a rebaseline is a network-wide fix or a per-node
// one, and nothing else measured so far reaches it.
//
// The bucket has no point-lookup API, so this walks it once and answers every outpoint in one pass.
func lookUpOutpoints(s *stores, path string) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		return
	}
	defer file.Close()

	wanted := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			wanted[line] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		return
	}
	fmt.Printf("\n=== looking up %d outpoints in this node's pruning point UTXO set\n", len(wanted))

	iterator, err := s.pruning.PruningPointUTXOIterator(s.db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pruning point UTXO iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	found := map[string]struct{}{}
	scanned := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, _, err := iterator.Get()
		if err != nil {
			fmt.Fprintf(os.Stderr, "iterate: %v\n", err)
			return
		}
		scanned++
		key := fmt.Sprintf("%s:%d", outpoint.TransactionID, outpoint.Index)
		if _, want := wanted[key]; want {
			found[key] = struct{}{}
			if len(found) == len(wanted) {
				break
			}
		}
	}

	fmt.Printf("  scanned %d entries\n", scanned)
	fmt.Printf("  PRESENT in this set: %d\n", len(found))
	fmt.Printf("  ABSENT from this set: %d\n", len(wanted)-len(found))
	switch {
	case len(found) == 0:
		fmt.Println("  => this node is missing them too: the gap is shared, and one repaired set serves both")
	case len(found) == len(wanted):
		fmt.Println("  => this node holds every one of them: the two nodes are missing DIFFERENT coins, so a " +
			"bundle built from one is not correct for the other")
	default:
		fmt.Println("  => partly shared: some coins are missing from both sets and some from only one, so a " +
			"bundle repairs part of another node's gap and not the rest")
	}
	shown := 0
	for outpoint := range wanted {
		if _, ok := found[outpoint]; ok {
			continue
		}
		if shown++; shown > 10 {
			break
		}
		fmt.Printf("    absent here too: %s\n", outpoint)
	}
}

// stampConsistencyCheck compares what virtual holds against what this node's own acceptance data
// says it should hold.
//
// Every coin an accepted transaction creates is stamped with the DAA score of the chain block that
// merged it - utxo.AcceptedUTXOBlockDAAScore is the identity function on that score. So for any
// unspent coin, virtual's BlockDAAScore must equal the DAA score of the chain block whose acceptance
// data accepted its creating transaction. There is no reorg, branch or timing story that makes a
// difference legitimate: both values come out of one node's own database.
//
// It matters because BlockDAAScore is part of the SerializeUTXO preimage. A coin carrying the wrong
// score hashes to something no other node will reproduce, so the node's UTXO commitment is
// permanently wrong by that coin's contribution - and lookups keyed on (outpoint, DAA score), which
// is how mutableUTXODiff.removeEntry matches a spend, stop finding it.
func stampConsistencyCheck(s *stores, sa *model.StagingArea, depth int) {
	fmt.Printf("\n=== stamp consistency: virtual's BlockDAAScore vs this node's own acceptance data\n")

	tipHash, err := s.headersTip.HeadersSelectedTip(s.db, sa)
	if err != nil {
		fmt.Printf("  headers selected tip: %v\n", err)
		return
	}
	tip, err := s.chain.GetIndexByHash(s.db, sa, tipHash)
	if err != nil {
		fmt.Printf("  tip index: %v\n", err)
		return
	}

	scanned, checked, matching, mismatched, notInVirtual := 0, 0, 0, 0, 0
	shown := 0
	for i := tip; i > 0 && tip-i < uint64(depth); i-- {
		chainBlock, err := s.chain.GetHashByIndex(s.db, sa, i)
		if err != nil {
			continue
		}
		acceptanceData, err := s.accept.Get(s.db, sa, chainBlock)
		if err != nil {
			continue
		}
		header, err := s.headers.BlockHeader(s.db, sa, chainBlock)
		if err != nil {
			continue
		}
		scanned++
		expected := utxo.AcceptedUTXOBlockDAAScore(header.DAAScore())

		for _, blockAcceptanceData := range acceptanceData {
			for _, tad := range blockAcceptanceData.TransactionAcceptanceData {
				if !tad.IsAccepted {
					continue
				}
				transactionID := consensushashing.TransactionID(tad.Transaction)
				for outputIndex := range tad.Transaction.Outputs {
					outpoint := externalapi.NewDomainOutpoint(transactionID, uint32(outputIndex))
					entry, ok, err := s.state.UTXOByOutpoint(s.db, sa, outpoint)
					if err != nil || !ok {
						// Spent since, or never reached virtual. Says nothing about the stamp.
						notInVirtual++
						continue
					}
					checked++
					if entry.BlockDAAScore() == expected {
						matching++
						continue
					}
					mismatched++
					if shown < 10 {
						shown++
						fmt.Printf("  %s:%d accepted by chain block %s (DAA %d), so its stamp must be "+
							"%d - virtual holds %d (delta %d)\n", transactionID, outputIndex, chainBlock,
							header.DAAScore(), expected, entry.BlockDAAScore(),
							absDelta(expected, entry.BlockDAAScore()))
					}
				}
			}
		}
	}

	fmt.Printf("  chain blocks with acceptance data: %d\n", scanned)
	fmt.Printf("  unspent created coins checked     : %d\n", checked)
	fmt.Printf("    stamp agrees with acceptance    : %d\n", matching)
	fmt.Printf("    stamp CONTRADICTS acceptance    : %d\n", mismatched)
	fmt.Printf("  outputs not in virtual (skipped)  : %d\n", notInVirtual)
	if mismatched > 0 {
		fmt.Printf("  Each of these coins hashes to a preimage no correct node reproduces, so this\n")
		fmt.Printf("  node's UTXO commitment cannot match however complete its coin set is.\n")
	}
}

func absDelta(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// utxoIndexConsistencyCheck compares the utxoindex against the consensus UTXO set in the same
// database.
//
// The index is derived from consensus and is what GetUtxosByAddresses answers from, so it is what
// wallets and explorers see. It has no independent authority: any disagreement is the index being
// wrong about its own node's state. On a node carrying the diff-composition bug fixed in 6df1cdaa4,
// one address alone showed 4,242 entries with the wrong BlockDAAScore and 21,443 entries the
// consensus set did not hold at all - the latter being coins a wallet would offer to spend and a
// balance would count, that do not exist.
//
// It reads the index bucket directly rather than over RPC, so it needs no running node and is not
// capped by the RPC result limit.
func utxoIndexConsistencyCheck(db *pebble.DB, s *stores, sa *model.StagingArea) {
	fmt.Printf("\n=== utxoindex consistency: the index against the consensus UTXO set\n")

	cursor, err := db.Cursor(infradatabase.MakeBucket([]byte("utxo-index")))
	if err != nil {
		fmt.Printf("  utxo-index cursor: %v (is this node running with --utxoindex?)\n", err)
		return
	}
	defer cursor.Close()

	entries, agree, daaDiff, amountDiff, notInConsensus, unreadable := 0, 0, 0, 0, 0, 0
	shown := 0
	for cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			unreadable++
			continue
		}
		outpoint, ok := outpointFromIndexKey(key.Suffix())
		if !ok {
			unreadable++
			continue
		}
		value, err := cursor.Value()
		if err != nil {
			unreadable++
			continue
		}
		dbEntry := &serialization.DbUtxoEntry{}
		if err := dbEntry.UnmarshalVT(value); err != nil {
			unreadable++
			continue
		}
		indexEntry, err := serialization.DBUTXOEntryToUTXOEntry(dbEntry)
		if err != nil {
			unreadable++
			continue
		}
		entries++

		consensusEntry, ok, err := s.state.UTXOByOutpoint(s.db, sa, outpoint)
		if err != nil || !ok {
			notInConsensus++
			if shown < 10 {
				shown++
				fmt.Printf("  %s:%d is in the index but NOT in the consensus set "+
					"(amount=%d daaScore=%d)\n", &outpoint.TransactionID, outpoint.Index,
					indexEntry.Amount(), indexEntry.BlockDAAScore())
			}
			continue
		}
		sameAmount := indexEntry.Amount() == consensusEntry.Amount()
		sameDAA := indexEntry.BlockDAAScore() == consensusEntry.BlockDAAScore()
		if sameAmount && sameDAA {
			agree++
			continue
		}
		if !sameAmount {
			amountDiff++
		}
		if !sameDAA {
			daaDiff++
		}
		if shown < 10 {
			shown++
			fmt.Printf("  %s:%d index says amount=%d daaScore=%d, consensus says amount=%d daaScore=%d\n",
				&outpoint.TransactionID, outpoint.Index, indexEntry.Amount(), indexEntry.BlockDAAScore(),
				consensusEntry.Amount(), consensusEntry.BlockDAAScore())
		}
	}

	fmt.Printf("  index entries read                        : %d\n", entries)
	fmt.Printf("    agree with consensus on amount and stamp: %d\n", agree)
	fmt.Printf("    differing BlockDAAScore                 : %d\n", daaDiff)
	fmt.Printf("    differing amount                        : %d\n", amountDiff)
	fmt.Printf("    held by the index, absent from consensus: %d\n", notInConsensus)
	if unreadable > 0 {
		fmt.Printf("  entries that could not be decoded         : %d\n", unreadable)
	}
	if notInConsensus > 0 {
		fmt.Printf("  Entries absent from consensus are coins a wallet would offer to spend and a\n")
		fmt.Printf("  balance would count, which do not exist.\n")
	}
}

// outpointFromIndexKey recovers the outpoint from a utxoindex key.
//
// The index stores entries at utxo-index/<scriptPublicKeyBytes>/<serializedOutpoint>, and a cursor
// opened on the parent bucket hands back everything after "utxo-index/" as the suffix. The script
// bytes cannot simply be split off: a script is arbitrary bytes and may itself contain the '/'
// separator, so splitting on it lands in the middle of a script often enough to matter.
//
// The outpoint is a protobuf DbOutpoint - a 32-byte hash plus a varint index - so its encoding is a
// short, bounded tail. Trying each plausible tail length and keeping the one that both decodes and
// is preceded by a separator identifies it without needing to understand the script at all.
func outpointFromIndexKey(suffix []byte) (*externalapi.DomainOutpoint, bool) {
	const minTail, maxTail = 34, 48
	for tail := minTail; tail <= maxTail && tail <= len(suffix); tail++ {
		start := len(suffix) - tail
		if start == 0 || suffix[start-1] != '/' {
			continue
		}
		dbOutpoint := &serialization.DbOutpoint{}
		if err := dbOutpoint.UnmarshalVT(suffix[start:]); err != nil {
			continue
		}
		outpoint, err := serialization.DbOutpointToDomainOutpoint(dbOutpoint)
		if err != nil {
			continue
		}
		return outpoint, true
	}
	return nil, false
}

// balanceComparison compares this node's per-address balances against a reference snapshot taken at
// a known point in time.
//
// The question it answers is "how far has this datadir drifted", which is what picking a datadir to
// rebaseline from comes down to. Drift shows up in two directions and they mean different things:
// an address holding LESS than the reference has had coins spent (ordinary) or lost (a gap in this
// node's set), while an address holding MORE has received coins (ordinary) or gained coins that do
// not exist (inflation from duplicated or mis-stamped entries).
//
// Neither direction is damning on its own, and the totals are not either: coinbase emission adds
// supply continuously, so every node's total is expected to exceed a past snapshot. The number that
// carries information is the comparison BETWEEN nodes at a similar DAA score. Two nodes that agree
// with the network agree with each other; a node carrying phantom coins reports more.
//
// Balances are summed from the consensus UTXO set rather than the utxoindex on purpose. The index is
// derived and has already been found wrong in exactly this dimension - 130,857 entries with a
// BlockDAAScore consensus disagreed with - so measuring drift with it would let an index bug flatter
// or condemn a datadir that is fine.
func balanceComparison(s *stores, sa *model.StagingArea, csvPath string) {
	fmt.Printf("\n=== per-address balances against %s\n", csvPath)

	reference, referenceTotal, err := readReferenceBalances(csvPath)
	if err != nil {
		fmt.Printf("  reading the reference: %v\n", err)
		return
	}
	fmt.Printf("  reference: %d addresses, %d sompi\n", len(reference), referenceTotal)

	iterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	current := make(map[string]uint64, len(reference))
	var currentTotal, unreadable uint64
	entries := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		_, entry, err := iterator.Get()
		if err != nil {
			unreadable++
			continue
		}
		entries++
		currentTotal += entry.Amount()
		_, address, err := txscript.ExtractScriptPubKeyAddress(entry.ScriptPublicKey(), &dagconfig.MainnetParams)
		if err != nil {
			// A script nobody can spend from, or one this build cannot parse. It still counts toward
			// the total - the coins exist - but it belongs to no address.
			unreadable++
			continue
		}
		current[address.EncodeAddress()] += entry.Amount()
	}

	var grew, shrank, unchanged, absent int
	var grewBy, shrankBy, absentBy uint64
	type mover struct {
		address            string
		reference, now, by uint64
	}
	var growers []mover
	for address, referenceBalance := range reference {
		now, held := current[address]
		switch {
		case !held:
			absent++
			absentBy += referenceBalance
		case now == referenceBalance:
			unchanged++
		case now > referenceBalance:
			grew++
			by := now - referenceBalance
			grewBy += by
			growers = append(growers, mover{address, referenceBalance, now, by})
		default:
			shrank++
			shrankBy += referenceBalance - now
		}
	}

	var newAddresses int
	var newBalance uint64
	for address, balance := range current {
		if _, inReference := reference[address]; !inReference {
			newAddresses++
			newBalance += balance
			// An address the reference never mentions held nothing as far as the reference is
			// concerned, so its whole balance is a gain. Ranked alongside the rest rather than
			// listed apart, because "which addresses account for the growth" does not care whether
			// the snapshot happened to know about them.
			growers = append(growers, mover{address, 0, balance, balance})
		}
	}

	sort.Slice(growers, func(i, j int) bool { return growers[i].by > growers[j].by })

	fmt.Printf("  this node: %d UTXO entries, %d addresses, %d sompi\n", entries, len(current), currentTotal)
	if unreadable > 0 {
		fmt.Printf("    entries whose address could not be derived: %d (counted in the total)\n", unreadable)
	}
	fmt.Printf("\n  addresses present in the reference: %d\n", len(reference))
	fmt.Printf("    unchanged                       : %d\n", unchanged)
	fmt.Printf("    hold more than the reference    : %d  (+%d sompi)\n", grew, grewBy)
	fmt.Printf("    hold less than the reference    : %d  (-%d sompi)\n", shrank, shrankBy)
	fmt.Printf("    hold nothing at all now         : %d  (-%d sompi)\n", absent, absentBy)
	fmt.Printf("  addresses absent from the reference: %d  (+%d sompi)\n", newAddresses, newBalance)

	fmt.Printf("\n  reference total : %d sompi\n", referenceTotal)
	fmt.Printf("  this node total : %d sompi\n", currentTotal)
	if currentTotal >= referenceTotal {
		growth := currentTotal - referenceTotal
		fmt.Printf("  growth          : +%d sompi (+%.4f%%)\n", growth,
			100*float64(growth)/float64(referenceTotal))
	} else {
		fmt.Printf("  shrinkage       : -%d sompi\n", referenceTotal-currentTotal)
	}

	reportExpectedEmission(s, sa, referenceTotal, currentTotal, newBalance)

	if len(growers) > 0 {
		var totalGain uint64
		for _, m := range growers {
			totalGain += m.by
		}
		var netGrowth uint64
		if currentTotal > referenceTotal {
			netGrowth = currentTotal - referenceTotal
		}

		shown := len(growers)
		if shown > 25 {
			shown = 25
		}
		fmt.Printf("\n  largest gainers, counting addresses the reference never mentioned as having\n")
		fmt.Printf("  started from nothing. %d addresses gained %d sompi between them", len(growers), totalGain)
		if netGrowth > 0 {
			fmt.Printf("; net growth is\n  %d, the difference being what other addresses lost", netGrowth)
		}
		fmt.Printf(".\n")
		var running uint64
		for i, m := range growers[:shown] {
			running += m.by
			share := ""
			if netGrowth > 0 {
				share = fmt.Sprintf("  [%.1f%% of net growth, %.1f%% cumulative]",
					100*float64(m.by)/float64(netGrowth), 100*float64(running)/float64(netGrowth))
			}
			origin := fmt.Sprintf("reference %d", m.reference)
			if m.reference == 0 {
				origin = "NOT IN THE REFERENCE"
			}
			fmt.Printf("    %2d. %s\n        %s -> now %d  (+%d)%s\n",
				i+1, m.address, origin, m.now, m.by, share)
		}
		if len(growers) > shown {
			fmt.Printf("    ... and %d more gaining addresses\n", len(growers)-shown)
		}
	}
}

// readReferenceBalances reads a two-column CSV of address and balance in sompi. A header row is
// tolerated and skipped; a malformed row is an error rather than a silently dropped address,
// because a snapshot read with holes in it would understate drift.
func readReferenceBalances(path string) (map[string]uint64, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	balances := make(map[string]uint64)
	var total uint64
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		address, amount, found := strings.Cut(text, ",")
		if !found {
			return nil, 0, errors.Errorf("line %d: expected \"address,balance\", got %q", line, text)
		}
		address = strings.TrimSpace(address)
		balance, err := strconv.ParseUint(strings.TrimSpace(amount), 10, 64)
		if err != nil {
			if line == 1 {
				continue // header
			}
			return nil, 0, errors.Errorf("line %d: balance %q is not a number", line, amount)
		}
		balances[address] += balance
		total += balance
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return balances, total, nil
}

// reportExpectedEmission works out how much supply the network should have emitted since the
// reference snapshot was taken, so growth can be judged against expectation instead of only against
// other nodes.
//
// Emission is a pure function of DAA score: every block pays coinbasemanager.BlockSubsidy for its
// own score, and DAA score advances about one per block. So the supply added between two scores is
// the sum of the subsidy over that range - which is computed here from the same function consensus
// uses, not a copy of the schedule.
//
// Two honest limits on the result, both stated in the output rather than buried here. The reference
// DAA score is estimated from the snapshot's timestamp and the target block rate unless the operator
// supplies it, and a network running above or below its target rate moves that estimate. And the
// whole range is priced at the CURRENT block version's target time per block; a range spanning a
// version change that altered the block rate is priced at today's rate throughout.
//
// What the comparison is for: a node whose actual growth materially EXCEEDS expected emission holds
// coins that were never emitted, which is inflation from duplicated or mis-stamped entries. One that
// falls materially short has lost coins the network has. Both are reasons to prefer a different
// datadir to rebaseline from.
func reportExpectedEmission(s *stores, sa *model.StagingArea, referenceTotal, currentTotal,
	heldByAddressesAbsentFromReference uint64,
) {
	tipHash, err := s.headersTip.HeadersSelectedTip(s.db, sa)
	if err != nil {
		fmt.Printf("\n  expected emission: cannot read the selected tip (%v)\n", err)
		return
	}
	tipHeader, err := s.headers.BlockHeader(s.db, sa, tipHash)
	if err != nil {
		fmt.Printf("\n  expected emission: cannot read the tip header (%v)\n", err)
		return
	}
	currentDAAScore := tipHeader.DAAScore()
	blockVersion := tipHeader.Version()
	params := &dagconfig.MainnetParams
	targetSecondsPerBlock := params.TargetTimePerBlock[currentBlockVersionIndex(len(params.TargetTimePerBlock),
		blockVersion)].Seconds()

	estimated := false
	referenceScore := *referenceDAAScore
	if referenceScore == 0 {
		estimated = true
		elapsedSeconds := float64(tipHeader.TimeInMilliseconds()-
			constants.ReferenceSupplyTimeUnixMilliseconds) / 1000
		if elapsedSeconds <= 0 || targetSecondsPerBlock <= 0 {
			fmt.Printf("\n  expected emission: the tip predates the reference snapshot; nothing to compare\n")
			return
		}
		blocksSince := uint64(elapsedSeconds / targetSecondsPerBlock)
		if blocksSince >= currentDAAScore {
			fmt.Printf("\n  expected emission: the estimated block count exceeds the tip's DAA score; " +
				"pass -referencedaascore\n")
			return
		}
		referenceScore = currentDAAScore - blocksSince
	}

	var expected uint64
	for score := referenceScore + 1; score <= currentDAAScore; score++ {
		expected += coinbasemanager.BlockSubsidy(params, score, blockVersion)
	}

	fmt.Printf("\n  reference DAA score : %d%s\n", referenceScore,
		map[bool]string{true: "  (estimated from the snapshot timestamp and the target block rate)",
			false: "  (supplied)"}[estimated])
	fmt.Printf("  tip DAA score       : %d  (block version %d, %.3fs target per block)\n",
		currentDAAScore, blockVersion, targetSecondsPerBlock)
	fmt.Printf("  blocks in between   : %d\n", currentDAAScore-referenceScore)
	fmt.Printf("  expected emission   : %d sompi\n", expected)

	if currentTotal < referenceTotal {
		fmt.Printf("  actual growth       : NEGATIVE (%d sompi below the reference)\n",
			referenceTotal-currentTotal)
		fmt.Printf("  This node holds less than the snapshot did AND should have gained %d. It is\n", expected)
		fmt.Printf("  missing coins the network has.\n")
		return
	}
	actual := currentTotal - referenceTotal
	fmt.Printf("  actual growth       : %d sompi\n", actual)
	switch {
	case actual > expected:
		excess := actual - expected
		fmt.Printf("  EXCESS              : +%d sompi (%.4f%% of expected)\n", excess,
			100*float64(excess)/float64(expected))
		// Before reading that as inflation, rule out the reference. This whole comparison assumes the
		// snapshot is a COMPLETE picture of the UTXO set at its moment - every address, every coin. If
		// it omits addresses that already held balances, its total understates the supply of that
		// moment and every excess computed from it is overstated by however much it left out.
		//
		// The measurable symptom of that is coins sitting at addresses the reference has never heard
		// of. Some of those are genuinely new since the snapshot, so their presence is not proof; but
		// when they account for a large share of the excess, the reference is the more likely
		// explanation than the network having issued coins it did not issue.
		if heldByAddressesAbsentFromReference*2 >= excess {
			fmt.Printf("  BUT %d sompi of this node's supply sits at addresses the reference does not\n",
				heldByAddressesAbsentFromReference)
			fmt.Printf("  list at all - that is %.1f%% of the excess. A reference that omits addresses\n",
				100*float64(heldByAddressesAbsentFromReference)/float64(excess))
			fmt.Printf("  which already held coins understates the supply of its own moment, and every\n")
			fmt.Printf("  excess measured against it is overstated by exactly that much. Establish that\n")
			fmt.Printf("  the snapshot is a COMPLETE UTXO set before reading this as inflation.\n")
			break
		}
		fmt.Printf("  This node gained more than the network emitted. Coins it holds were never issued.\n")
	case expected > actual:
		shortfall := expected - actual
		fmt.Printf("  SHORTFALL           : -%d sompi (%.4f%% of expected)\n", shortfall,
			100*float64(shortfall)/float64(expected))
		fmt.Printf("  This node gained less than the network emitted. Either it is missing coins, or\n")
		fmt.Printf("  the reference DAA score is off - pass -referencedaascore to remove that doubt.\n")
	default:
		fmt.Printf("  Actual growth matches expected emission exactly.\n")
	}
	if estimated {
		fmt.Printf("  The reference DAA score was ESTIMATED, so treat a small difference either way as\n")
		fmt.Printf("  noise in that estimate rather than as a finding.\n")
	}
}

// currentBlockVersionIndex bounds a block version to a per-version parameter slice, the way
// dagconfig does internally.
func currentBlockVersionIndex(length int, blockVersion uint16) int {
	index := int(blockVersion) - 1
	if index < 0 {
		index = 0
	}
	if index >= length {
		index = length - 1
	}
	return index
}

// listAddressUTXOs breaks one address's balance down into the individual coins behind it.
//
// -balancecheck can say an address grew; it cannot say why. This can: every coin is reported with
// its amount, the DAA score it was stamped with, and whether a coinbase minted it. A balance made of
// coinbase outputs was mined into existence; one made of ordinary outputs was received from
// somewhere, and the transaction ids name where to look next.
//
// Read from the consensus UTXO set rather than the utxoindex, for the same reason -balancecheck is:
// the index is derived and has been wrong in this exact dimension.
func listAddressUTXOs(s *stores, sa *model.StagingArea, target string) {
	fmt.Printf("\n=== coins held by %s\n", target)

	iterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	type coin struct {
		outpoint   externalapi.DomainOutpoint
		amount     uint64
		daaScore   uint64
		isCoinbase bool
	}
	var coins []coin
	var total, coinbaseTotal uint64
	coinbaseCount := 0

	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			continue
		}
		_, address, err := txscript.ExtractScriptPubKeyAddress(entry.ScriptPublicKey(), &dagconfig.MainnetParams)
		if err != nil || address.EncodeAddress() != target {
			continue
		}
		coins = append(coins, coin{*outpoint, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase()})
		total += entry.Amount()
		if entry.IsCoinbase() {
			coinbaseCount++
			coinbaseTotal += entry.Amount()
		}
	}

	if len(coins) == 0 {
		fmt.Printf("  this address holds nothing in the consensus UTXO set\n")
		return
	}

	sort.Slice(coins, func(i, j int) bool { return coins[i].amount > coins[j].amount })

	fmt.Printf("  coins: %d, total %d sompi\n", len(coins), total)
	fmt.Printf("    minted by a coinbase : %d coins, %d sompi (%.2f%% of the balance)\n",
		coinbaseCount, coinbaseTotal, 100*float64(coinbaseTotal)/float64(total))
	fmt.Printf("    received from a transaction: %d coins, %d sompi\n",
		len(coins)-coinbaseCount, total-coinbaseTotal)
	fmt.Printf("  DAA score range: %d .. %d\n", coins[len(coins)-1].daaScore, coins[0].daaScore)

	shown := len(coins)
	if shown > 15 {
		shown = 15
	}
	fmt.Printf("\n  largest coins:\n")
	for _, c := range coins[:shown] {
		kind := "transaction output"
		if c.isCoinbase {
			kind = "COINBASE"
		}
		fmt.Printf("    %d sompi  daaScore=%d  %s\n      %s:%d\n",
			c.amount, c.daaScore, kind, &c.outpoint.TransactionID, c.outpoint.Index)
	}

	// A balance dominated by one coin is a different story from one accumulated in many, and the
	// difference decides where to look next.
	if coins[0].amount*2 > total {
		fmt.Printf("\n  A single coin is more than half this balance. Whatever created it is the whole\n")
		fmt.Printf("  story - trace that transaction rather than the address.\n")
	}
}

// addressesGainingInDAAWindow groups the coins stamped within a DAA score range by the address
// holding them, so a burst of activity can be attributed rather than guessed at.
//
// A coin's BlockDAAScore is the score of the block that merged it, so filtering on it selects coins
// that entered the set during that window. What comes back is therefore "received during this window
// and still unspent". Coins received then and spent since are invisible here, which makes this a
// lower bound on what moved - fine for finding who gained, useless for auditing flow.
func addressesGainingInDAAWindow(s *stores, sa *model.StagingArea, window string) {
	fromText, toText, found := strings.Cut(window, "-")
	if !found {
		fmt.Printf("\n  -daawindow wants \"from-to\", got %q\n", window)
		return
	}
	from, err := strconv.ParseUint(strings.TrimSpace(fromText), 10, 64)
	if err != nil {
		fmt.Printf("\n  -daawindow: %q is not a DAA score\n", fromText)
		return
	}
	to, err := strconv.ParseUint(strings.TrimSpace(toText), 10, 64)
	if err != nil {
		fmt.Printf("\n  -daawindow: %q is not a DAA score\n", toText)
		return
	}

	fmt.Printf("\n=== coins stamped between DAA %d and %d, by address\n", from, to)

	iterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	type holding struct {
		amount, coins, coinbaseAmount uint64
	}
	byAddress := make(map[string]*holding)
	var windowTotal, windowCoinbase uint64
	windowCoins, allCoins := 0, 0

	for ok := iterator.First(); ok; ok = iterator.Next() {
		_, entry, err := iterator.Get()
		if err != nil {
			continue
		}
		allCoins++
		if entry.BlockDAAScore() < from || entry.BlockDAAScore() > to {
			continue
		}
		windowCoins++
		windowTotal += entry.Amount()
		if entry.IsCoinbase() {
			windowCoinbase += entry.Amount()
		}
		_, address, err := txscript.ExtractScriptPubKeyAddress(entry.ScriptPublicKey(), &dagconfig.MainnetParams)
		if err != nil {
			continue
		}
		encoded := address.EncodeAddress()
		held := byAddress[encoded]
		if held == nil {
			held = &holding{}
			byAddress[encoded] = held
		}
		held.amount += entry.Amount()
		held.coins++
		if entry.IsCoinbase() {
			held.coinbaseAmount += entry.Amount()
		}
	}

	fmt.Printf("  coins in the set: %d, of which stamped in this window: %d\n", allCoins, windowCoins)
	if windowCoins == 0 {
		return
	}
	fmt.Printf("  held by %d addresses, %d sompi total\n", len(byAddress), windowTotal)
	fmt.Printf("    minted by a coinbase: %d sompi (%.2f%%)\n", windowCoinbase,
		100*float64(windowCoinbase)/float64(windowTotal))
	fmt.Printf("    received by transfer: %d sompi (%.2f%%)\n", windowTotal-windowCoinbase,
		100*float64(windowTotal-windowCoinbase)/float64(windowTotal))

	type row struct {
		address string
		holding
	}
	rows := make([]row, 0, len(byAddress))
	for address, held := range byAddress {
		rows = append(rows, row{address, *held})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].amount > rows[j].amount })

	shown := len(rows)
	if shown > 20 {
		shown = 20
	}
	fmt.Printf("\n  largest gainers in this window:\n")
	for _, r := range rows[:shown] {
		source := "transfer"
		if r.coinbaseAmount == r.amount {
			source = "COINBASE"
		} else if r.coinbaseAmount > 0 {
			source = fmt.Sprintf("mixed, %d sompi coinbase", r.coinbaseAmount)
		}
		fmt.Printf("    %22d sompi  %6d coins  %s\n      %s\n", r.amount, r.coins, source, r.address)
	}
	if len(rows) > shown {
		fmt.Printf("    ... and %d more addresses\n", len(rows)-shown)
	}
}

// supplyHistogramByDAAScore reports where this node's supply sits along the DAA axis.
//
// It exists because "supply grew by X since a snapshot" and "coins stamped in some window total Y"
// are different quantities, and comparing them directly invites a wrong conclusion. Growth is NET:
// spending an old coin destroys it and creates new ones carrying a fresh stamp, so the coins stamped
// after a point in time sum to more than the growth over that period - the difference is churn, not
// issuance. This lays out the whole distribution so the arithmetic can be done rather than assumed.
func supplyHistogramByDAAScore(s *stores, sa *model.StagingArea, bucketSize uint64) {
	fmt.Printf("\n=== supply by the DAA score its coins are stamped with (buckets of %d)\n", bucketSize)

	iterator, err := s.state.VirtualUTXOSetIterator(s.db, sa)
	if err != nil {
		fmt.Printf("  virtual UTXO set iterator: %v\n", err)
		return
	}
	defer iterator.Close()

	type bucket struct{ amount, coins, coinbase uint64 }
	buckets := make(map[uint64]*bucket)
	var total, coinbaseTotal uint64
	var lowest, highest uint64
	lowest = ^uint64(0)
	coins := 0

	for ok := iterator.First(); ok; ok = iterator.Next() {
		_, entry, err := iterator.Get()
		if err != nil {
			continue
		}
		coins++
		score := entry.BlockDAAScore()
		if score < lowest {
			lowest = score
		}
		if score > highest {
			highest = score
		}
		total += entry.Amount()
		if entry.IsCoinbase() {
			coinbaseTotal += entry.Amount()
		}
		key := score / bucketSize * bucketSize
		held := buckets[key]
		if held == nil {
			held = &bucket{}
			buckets[key] = held
		}
		held.amount += entry.Amount()
		held.coins++
		if entry.IsCoinbase() {
			held.coinbase += entry.Amount()
		}
	}

	if coins == 0 {
		fmt.Printf("  the UTXO set is empty\n")
		return
	}

	keys := make([]uint64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	fmt.Printf("  %d coins, %d sompi total, stamps from %d to %d\n", coins, total, lowest, highest)
	fmt.Printf("    minted by a coinbase: %d sompi (%.2f%%)\n\n", coinbaseTotal,
		100*float64(coinbaseTotal)/float64(total))
	fmt.Printf("  %-14s %22s %10s %9s %12s\n", "from DAA", "sompi", "coins", "coinbase%", "cumulative%")
	var running uint64
	for _, key := range keys {
		held := buckets[key]
		running += held.amount
		fmt.Printf("  %-14d %22d %10d %8.2f%% %11.2f%%\n", key, held.amount, held.coins,
			100*float64(held.coinbase)/float64(held.amount), 100*float64(running)/float64(total))
	}
}
