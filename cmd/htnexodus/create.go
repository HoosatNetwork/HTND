package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/HoosatNetwork/HTND/version"
	"github.com/pkg/errors"
)

func runCreate(args []string) error {
	fs := newFlagSet("create")
	dbPath := fs.String("db-path", "", "Path to the node's database directory")
	dbType := fs.String("db-type", "pebble", "Database engine: pebble or leveldb")
	network := fs.String("network", "mainnet", "Network the database belongs to")
	blockHashFlag := fs.String("block", "", "Target block hash (hex)")
	daaScoreFlag := fs.Uint64("daa-score", 0, "Target block DAA score")
	out := fs.String("out", "", "Directory to write the candidate bundle to")
	note := fs.String("note", "", "Free-text operator note/identity to embed in the bundle")
	chunkSize := fs.Int("chunk-size", exodus.DefaultChunkEntryCount, "UTXO entries per chunk file")
	source := fs.String("source", "acceptance-data", "Which derivation of the UTXO set to snapshot: "+
		"\"acceptance-data\" (rebuild it from the pruning point set plus the recorded acceptance data - the "+
		"derivation that reproduces block headers' UTXO commitments) or \"materialised\" (read virtual's "+
		"UTXO table through the stored diff chain, which is never recomputed and is known to drift)")
	allowMismatch := fs.Bool("allow-commitment-mismatch", false, "Write the bundle even when its commitment "+
		"does not match the target block's own header UTXO commitment. A bundle that fails that check is not "+
		"the UTXO set the chain committed to, and adopting one as a trusted floor makes its errors permanent.")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	if *dbPath == "" {
		return errors.New("--db-path is required")
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	if *blockHashFlag == "" && *daaScoreFlag == 0 {
		return errors.New("either --block or --daa-score must be given")
	}
	if *blockHashFlag != "" && *daaScoreFlag != 0 {
		return errors.New("only one of --block or --daa-score may be given")
	}

	cs, db, err := openConsensus(*dbPath, *dbType, *network)
	if err != nil {
		return err
	}
	defer db.Close()

	var blockHash *externalapi.DomainHash
	if *blockHashFlag != "" {
		blockHash, err = externalapi.NewDomainHashFromString(*blockHashFlag)
		if err != nil {
			return errors.Wrapf(err, "invalid --block hash")
		}
	} else {
		blockHash, err = resolveBlockByDAAScore(cs, *daaScoreFlag)
		if err != nil {
			return err
		}
	}

	header, err := cs.GetBlockHeader(blockHash)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch header for target block %s", blockHash)
	}
	daaScore := header.DAAScore()

	var iterate func(*externalapi.DomainHash, func(*externalapi.DomainOutpoint, externalapi.UTXOEntry) error) error
	switch *source {
	case "acceptance-data":
		iterate = cs.IterateUTXOSetAtBlockFromAcceptanceData
	case "materialised", "materialized":
		iterate = cs.IterateUTXOSetAtBlock
	default:
		return errors.Errorf("unknown --source %q (expected \"acceptance-data\" or \"materialised\")", *source)
	}

	fmt.Printf("Target block: %s (DAA score %d)\n", blockHash, daaScore)
	fmt.Printf("Walking UTXO set into bundle at %s (source: %s) ...\n", *out, *source)

	writer, err := exodus.NewWriter(*out, exodus.BundleTarget{BlockHash: blockHash, DAAScore: daaScore}, *chunkSize)
	if err != nil {
		return err
	}

	err = iterate(blockHash, func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		err := writer.AddEntry(outpoint, entry)
		if err != nil {
			return err
		}
		if writer.EntryCount()%1_000_000 == 0 {
			fmt.Printf("  ... %d entries processed\n", writer.EntryCount())
			// Persist progress periodically so a later interruption loses as little
			// already-written work as possible.
			return writer.SaveProgress()
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(err, "failed while walking the UTXO set (partial progress was saved to %s and can be resumed)", *out)
	}

	commitment, err := writer.Finalize(exodus.BundleMeta{
		ToolVersion:  version.Version(),
		NodeVersion:  version.Version(),
		Network:      (*network),
		OperatorNote: *note,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Wrote %d UTXO entries.\n", writer.EntryCount())
	fmt.Printf("Computed UTXO set commitment: %s\n", commitment)
	if header.UTXOCommitment().Equal(commitment) {
		fmt.Printf("Matches the target block's own header UTXO commitment.\n")
		return nil
	}

	// The header commitment is the one thing here the network agreed on: it is in a block that was
	// mined, propagated and accepted. A bundle that hashes to something else is, by definition, not
	// the UTXO set this chain committed to at that block - and a bundle's whole purpose is to become
	// a floor that nodes stop recomputing, so adopting a mismatched one makes its errors permanent
	// and unrecoverable. Refuse by default rather than emit it with a warning.
	message := fmt.Sprintf("bundle commitment %s does NOT match the target block's own header UTXO "+
		"commitment %s", commitment, header.UTXOCommitment())
	if !*allowMismatch {
		return errors.Errorf("%s.\nThe bundle was written to %s but must not be published as a candidate "+
			"as it stands.\nIf --source was \"materialised\", retry with --source acceptance-data: virtual's "+
			"materialised UTXO table is never recomputed, so a mis-applied UTXO diff persists in it "+
			"indefinitely, while acceptance data is what the per-block multiset chain the headers commit "+
			"to is built from.\nIf it already was \"acceptance-data\", this node's pruning point set is "+
			"itself offset from what the chain committed to, and no bundle derived from it can be "+
			"trusted - compare against an independently-run node with \"htnexodus diff\" before going "+
			"further.\nPass --allow-commitment-mismatch only to produce an artifact for that comparison, "+
			"never to publish a candidate.", message, *out)
	}
	fmt.Printf("WARNING: %s.\n"+
		"Written anyway because --allow-commitment-mismatch was given. This bundle is NOT the UTXO set\n"+
		"the chain committed to at this block and must not be adopted as a trusted floor; it is only\n"+
		"useful for diffing against another node's bundle to locate where they disagree.\n", message)

	return nil
}

// resolveBlockByDAAScore walks the selected parent chain backward from the virtual selected
// parent until it finds a block with the requested DAA score. DAA score is monotonically
// non-decreasing along the selected parent chain, so this always terminates - either at the
// requested score, or with a definitive "not found" once scores go below the target.
//
// Before walking, the requested score is bounds-checked against the node's own pruning point:
// anything older than the pruning point is guaranteed not to be retained locally (its full block
// data, and therefore its GHOSTDAG selected-parent chain, has been discarded). Without this check
// the walk would instead run all the way down to the pruning point/its anticone - which, once
// imported via the trusted pruning-point-proof IBD path, has its GHOSTDAG selected parent recorded
// as a synthetic "virtual genesis" marker rather than a real, fetchable block header - and fail
// with a confusing low-level "block header ...fefefe...fe does not exist" error instead of a clear
// one.
func resolveBlockByDAAScore(cs externalapi.Consensus, targetDAAScore uint64) (*externalapi.DomainHash, error) {
	hash, err := cs.GetVirtualSelectedParent()
	if err != nil {
		return nil, err
	}

	tipHeader, err := cs.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}
	tipDAAScore := tipHeader.DAAScore()

	pruningPointHash, err := cs.PruningPoint()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch the local pruning point")
	}
	pruningPointHeader, err := cs.GetBlockHeader(pruningPointHash)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch the local pruning point's header")
	}
	pruningPointDAAScore := pruningPointHeader.DAAScore()

	if targetDAAScore > tipDAAScore {
		return nil, errors.Errorf(
			"requested DAA score %d is beyond the node's current tip (DAA score %d); the node has not "+
				"synced that far yet", targetDAAScore, tipDAAScore)
	}
	if targetDAAScore < pruningPointDAAScore {
		return nil, errors.Errorf(
			"requested DAA score %d is older than the node's local pruning point (DAA score %d, hash %s); "+
				"this history has been pruned and is no longer retained locally. Choose a DAA score between "+
				"%d and %d (comfortably above the pruning point, to leave margin against it advancing before "+
				"you can re-run this tool)", targetDAAScore, pruningPointDAAScore, pruningPointHash,
			pruningPointDAAScore, tipDAAScore)
	}

	for {
		header, err := cs.GetBlockHeader(hash)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch header for %s while walking back from the tip "+
				"toward DAA score %d", hash, targetDAAScore)
		}
		daaScore := header.DAAScore()
		if daaScore == targetDAAScore {
			return hash, nil
		}
		if daaScore < targetDAAScore {
			return nil, errors.Errorf(
				"DAA score %d was not found on the selected parent chain (closest block below it, %s, has "+
					"DAA score %d); has the node synced past this DAA score?",
				targetDAAScore, hash, daaScore)
		}

		info, err := cs.GetBlockInfo(hash)
		if err != nil {
			return nil, err
		}
		if info.SelectedParent == nil {
			return nil, errors.Errorf("reached the start of the chain without finding DAA score %d", targetDAAScore)
		}
		if hash.Equal(pruningPointHash) {
			// We already bounds-checked targetDAAScore against the pruning point above, so if we
			// end up walking as far back as the pruning point itself without a match, the requested
			// score falls in a gap the local selected chain skips right at the pruning boundary.
			// Stop here with a clear message instead of following SelectedParent into the synthetic
			// "virtual genesis" marker used for trusted, pruning-point-proof-imported GHOSTDAG data.
			return nil, errors.Errorf(
				"reached the local pruning point (%s, DAA score %d) without finding an exact match for "+
					"DAA score %d; try a slightly different DAA score", hash, daaScore, targetDAAScore)
		}
		hash = info.SelectedParent
	}
}
