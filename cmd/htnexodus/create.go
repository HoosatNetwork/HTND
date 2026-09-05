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

	fmt.Printf("Target block: %s (DAA score %d)\n", blockHash, daaScore)
	fmt.Printf("Walking UTXO set into bundle at %s ...\n", *out)

	writer, err := exodus.NewWriter(*out, exodus.BundleTarget{BlockHash: blockHash, DAAScore: daaScore}, *chunkSize)
	if err != nil {
		return err
	}

	err = cs.IterateUTXOSetAtBlock(blockHash, func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
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
	} else {
		fmt.Printf("WARNING: does NOT match the target block's own header UTXO commitment (%s).\n"+
			"This is expected if the node's locally-tolerated pruning UTXO set calculation is exactly\n"+
			"the discrepancy this tooling exists to help investigate; it does not necessarily mean the\n"+
			"bundle itself is wrong.\n", header.UTXOCommitment())
	}

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
