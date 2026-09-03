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
		exportSnapshot = flag.String("export-snapshot", "",
			"write --src's served pruning-point UTXO set to this file and exit (read-only)")
		importSnapshot = flag.String("import-snapshot", "",
			"replace --src's served pruning-point UTXO set, and its multiset anchor, with this file")
		fromPruningPoint = flag.Bool("from-pruning-point", false,
			"pruned-datadir mode: seed from this node's own served pruning-point UTXO set and replay "+
				"forward instead of from genesis. Diagnostic only - it cannot establish correctness and "+
				"never persists anything.")
	)
	flag.Parse()

	if *src == "" {
		fmt.Fprintln(os.Stderr, "--src is required")
		flag.Usage()
		os.Exit(exitPreflight)
	}
	params, err := paramsByName(*networkName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitPreflight)
	}

	// Snapshot moves are their own operation: they neither walk nor derive, so they run before
	// the walk-specific argument checks and exit.
	if *exportSnapshot != "" || *importSnapshot != "" {
		if err := runSnapshot(*src, *exportSnapshot, *importSnapshot, params, *cacheSizeMiB); err != nil {
			fmt.Fprintf(os.Stderr, "\nderiveutxo: %s\n", err)
			os.Exit(exitFailed)
		}
		os.Exit(exitOK)
	}

	if *dst == "" && !*fromPruningPoint {
		fmt.Fprintln(os.Stderr, "--dst is required for a genesis walk (it is where the derived set is "+
			"written). --from-pruning-point writes nothing and may omit it.")
		flag.Usage()
		os.Exit(exitPreflight)
	}

	if err := run(*src, *dst, params, *probeDepth, *stopOnMismatch, *cacheSizeMiB, *skipCopy, *fromPruningPoint); err != nil {
		fmt.Fprintf(os.Stderr, "\nderiveutxo: %s\n", err)
		os.Exit(exitFailed)
	}
	os.Exit(exitOK)
}

// consensusDBDirname is the directory htnd actually keeps the consensus LevelDB in, under
// <appdir>/<network>/. Operators reach for the appdir, so resolve it for them rather than
// failing with "no active prefix".
const consensusDBDirname = "datadir2"

