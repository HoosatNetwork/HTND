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
func resolveBlockByDAAScore(cs externalapi.Consensus, targetDAAScore uint64) (*externalapi.DomainHash, error) {
	hash, err := cs.GetVirtualSelectedParent()
	if err != nil {
		return nil, err
	}

	for {
		header, err := cs.GetBlockHeader(hash)
		if err != nil {
			return nil, err
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
		hash = info.SelectedParent
	}
}
