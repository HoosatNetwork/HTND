package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/pkg/errors"
)

func runDiff(args []string) error {
	fs := newFlagSet("diff")
	bundleADir := fs.String("bundle-a", "", "First bundle directory to diff")
	bundleBDir := fs.String("bundle-b", "", "Second bundle directory to diff")
	live := fs.Bool("live", false, "Diff --bundle-a against a live recomputation instead of --bundle-b")
	dbPath := fs.String("db-path", "", "Path to the node's database directory (required with --live)")
	dbType := fs.String("db-type", "pebble", "Database engine: pebble or leveldb")
	network := fs.String("network", "mainnet", "Network the database belongs to")
	maxPrint := fs.Int("max-print", 20, "Maximum number of differing/unique outpoints to print per category")
	source := fs.String("source", "acceptance-data", "With --live, which derivation to recompute the UTXO set "+
		"from: \"acceptance-data\" or \"materialised\". Diffing the two against each other on one node is "+
		"itself informative: they are meant to be the same set, and where they are not, the materialised "+
		"one is the side that drifted")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	if *bundleADir == "" {
		return errors.New("--bundle-a is required")
	}
	if *live && *bundleBDir != "" {
		return errors.New("--live and --bundle-b are mutually exclusive")
	}
	if !*live && *bundleBDir == "" {
		return errors.New("either --bundle-b or --live must be given")
	}

	readerA, err := exodus.OpenBundle(*bundleADir)
	if err != nil {
		return errors.Wrapf(err, "failed to open bundle at %s", *bundleADir)
	}

	var sourceB exodus.Source
	var labelB string

	if *live {
		if *dbPath == "" {
			return errors.New("--db-path is required with --live")
		}
		blockHash, err := externalapi.NewDomainHashFromString(readerA.Manifest().BlockHash)
		if err != nil {
			return errors.Wrapf(err, "bundle A has a malformed block hash")
		}

		cs, db, err := openConsensus(*dbPath, *dbType, *network)
		if err != nil {
			return err
		}
		defer db.Close()

		var iterate func(*externalapi.DomainHash,
			func(*externalapi.DomainOutpoint, externalapi.UTXOEntry) error) error
		switch *source {
		case "acceptance-data":
			iterate = cs.IterateUTXOSetAtBlockFromAcceptanceData
		case "materialised", "materialized":
			iterate = cs.IterateUTXOSetAtBlock
		default:
			return errors.Errorf("unknown --source %q (expected \"acceptance-data\" or \"materialised\")", *source)
		}

		sourceB = func(callback exodus.EntryCallback) error {
			return iterate(blockHash, callback)
		}
		labelB = fmt.Sprintf("live recomputation at %s (from %s, source: %s)", blockHash, *dbPath, *source)
	} else {
		readerB, err := exodus.OpenBundle(*bundleBDir)
		if err != nil {
			return errors.Wrapf(err, "failed to open bundle at %s", *bundleBDir)
		}
		sourceB = readerB.AsSource()
		labelB = *bundleBDir
	}

	fmt.Printf("A: %s (block %s, DAA score %d)\n", *bundleADir, readerA.Manifest().BlockHash, readerA.Manifest().DAAScore)
	fmt.Printf("B: %s\n\n", labelB)

	result, err := exodus.Diff(readerA.AsSource(), sourceB)
	if err != nil {
		return err
	}

	fmt.Printf("Entries in A: %d\n", result.CountA)
	fmt.Printf("Entries in B: %d\n", result.CountB)
	fmt.Printf("Only in A:    %d (aggregate value %d sompi)\n", len(result.OnlyInA), result.ValueOnlyInA)
	fmt.Printf("Only in B:    %d (aggregate value %d sompi)\n", len(result.OnlyInB), result.ValueOnlyInB)
	fmt.Printf("Differing:    %d\n\n", len(result.Differing))

	printEntries := func(title string, entries []exodus.EntryDiff) {
		if len(entries) == 0 {
			return
		}
		fmt.Printf("%s:\n", title)
		for i, entryDiff := range entries {
			if i >= *maxPrint {
				fmt.Printf("  ... and %d more\n", len(entries)-*maxPrint)
				break
			}
			fmt.Printf("  %s:%d  A=%s  B=%s\n", entryDiff.Outpoint.TransactionID, entryDiff.Outpoint.Index,
				describeEntry(entryDiff.InA), describeEntry(entryDiff.InB))
		}
	}

	printEntries("Only in A", result.OnlyInA)
	printEntries("Only in B", result.OnlyInB)
	printEntries("Differing", result.Differing)

	if result.Identical() {
		fmt.Println("Sets are IDENTICAL.")
		return nil
	}

	return errors.New("sets DIFFER (see details above)")
}

func describeEntry(entry externalapi.UTXOEntry) string {
	if entry == nil {
		return "<absent>"
	}
	return fmt.Sprintf("amount=%d daaScore=%d coinbase=%t", entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase())
}
