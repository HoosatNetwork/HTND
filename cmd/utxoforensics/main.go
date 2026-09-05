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
	"flag"
	"fmt"
	"math"
	"os"
	"sort"

	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/acceptancedatastore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/blockstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/consensusstatestore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/daablocksstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/headersselectedchainstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/multisetstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/utxodiffstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/prefixmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/pebble"
	"github.com/pkg/errors"
)

var (
	dbPath         = flag.String("db", "", "path to a COPY of a datadir2 (pebble)")
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
	baseTest = flag.Bool("basecheck", false, "hash the stored pruning point UTXO set, compare it to the pruning "+
		"point's header commitment, and - if it matches - use it as a network-sourced base to discriminate the "+
		"two DAA-stamp rules on the next selected-chain blocks")
)

type stores struct {
	db      model.DBManager
	headers model.BlockHeaderStore
	blocks  model.BlockStore
	accept  model.AcceptanceDataStore
	ms      model.MultisetStore
	gd      model.GHOSTDAGDataStore
	daa     model.DAABlocksStore
	chain   model.HeadersSelectedChainStore
	pruning model.PruningStore
	state   model.ConsensusStateStore
	diffs   model.UTXODiffStore
}

func main() {
	flag.Parse()
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

	if *reconstruct {
		reconstructPruningPointSet(s, sa)
	}

	if *ppHistory {
		pruningPointHistory(s, sa)
	}

	if *baseTest {
		baseCheck(s, sa)
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
		accept:  acceptancedatastore.New(pb, 100, false),
		ms:      multisetstore.New(pb, 100, false),
		gd:      ghostdagdatastore.New(pb.Bucket([]byte{0}), 100, false),
		daa:     daablocksstore.New(pb, 100, 100, false),
		chain:   headersselectedchainstore.New(pb, 100, false),
		pruning: pruningstore.New(pb, 2, false),
		state:   consensusstatestore.New(pb, 100, false),
		diffs:   utxodiffstore.New(pb, 100, false),
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
