package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/pkg/errors"
)

func runVerify(args []string) error {
	fs := newFlagSet("verify")
	bundleDir := fs.String("bundle", "", "Bundle directory to verify")
	dbPath := fs.String("db-path", "", "Optional path to a stopped node's database directory. When given, the "+
		"bundle is additionally checked against the target block's own header UTXO commitment - the only "+
		"check that says whether the bundle is the set the chain actually committed to, rather than merely "+
		"matching its own manifest")
	dbType := fs.String("db-type", "pebble", "Database engine: pebble or leveldb")
	network := fs.String("network", "mainnet", "Network the database belongs to")
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if *bundleDir == "" {
		return errors.New("--bundle is required")
	}

	reader, err := exodus.OpenBundle(*bundleDir)
	if err != nil {
		return errors.Wrapf(err, "failed to open bundle at %s", *bundleDir)
	}
	manifest := reader.Manifest()

	fmt.Printf("Bundle:          %s\n", *bundleDir)
	fmt.Printf("Block hash:      %s\n", manifest.BlockHash)
	fmt.Printf("DAA score:       %d\n", manifest.DAAScore)
	fmt.Printf("Claimed commit:  %s\n", manifest.UTXOCommitment)
	fmt.Printf("Entry count:     %d\n", manifest.EntryCount)
	fmt.Printf("Generated at:    %s\n", manifest.GeneratedAt)
	fmt.Printf("Tool/node vers.: %s / %s\n", manifest.ToolVersion, manifest.NodeVersion)
	if manifest.OperatorNote != "" {
		fmt.Printf("Operator note:   %s\n", manifest.OperatorNote)
	}
	fmt.Println()

	result, err := reader.VerifySelfConsistency()
	if err != nil {
		return err
	}

	if len(result.ChunkErrors) > 0 {
		fmt.Println("Chunk errors:")
		for _, chunkErr := range result.ChunkErrors {
			fmt.Printf("  - %s\n", chunkErr)
		}
	}

	fmt.Printf("Recomputed commitment: %s\n", result.ComputedCommitment)
	fmt.Printf("Recomputed entry count: %d\n", result.EntryCount)

	if !result.Matches {
		return errors.New("bundle FAILED self-consistency verification (see details above)")
	}
	fmt.Println("OK: bundle is internally self-consistent.")

	if *dbPath == "" {
		fmt.Println("NOTE: self-consistency only proves the chunks match this bundle's own manifest. It says\n" +
			"nothing about whether the bundle is the UTXO set the chain committed to at that block.\n" +
			"Re-run with --db-path pointing at a stopped node to check it against the block's header\n" +
			"UTXO commitment.")
		return nil
	}

	cs, db, err := openConsensus(*dbPath, *dbType, *network)
	if err != nil {
		return err
	}
	defer db.Close()

	blockHash, err := externalapi.NewDomainHashFromString(manifest.BlockHash)
	if err != nil {
		return errors.Wrapf(err, "bundle manifest has an unparseable block hash %q", manifest.BlockHash)
	}
	header, err := cs.GetBlockHeader(blockHash)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch the header for the bundle's target block %s (does this "+
			"node have it?)", blockHash)
	}

	fmt.Printf("Header commitment:      %s\n", header.UTXOCommitment())
	if header.UTXOCommitment().Equal(result.ComputedCommitment) {
		fmt.Println("OK: bundle matches the target block's own header UTXO commitment.")
		return nil
	}

	return errors.Errorf("bundle commitment %s does NOT match the target block's own header UTXO commitment "+
		"%s.\nThe bundle is internally consistent but is not the UTXO set this chain committed to at %s, so "+
		"it must not be adopted as a trusted floor: doing so would make its errors permanent. Diff it against "+
		"a bundle from an independently-run node to locate where they disagree.",
		result.ComputedCommitment, header.UTXOCommitment(), blockHash)
}
