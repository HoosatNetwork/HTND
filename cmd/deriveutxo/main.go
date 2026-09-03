// Command deriveutxo rebuilds a UTXO set from block bodies and checks it against the block
// headers the network actually committed to.
//
// It exists because the network no longer agrees on UTXO state, so there is no snapshot left
// to adopt: a header-matching set has to be derived. It reads an archival datadir that is
// already on disk and never opens a network socket - the P2P layer cannot serve
// pre-pruning-point bodies to anything that asks for them, and a pruned peer answers such a
// request with a header-only block rather than an error.
//
// It writes only to the destination directory, and only after a walk whose derived commitment
// matched the target header.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utxoderive"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/prefixmanager"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

const (
	exitOK        = 0
	exitFailed    = 1
	exitPreflight = 2
)

func main() {
	logger.InitLogStdout(logger.LevelInfo)
	logger.SetLogLevels(logger.LevelInfo)

	var (
		src            = flag.String("src", "", "source datadir (must be archival and not in use)")
		dst            = flag.String("dst", "", "destination datadir for the derived result")
		networkName    = flag.String("network", "hoosat-mainnet", "network params to use")
		probeDepth     = flag.Int("probe-depth", utxoderive.DefaultProbeDepth, "how far below the pruning point preflight looks for a real body")
		stopOnMismatch = flag.Bool("stop-on-mismatch", true, "stop at the first commitment mismatch (the corruption horizon)")
		cacheSizeMiB   = flag.Int("cache", 256, "LevelDB cache size in MiB")
		skipCopy       = flag.Bool("skip-copy", false, "dst is already a copy of src; only wipe derived stores")
	)
	flag.Parse()

	if *src == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "both --src and --dst are required")
		flag.Usage()
		os.Exit(exitPreflight)
	}

	params, err := paramsByName(*networkName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitPreflight)
	}

	if err := run(*src, *dst, params, *probeDepth, *stopOnMismatch, *cacheSizeMiB, *skipCopy); err != nil {
		fmt.Fprintf(os.Stderr, "\nderiveutxo: %s\n", err)
		os.Exit(exitFailed)
	}
	os.Exit(exitOK)
}

func paramsByName(name string) (*dagconfig.Params, error) {
	for _, params := range []*dagconfig.Params{
		&dagconfig.MainnetParams, &dagconfig.TestnetParams,
		&dagconfig.SimnetParams, &dagconfig.DevnetParams,
	} {
		if params.Name == name {
			return params, nil
		}
	}
	return nil, fmt.Errorf("unknown network %q", name)
}

