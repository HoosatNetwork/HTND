package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/exodus"
	"github.com/pkg/errors"
)

// runImport implements `htnexodus import`: rebaselines a locally synced node's own consensus
// state onto a previously exported exodus bundle, per HoosatNetwork/HTND#20.
//
// Scope/limitations, deliberately kept narrow for this first version:
//   - This only supports rebaselining onto a block the node already fully has locally (its own
//     header, block body and GHOSTDAG data must already exist). It does not implement any
//     peer/network distribution of an unknown candidate block - per #20's explicit non-goals,
//     there is no node-to-node distribution protocol for candidates here.
//   - It reuses the exact same, already-in-production consensus machinery real headers-proof IBD
//     uses to bootstrap a fresh node's initial pruning point
//     (ClearImportedPruningPointData / AppendImportedPruningPointUTXOs /
//     ValidateAndInsertImportedPruningPoint on externalapi.Consensus), rather than adding new,
//     never-before-exercised consensus code. That machinery already tolerates this chain's known
//     inherited UTXO-baseline offset (see pruningmanager.go's
//     verifyAndRepairImportedPruningPointUTXOSet) the same way a real IBD run does.
//   - It does not update the pruning-point store's "current pruning point" pointer
//     (externalapi.Consensus.PruningPoint()) - that is only ever advanced by ordinary block
//     processing as pruning depth naturally passes (UpdatePruningPointByVirtual), which is out of
//     scope here per #20/#21's "no changes to fork-choice or pruning-point selection logic". This
//     import only forces virtual's parent and UTXO baseline; PruningPoint() will continue to
//     report whatever it already did until the node's ordinary pruning-point-advancement logic
//     catches up during normal operation.
//   - It does not attempt to identify or discard "diverging" local blocks/pruning points (the
//     fork-override scenario) - #20 itself lists this as a design question not necessarily
//     resolved in v1 code. This import is intended for the straightforward case: rebaselining
//     onto a block already on the node's own current selected parent chain.
func runImport(args []string) error {
	fs := newFlagSet("import")
	dbPath := fs.String("db-path", "", "Path to the node's database directory")
	dbType := fs.String("db-type", "pebble", "Database engine: pebble or leveldb")
	network := fs.String("network", "mainnet", "Network the database belongs to")
	bundleDir := fs.String("bundle", "", "Path to the candidate bundle directory to import")
	batchSize := fs.Int("batch-size", exodus.DefaultChunkEntryCount,
		"UTXO entries staged per AppendImportedPruningPointUTXOs call")
	allowMismatch := fs.Bool("allow-commitment-mismatch", false,
		"Import the bundle even though its commitment does not match the target block's own header UTXO "+
			"commitment. Such a bundle is not the UTXO set the chain committed to at that block, and importing "+
			"it as a trusted floor makes its errors permanent - this node stops recomputing that set.")
	force := fs.Bool("force", false,
		"Required: actually perform the rebaseline (this overwrites the node's virtual chain "+
			"state and UTXO baseline)")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	if *bundleDir == "" {
		return errors.New("--bundle is required")
	}
	if *dbPath == "" {
		return errors.New("--db-path is required")
	}
	if !*force {
		return errors.New("refusing to import without --force: this OVERWRITES the node's virtual " +
			"chain state and UTXO baseline with the bundle's contents. The node process must be " +
			"stopped first (it must not be running against --db-path at the same time). Re-run " +
			"with --force once you are sure")
	}

	reader, err := exodus.OpenBundle(*bundleDir)
	if err != nil {
		return errors.Wrapf(err, "failed to open bundle %s", *bundleDir)
	}
	manifest := reader.Manifest()

	fmt.Printf("Verifying bundle self-consistency before touching any node state...\n")
	verifyResult, err := reader.VerifySelfConsistency()
	if err != nil {
		return errors.Wrapf(err, "failed to verify bundle %s", *bundleDir)
	}
	if !verifyResult.Matches {
		for _, chunkErr := range verifyResult.ChunkErrors {
			fmt.Printf("  chunk error: %s\n", chunkErr)
		}
		return errors.Errorf(
			"bundle at %s failed self-consistency verification (claimed commitment %s, recomputed "+
				"%s); refusing to import a bundle that does not match its own claim - re-run `exodus "+
				"verify --bundle %s` for details", *bundleDir, verifyResult.ClaimedCommitment,
			verifyResult.ComputedCommitment, *bundleDir)
	}
	fmt.Printf("Bundle is self-consistent: %d entries, commitment %s\n", verifyResult.EntryCount,
		verifyResult.ClaimedCommitment)

	blockHash, err := externalapi.NewDomainHashFromString(manifest.BlockHash)
	if err != nil {
		return errors.Wrapf(err, "bundle manifest has a malformed block hash %q", manifest.BlockHash)
	}

	cs, db, err := openConsensus(*dbPath, *dbType, *network)
	if err != nil {
		return err
	}
	defer db.Close()

	info, err := cs.GetBlockInfo(blockHash)
	if err != nil {
		return errors.Wrapf(err, "failed to look up target block %s locally", blockHash)
	}
	if !info.Exists {
		return errors.Errorf(
			"target block %s (from the bundle's manifest) is not known to this node at all. "+
				"`exodus import` only supports rebaselining onto a block this node already has "+
				"locally in full (header, body and GHOSTDAG data); it does not implement any "+
				"peer/network distribution of an unknown candidate block (see HoosatNetwork/HTND#20's "+
				"explicit non-goals)", blockHash)
	}
	if info.BlockStatus == externalapi.StatusInvalid {
		return errors.Errorf(
			"target block %s is marked %s locally; refusing to rebaseline onto a block this node has "+
				"already rejected", blockHash, info.BlockStatus)
	}
	if info.BlockStatus == externalapi.StatusHeaderOnly {
		return errors.Errorf(
			"target block %s is header-only locally (no block body retained - likely pruned); "+
				"`exodus import` needs the full block body to validate its own transactions against "+
				"the imported UTXO set", blockHash)
	}

	// The target block's header commitment is the one value in this whole procedure that the network
	// demonstrably agreed on - it is in a block that was mined, propagated and accepted. Importing a
	// bundle that hashes to anything else installs a UTXO set the chain never committed to, as the
	// very thing this node will stop recomputing. That is the irreversible step, so it is checked
	// here and not only in create/verify, which an operator receiving a bundle may never have run.
	targetHeader, err := cs.GetBlockHeader(blockHash)
	if err != nil {
		return errors.Wrapf(err, "failed to fetch the header of target block %s", blockHash)
	}
	if manifest.UTXOCommitment != targetHeader.UTXOCommitment().String() {
		message := fmt.Sprintf("bundle commitment %s does NOT match the target block's own header UTXO "+
			"commitment %s", manifest.UTXOCommitment, targetHeader.UTXOCommitment())
		if !*allowMismatch {
			return errors.Errorf("%s.\nRefusing to rebaseline onto a UTXO set this chain did not commit "+
				"to at %s. Verify the bundle against a node with `htnexodus verify --bundle <dir> --db-path "+
				"<path>`, and diff it against a bundle from an independently-run node, before importing "+
				"anything. Pass --allow-commitment-mismatch only if you have decided, deliberately and with "+
				"the rest of the network, to adopt a set that does not match the header.",
				message, blockHash)
		}
		fmt.Printf("WARNING: %s.\nImporting anyway because --allow-commitment-mismatch was given.\n", message)
	}

	previousPruningPoint, err := cs.PruningPoint()
	if err != nil {
		return errors.Wrapf(err, "failed to read the current pruning point")
	}
	previousVirtualSelectedParent, err := cs.GetVirtualSelectedParent()
	if err != nil {
		return errors.Wrapf(err, "failed to read the current virtual selected parent")
	}

	fmt.Printf("\n=== OVERRIDING LOCAL CHAIN STATE ===\n")
	fmt.Printf("Previous virtual selected parent: %s\n", previousVirtualSelectedParent)
	fmt.Printf("Previous pruning point (record left unchanged by this import): %s\n", previousPruningPoint)
	fmt.Printf("New trusted floor: block %s, DAA score %d, bundle commitment %s\n",
		blockHash, manifest.DAAScore, manifest.UTXOCommitment)
	fmt.Printf("Forcing the virtual block's sole parent to this block and replacing the node's virtual "+
		"UTXO set with the bundle's %d entries as the new trusted baseline.\n", manifest.EntryCount)
	fmt.Printf("This does not change any consensus rule, does not verify the bundle's authenticity " +
		"beyond internal self-consistency (no signature/ratification), and does not distribute this " +
		"bundle to any peer.\n\n")

	fmt.Printf("Clearing any stale import staging data...\n")
	err = cs.ClearImportedPruningPointData()
	if err != nil {
		return errors.Wrapf(err, "failed to clear stale import staging data")
	}
	// Mirror the real IBD-with-headers-proof flow's own cleanup pattern: never leave stray staged
	// import data behind, whether this import succeeds or fails.
	defer func() {
		if clearErr := cs.ClearImportedPruningPointData(); clearErr != nil {
			fmt.Printf("warning: failed to clear import staging data after import: %s\n", clearErr)
		}
	}()

	fmt.Printf("Streaming %d UTXO entries from the bundle into the node (batches of %d)...\n",
		manifest.EntryCount, *batchSize)
	batch := make([]*externalapi.OutpointAndUTXOEntryPair, 0, *batchSize)
	staged := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := cs.AppendImportedPruningPointUTXOs(batch)
		if err != nil {
			return err
		}
		staged += len(batch)
		batch = batch[:0]
		return nil
	}
	err = reader.Iterate(func(outpoint *externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error {
		batch = append(batch, &externalapi.OutpointAndUTXOEntryPair{Outpoint: outpoint, UTXOEntry: entry})
		if len(batch) >= *batchSize {
			if err := flush(); err != nil {
				return err
			}
			fmt.Printf("  ... %d entries staged\n", staged)
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(err, "failed while streaming UTXO entries into the node (import staging "+
			"data will be cleared; the node's chain state has NOT been changed)")
	}
	if err := flush(); err != nil {
		return errors.Wrapf(err, "failed to stage the final batch (import staging data will be "+
			"cleared; the node's chain state has NOT been changed)")
	}
	fmt.Printf("Staged %d entries.\n", staged)

	fmt.Printf("Validating and inserting the imported pruning point (forces the virtual parent, marks " +
		"the block UTXO-valid, stages its multiset, and replaces the virtual UTXO set)...\n")
	err = cs.ValidateAndInsertImportedPruningPoint(blockHash)
	if err != nil {
		return errors.Wrapf(err, "failed to validate/insert the imported pruning point (import "+
			"staging data will be cleared; the node's chain state has NOT been changed)")
	}

	newVirtualSelectedParent, err := cs.GetVirtualSelectedParent()
	if err != nil {
		return err
	}
	fmt.Printf("\nRebaseline complete. Virtual selected parent is now %s.\n", newVirtualSelectedParent)
	fmt.Printf("NOTE: PruningPoint() still reports %s - this import intentionally does not touch the "+
		"pruning-point store's own \"current pruning point\" record, only virtual's parent/UTXO "+
		"baseline. It will catch up on its own as the node's ordinary pruning-point-advancement "+
		"logic runs during normal operation.\n", previousPruningPoint)
	fmt.Printf("Start htnd normally against this database directory to resync forward via normal " +
		"header/block IBD from peers.\n")
	return nil
}
