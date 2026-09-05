// Command htnexodus generates, verifies, diffs and imports candidate "exodus pruning point"
// bundles: manually authored, community-vetted UTXO set checkpoints intended to serve as a
// trusted floor for HTND nodes whose own pruning-point UTXO set calculation cannot be trusted
// (see HoosatNetwork/HTND#21). It does not implement any consensus rule change, signing, or
// checkpoint shipping/embedding (see HoosatNetwork/HTND#20 for the rebaseline mechanics this
// tool's `import` command implements).
//
// Usage:
//
//	htnexodus create  --db-path <path> [--network mainnet] (--block <hash> | --daa-score <n>) --out <dir> [--note "..."]
//	htnexodus verify  --bundle <dir>
//	htnexodus diff    --bundle-a <dir> --bundle-b <dir>
//	htnexodus diff    --bundle-a <dir> --live --db-path <path> [--network mainnet]
//	htnexodus import  --bundle <dir> --db-path <path> [--network mainnet] --force
//
// `create`, `verify` and `diff` are read-only: `create` opens the node's own on-disk database
// directly (the node must not be running at the same time, since both processes would
// otherwise contend for the same database files) and walks the UTXO set as of the requested
// block, exactly like the node's own pruning point UTXO set calculation, but generalized to an
// arbitrary historical block.
//
// `import` is the one command that mutates the node's own consensus state: it forces virtual's
// selected parent to the bundle's block and replaces the virtual UTXO set with the bundle's
// contents, using the same consensus machinery real headers-proof IBD already uses to bootstrap
// a node's initial pruning point. It requires --force and, like `create`, requires the node
// process to be stopped first.
package main

import (
	"flag"
	"fmt"
	"os"
)

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  htnexodus create --db-path <path> [--network mainnet] (--block <hash> | --daa-score <n>) --out <dir> [--note "..."] [--chunk-size N]
  htnexodus verify --bundle <dir>
  htnexodus diff --bundle-a <dir> --bundle-b <dir> [--max-print N]
  htnexodus diff --bundle-a <dir> --live --db-path <path> [--network mainnet] [--max-print N]
  htnexodus import --bundle <dir> --db-path <path> [--network mainnet] [--db-type pebble] [--batch-size N] --force

Options:
  --db-path string     Path to the node's database directory (e.g. ~/.htnd/hoosat-mainnet/datadir2).
                        The node must be stopped before running this tool against its database.
  --network string     Network the database belongs to: mainnet, testnet, testnet-b5, testnet-b10,
                        simnet, devnet (default "mainnet")
  --db-type string     Database engine: pebble or leveldb (default "pebble")
  --block string        Target block hash (hex) to snapshot the UTXO set of
  --daa-score uint      Target block DAA score to snapshot the UTXO set of (resolved via the
                        selected parent chain; the node must have already synced past it)
  --out string          Directory to write the candidate bundle to (created if missing; a
                        previous, unfinished attempt at the same block is resumed automatically)
  --note string         Free-text operator note/identity to embed in the bundle (no signature)
  --chunk-size int      UTXO entries per chunk file (default 500000)
  --bundle string       Bundle directory to verify or import
  --bundle-a string     First bundle directory to diff
  --bundle-b string     Second bundle directory to diff
  --live                Diff --bundle-a against a live recomputation from --db-path/--network at
                        the bundle's own recorded block, instead of a second bundle
  --max-print int       Maximum number of differing/unique outpoints to print per category (default 20)
  --batch-size int      UTXO entries staged per import batch (default 500000)
  --force               Required by 'import': confirms you understand this overwrites the node's
                        virtual chain state and UTXO baseline
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "create":
		err = runCreate(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "diff":
		err = runDiff(os.Args[2:])
	case "import":
		err = runImport(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = usage
	return fs
}