// resolveDataDir accepts either the consensus database directory itself or an appdir /
// appdir-network directory above it, and returns the database directory.
func resolveDataDir(path, networkName string) (string, error) {
	if utxoderive.LooksLikeDataDir(path) {
		return path, nil
	}
	for _, candidate := range []string{
		filepath.Join(path, consensusDBDirname),
		filepath.Join(path, networkName, consensusDBDirname),
	} {
		if utxoderive.LooksLikeDataDir(candidate) {
			fmt.Printf("Resolved %s -> %s\n", path, candidate)
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not a consensus database directory and does not contain one at "+
		"%s/%s or %s/%s/%s - point --src at the directory holding the MANIFEST/CURRENT files",
		path, path, consensusDBDirname, path, networkName, consensusDBDirname)
}

// runSnapshot moves a pruning-point UTXO set between datadirs as a file.
//
// This is the operation that makes two nodes agree. htnd's forward path was audited against an
// independent replay over 61,044 consecutive blocks from the same anchor and matched on every
// one, so nodes given the same pruning-point set stay in agreement; today they differ only
// because they imported different sets from different peers. Moving one set by hand removes that
// variable, which distributing it over P2P cannot, since the P2P export path is what diverged.
func runSnapshot(srcPath, exportPath, importPath string, params *dagconfig.Params, cacheSizeMiB int) error {
	if exportPath != "" && importPath != "" {
		return fmt.Errorf("choose either --export-snapshot or --import-snapshot, not both")
	}

	srcPath, err := resolveDataDir(srcPath, params.Name)
	if err != nil {
		return err
	}
	db, err := utxoderive.OpenDataDir(srcPath, cacheSizeMiB)
	if err != nil {
		return err
	}
	defer db.Close()
	prefixBytes, err := activePrefix(db)
	if err != nil {
		return err
	}

	if exportPath != "" {
		stores, err := utxoderive.OpenStores(db, prefixBytes, 10_000, false)
		if err != nil {
			return err
		}
		header, err := utxoderive.ExportSnapshot(stores, exportPath)
		if err != nil {
			return err
		}
		fmt.Printf("Exported %d entries to %s\n", header.EntryCount, exportPath)
		fmt.Printf("  pruning point : %s\n", header.PruningPoint)
		fmt.Printf("  multiset      : %s\n", header.Multiset)
		fmt.Println()
		fmt.Println("Every node that imports this file at the same pruning point will hold the same")
		fmt.Println("UTXO set and therefore report the same balances. It is NOT a claim that the set")
		fmt.Println("is correct - no set on this network currently matches its own header commitment.")
		return nil
	}

	header, err := utxoderive.ImportSnapshot(db, prefixBytes, importPath, 10_000)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d entries from %s\n", header.EntryCount, importPath)
	fmt.Printf("  pruning point : %s\n", header.PruningPoint)
	fmt.Printf("  multiset      : %s\n", header.Multiset)
	fmt.Println()
	fmt.Println("The served set and the node's multiset anchor now both match the snapshot.")
	fmt.Println("Restart the node and let it resolve forward.")
	return nil
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
	stopOnMismatch bool, cacheSizeMiB int, skipCopy, fromPruningPoint bool,
) error {
	srcPath, err := resolveDataDir(srcPath, params.Name)
	if err != nil {
		return err
	}

	backend := "leveldb"
	if utxoderive.IsPebbleDataDir(srcPath) {
		backend = "pebble"
	}

	// --- Preflight against the source, before anything is copied. ---
	fmt.Printf("Preflight against %s (%s)\n", srcPath, backend)
	srcDB, err := utxoderive.OpenDataDir(srcPath, cacheSizeMiB)
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
	if fromPruningPoint {
		if _, err := srcDeriver.PreflightFromPruningPoint(); err != nil {
			srcDB.Close()
			fmt.Fprintln(os.Stderr, "\nPreflight FAILED. Nothing was copied and nothing was written.")
			return err
		}
		srcDB.Close()
		fmt.Println("Preflight passed for a PRUNING-POINT-ANCHORED replay.")
		printSeededModeWarning()
	} else {
		if err := srcDeriver.Preflight(probeDepth); err != nil {
			srcDB.Close()
			fmt.Fprintln(os.Stderr, "\nPreflight FAILED. Nothing was copied and nothing was written.")
			fmt.Fprintln(os.Stderr, "If this datadir is pruned, --from-pruning-point runs the weaker,")
			fmt.Fprintln(os.Stderr, "diagnostic replay instead. It cannot mint a header-matching node.")
			return err
		}
		srcDB.Close()
		fmt.Println("Preflight passed: bodies and GHOSTDAG data are present below the pruning point.")
	}

	// --- Prepare the working directory. ---
	//
	// The genesis walk derives everything from bodies, so its destination must have every derived
	// store wiped or it could inherit the exported lineage. The seeded walk is the opposite: the
	// served pruning-point bucket IS its input, so wiping would destroy the thing it reads. It
	// therefore never copies and never wipes, and writes nothing at all.
	workPath := dstPath
	if fromPruningPoint {
		if dstPath == "" {
			workPath = srcPath
			fmt.Println("Seeded mode writes nothing, so it reads the source directly (no copy).")
		} else {
			if !skipCopy {
				fmt.Printf("Copying %s -> %s\n", srcPath, dstPath)
				if err := copyDir(srcPath, dstPath); err != nil {
					return err
				}
			}
			fmt.Println("Seeded mode: NOT wiping derived stores - the served pruning-point set is the input.")
		}
	} else if !skipCopy {
		fmt.Printf("Copying %s -> %s\n", srcPath, dstPath)
		if err := copyDir(srcPath, dstPath); err != nil {
			return err
		}
	}

	dstDB, err := utxoderive.OpenDataDir(workPath, cacheSizeMiB)
	if err != nil {
		return err
	}
	defer dstDB.Close()

	dstPrefix, err := activePrefix(dstDB)
	if err != nil {
		return err
	}

	if !fromPruningPoint {
		fmt.Println("Wiping derived stores from the destination")
		if err := utxoderive.WipeDerivedStores(dstDB, dstPrefix); err != nil {
			return err
		}
		if err := utxoderive.VerifyDerivedStoresAbsent(dstDB, dstPrefix); err != nil {
			return err
		}
		fmt.Println("Destination carries blocks, headers, GHOSTDAG and DAA only.")
	}

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

	if fromPruningPoint {
		// Seeded, diagnostic mode. Nothing is persisted, so no hooks are registered at all - the
		// persist path is not merely skipped, it is not reachable.
		if err := deriver.SeedFromPruningPointUTXOSet(); err != nil {
			return err
		}
		target, err := deriver.HighestChainBlockWithBody(pruningPoint)
		if err != nil {
			return err
		}
		fmt.Printf("Replaying forward from pruning point %s to %s\n", pruningPoint, target)

		walkErr := deriver.WalkRange(pruningPoint, target, nil)
		printReport(deriver.Report())
		if walkErr != nil {
			return walkErr
		}
		printSeededModeConclusion(deriver.Report())
		return nil
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

	if report := deriver.Report(); report.AcceptanceDiverged && persisted {
		return fmt.Errorf("internal error: a set was persisted despite acceptance divergence")
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

func printSeededModeWarning() {
	fmt.Println()
	fmt.Println("MODE: pruning-point-anchored replay (--from-pruning-point).")
	fmt.Println("  The bodies below the pruning point are gone from this datadir and no peer will serve")
	fmt.Println("  them, so the replay cannot start from genesis. It starts from this node's OWN served")
	fmt.Println("  pruning-point UTXO set, which is the artifact under suspicion.")
	fmt.Println()
	fmt.Println("  This CANNOT establish that any set is correct, and it will not persist anything.")
	fmt.Println("  What it can establish:")
	fmt.Println("    - whether acceptance still matches the network (AcceptedIDMerkleRoot per block)")
	fmt.Println("    - which outpoints the served set is missing, named individually")
	fmt.Println()
}

func printSeededModeConclusion(report *utxoderive.Report) {
	fmt.Println()
	fmt.Println("==================================================================")
	if report.SeedMatchesHeader {
		fmt.Println("The seed MATCHED its pruning point header commitment.")
		fmt.Println("That is unexpected given every peer surveyed so far and worth re-checking, but it")
		fmt.Println("means the UTXO commitments above are meaningful rather than relative.")
	} else {
		fmt.Printf("The seed did NOT match its header: served=%s header=%s (%d entries).\n",
			report.SeedMultiset, report.SeedHeaderCommitment, report.SeedEntries)
		fmt.Println("Every derived UTXO commitment above is therefore offset by the same amount and")
		fmt.Println("their mismatches carry no information. The two results that DO carry information:")
	}

	fmt.Println()
	fmt.Println("  audit against the node's OWN per-block multiset chain (same anchor, same blocks):")
	fmt.Printf("    seed equals the node's stored anchor : %t (node anchor %s)\n",
		report.SeedMatchesNodeAnchor, report.NodeAnchorMultiset)
	fmt.Printf("    blocks compared                      : %d\n", report.NodeMultisetChecked)
	fmt.Printf("    blocks where they AGREE              : %d\n", report.NodeMultisetAgreed)
	if report.FirstNodeMultisetDivergence != nil {
		fmt.Printf("    FIRST DIVERGENCE                     : block %s daa %d\n",
			report.FirstNodeMultisetDivergence.PruningPoint, report.FirstNodeMultisetDivergence.DAAScore)
		fmt.Printf("      node   : %s\n", report.FirstNodeMultisetDivergence.HeaderCommitment)
		fmt.Printf("      replay : %s\n", report.FirstNodeMultisetDivergence.DerivedMultiset)
		fmt.Println("      Both started from the same anchor, so this is the forward path, not the snapshot.")
	} else if report.NodeMultisetChecked > 0 {
		fmt.Println("    no divergence: the node's forward path matches an independent replay exactly.")
	}
	fmt.Println()

	acceptanceBroke := "no"
	if report.AcceptanceDiverged {
		acceptanceBroke = "YES"
	}
	fmt.Printf("  acceptance diverged from the network : %s\n", acceptanceBroke)
	fmt.Printf("  benign duplicate-transaction re-spends, discarded : %d\n", report.DuplicateSpendOccurrences)
	fmt.Printf("  real missing-input occurrences                    : %d\n", len(report.MissingInputs))
	fmt.Printf("  root missing coins (cascade removed)              : %d\n", len(report.RootMissingInputs))
	fmt.Println()
	fmt.Println("  where the root missing coins came from:")
	fmt.Printf("    created BELOW the pruning point (the export should have carried them) : %d\n",
		report.RootsPredatingPruningPoint)
	fmt.Printf("    created INSIDE the replayed range (the export is not to blame)        : %d\n",
		report.RootsCreatedInReplayedRange)
	if report.RootsCreatedInReplayedRange > 0 {
		fmt.Printf("      of those, creating tx seen AFTER the spend (walk-ordering fault) : %d\n",
			report.RootsCreatedAfterSpend)
		fmt.Printf("      of those, creating tx seen BEFORE the spend                      : %d\n",
			report.RootsCreatedBeforeSpend)
		fmt.Printf("        creating tx was never accepted, so the coin never existed here : %d\n",
			report.RootsCreatorNeverAccepted)
		fmt.Printf("        creating tx ID appeared more than once (duplicate transaction)  : %d\n",
			report.RootsCreatorSeenTwice)
		fmt.Printf("        creating tx accepted exactly once, coin genuinely lost          : %d\n",
			report.RootsCreatorAcceptedOnce)
	}

	if len(report.MissingInputs) > 0 {
		fmt.Println("\nroot missing outpoints (cascade removed):")
		for i, missing := range report.RootMissingInputs {
			if i >= 50 {
				fmt.Printf("  ... and %d more\n", len(report.RootMissingInputs)-50)
				break
			}
			fmt.Printf("  %s:%d spentBy=%s inBlock=%s\n",
				missing.Outpoint.TransactionID, missing.Outpoint.Index,
				missing.TransactionID, missing.InBlock)
		}
	}

	fmt.Println("\nNothing was persisted. This datadir is NOT a candidate header-matching node, and")
	fmt.Println("this run is not grounds for enabling Stage B.")
	fmt.Println("==================================================================")
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

	if len(report.Mismatches) > 1 {
		fmt.Printf("\nall %d mismatching blocks (--stop-on-mismatch was disabled):\n", len(report.Mismatches))
		for _, mismatch := range report.Mismatches {
			fmt.Printf("  block=%s daa=%d failed=%s\n", mismatch.PruningPoint, mismatch.DAAScore,
				mismatch.FailedChecks)
		}
	}

	if report.FirstMismatch != nil {
		mismatch := report.FirstMismatch
		fmt.Println("\nCORRUPTION HORIZON - first block whose commitments the replay could not reproduce:")
		fmt.Printf("  block             : %s\n", mismatch.PruningPoint)
		fmt.Printf("  daa               : %d\n", mismatch.DAAScore)
		fmt.Printf("  failed            : %s\n", mismatch.FailedChecks)
		fmt.Printf("  utxoHeader        : %s\n", mismatch.HeaderCommitment)
		fmt.Printf("  utxoDerived       : %s\n", mismatch.DerivedMultiset)
		fmt.Printf("  acceptedIDHeader  : %s\n", mismatch.HeaderAcceptedIDMerkleRoot)
		fmt.Printf("  acceptedIDDerived : %s\n", mismatch.DerivedAcceptedIDMerkleRoot)
	}

	if report.AcceptanceDiverged {
		fmt.Println("\nACCEPTANCE DIVERGED: the replay and the network disagree about which transactions")
		fmt.Println("were accepted. Everything derived after that block is meaningless rather than merely")
		fmt.Println("wrong, and nothing from this run may be persisted or served.")
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
