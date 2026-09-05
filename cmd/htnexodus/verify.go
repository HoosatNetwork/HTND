package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/pkg/errors"
)

func runVerify(args []string) error {
	fs := newFlagSet("verify")
	bundleDir := fs.String("bundle", "", "Bundle directory to verify")
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

	if result.Matches {
		fmt.Println("OK: bundle is internally self-consistent.")
		return nil
	}

	return errors.New("bundle FAILED self-consistency verification (see details above)")
}