func run(srcPath, dstPath string, params *dagconfig.Params, probeDepth int,
	stopOnMismatch bool, cacheSizeMiB int, skipCopy bool,
) error {
	// --- Preflight against the source, before anything is copied. ---
	fmt.Printf("Preflight against %s\n", srcPath)
	srcDB, err := utxoderive.OpenLevelDB(srcPath, cacheSizeMiB)
	if err != nil {
		return err
	}
	srcPrefix, err := activePrefix(srcDB)
	if err != nil {
		srcDB.Close()
		return err
	}
	srcStores, err := utxoderive.OpenStores(srcDB, srcPrefix, 10_000, false)
	if err != nil {
		srcDB.Close()
		return err
	}
	srcDeriver, err := utxoderive.New(srcStores, params.GenesisHash, stopOnMismatch)
	if err != nil {
		srcDB.Close()
		return err
	}
	if err := srcDeriver.Preflight(probeDepth); err != nil {
		srcDB.Close()
		fmt.Fprintln(os.Stderr, "\nPreflight FAILED. Nothing was copied and nothing was written.")
		return err
	}
	srcDB.Close()
	fmt.Println("Preflight passed: bodies and GHOSTDAG data are present below the pruning point.")

	// --- Prepare the destination. ---
	if !skipCopy {
		fmt.Printf("Copying %s -> %s\n", srcPath, dstPath)
		if err := copyDir(srcPath, dstPath); err != nil {
			return err
		}
	}

	dstDB, err := utxoderive.OpenLevelDB(dstPath, cacheSizeMiB)
	if err != nil {
		return err
	}
	defer dstDB.Close()

	dstPrefix, err := activePrefix(dstDB)
	if err != nil {
		return err
	}

	fmt.Println("Wiping derived stores from the destination")
	if err := utxoderive.WipeDerivedStores(dstDB, dstPrefix); err != nil {
		return err
	}
	if err := utxoderive.VerifyDerivedStoresAbsent(dstDB, dstPrefix); err != nil {
		return err
	}
	fmt.Println("Destination carries blocks, headers, GHOSTDAG and DAA only.")

	// --- Replay. ---
	dstStores, err := utxoderive.OpenStores(dstDB, dstPrefix, 10_000, false)
	if err != nil {
		return err
	}
	deriver, err := utxoderive.New(dstStores, params.GenesisHash, stopOnMismatch)
	if err != nil {
		return err
	}

	pruningPoint, err := dstStores.PruningStore.PruningPoint(dstStores.DatabaseContext, model.NewStagingArea())
	if err != nil {
		return err
	}
	fmt.Printf("Replaying from genesis to pruning point %s\n", pruningPoint)

	persisted := false
	hooks := map[externalapi.DomainHash]utxoderive.CheckpointHook{
		*pruningPoint: func(blockHash *externalapi.DomainHash, d *utxoderive.Deriver) error {
			fmt.Printf("\nMATCH at the current pruning point %s - persisting the derived set\n", blockHash)
			if err := utxoderive.PersistPruningPointUTXOSet(dstDB, dstPrefix, d.UTXOs(), 10_000); err != nil {
				return err
			}
			persisted = true
			return nil
		},
	}

	walkErr := deriver.Walk(pruningPoint, hooks)
	printReport(deriver.Report())
	if walkErr != nil {
		return walkErr
	}

	if !persisted {
		fmt.Println("\nNothing was persisted: the derived commitment did not match the pruning point header.")
		fmt.Println("The destination holds inputs and a report only - it must not be served.")
		return fmt.Errorf("replay did not reach a matching commitment at the current pruning point")
	}

	fmt.Printf("\nDerived set persisted. entries=%d sum=%d sompi\n",
		deriver.Report().DerivedEntries, deriver.Report().DerivedSum)
	fmt.Println("This destination is a CANDIDATE first header-matching node.")
	fmt.Println("Do not enable Stage B on the strength of it - see docs/utxo-set-verification.md.")
	return nil
}

func printReport(report *utxoderive.Report) {
	fmt.Printf("\nchain blocks walked   : %d\n", report.ChainBlocks)
	fmt.Printf("merge-set blocks      : %d\n", report.BlocksApplied)
	fmt.Printf("transactions accepted : %d\n", report.TxsAccepted)

	if len(report.Checkpoints) > 0 {
		fmt.Println("\npruning point checkpoints:")
		fmt.Printf("  %-64s %12s %-16s %-16s %s\n", "ppHash", "daa", "derived", "header", "status")
		for _, checkpoint := range report.Checkpoints {
			status := "MATCH"
			if !checkpoint.Match {
				status = "MISMATCH"
			}
			fmt.Printf("  %-64s %12d %-16.16s %-16.16s %s\n",
				checkpoint.PruningPoint, checkpoint.DAAScore,
				checkpoint.DerivedMultiset, checkpoint.HeaderCommitment, status)
		}
	}

	if report.FirstMismatch != nil {
		fmt.Println("\nCORRUPTION HORIZON - first block whose header commitment the replay could not reproduce:")
		fmt.Printf("  block   : %s\n", report.FirstMismatch.PruningPoint)
		fmt.Printf("  daa     : %d\n", report.FirstMismatch.DAAScore)
		fmt.Printf("  header  : %s\n", report.FirstMismatch.HeaderCommitment)
		fmt.Printf("  derived : %s\n", report.FirstMismatch.DerivedMultiset)
	}

	if report.StopReason != "" {
		fmt.Printf("\nstopped at %s: %s\n", report.StoppedAt, report.StopReason)
	}
}

func activePrefix(db infrastructuredatabase.Database) ([]byte, error) {
	activePrefix, exists, err := prefixmanager.ActivePrefix(db)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("datadir has no active prefix - it is not an initialised consensus database")
	}
	return activePrefix.Serialize(), nil
}

// copyDir copies a closed LevelDB directory. Both databases must be closed; the caller
// enforces that by opening src only for preflight and closing it first.
func copyDir(srcPath, dstPath string) error {
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("destination %s already exists - refusing to overwrite", dstPath)
	}
	return filepath.Walk(srcPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(srcPath, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstPath, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer destination.Close()
		_, err = io.Copy(destination, source)
		return err
	})
}
