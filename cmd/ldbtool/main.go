package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n  ldbtool fuse [options] <dest> <src1> [src2 ...]\n  ldbtool copy [options] <src> <dest>\n\nOptions:\n  -strategy string     Conflict strategy: overwrite | keep (default overwrite)\n  -batch int           Batch size (number of keys) for writes (default 1000)\n  -batch-bytes int     Max batch size in MiB, flush when exceeded (default 8)\n  -pipeline int        Pipeline depth (batches queued for async writes), default 2 (ignored; direct writes)\n  -cache int           Cache size MiB for DBs; 0 uses defaults from Options() (default 0)\n  -compact             Compact destination after operation\n  -fresh               Remove destination directory before copy (dangerous)\n  -no-compact          Disable compaction assistance (preflight/manual) [default true for copy]\n  -l0-trigger int      Override LevelDB CompactionL0Trigger (default 300000 for copy)\n  -l0-slowdown int     Override LevelDB WriteL0SlowdownTrigger (default 400000 for copy)\n  -l0-pause int        Override LevelDB WriteL0PauseTrigger (default 500000 for copy)\n\nExamples:\n  ldbtool fuse -strategy overwrite -batch 100000 -batch-bytes 32 -cache 0 /dest /src1 /src2\n  ldbtool copy /src /dest\n  ldbtool copy -no-compact=false -l0-pause 200000 -l0-slowdown 150000 -l0-trigger 100000 -batch 10000 /src /dest\n\n")
}

func main() {
	// Initialize logging to stdout at INFO level so progress and status are visible in terminal.
	// This makes ldbtool "fuse" visibly report progress using the ldb package logs.
	logger.InitLogStdout(logger.LevelInfo)
	logger.SetLogLevels(logger.LevelInfo)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "fuse":
		fs := flag.NewFlagSet("fuse", flag.ExitOnError)
		var strategyStr string
		var batch int
		var batchBytesMiB int
		var cache int
		var pipeline int
		var compact bool
		fs.StringVar(&strategyStr, "strategy", "overwrite", "Conflict strategy: overwrite | keep")
		fs.IntVar(&batch, "batch", 1000, "Batch size for writes")
		fs.IntVar(&batchBytesMiB, "batch-bytes", 8, "Max batch size in MiB before flush")
		fs.IntVar(&cache, "cache", 0, "Cache size (MiB) for DBs; 0 uses defaults from Options()")
		fs.IntVar(&pipeline, "pipeline", 2, "Pipeline depth (batches queued for async writes)")
		fs.BoolVar(&compact, "compact", false, "Compact destination after fuse")

		if err := fs.Parse(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		args := fs.Args()
		if len(args) < 2 {
			usage()
			os.Exit(2)
		}
		dest := args[0]
		srcs := args[1:]

		// Provide immediate user feedback that the operation has started.
		fmt.Fprintf(os.Stderr, "Starting fuse into '%s' from %d source DB(s)...\n", dest, len(srcs))

		var strategy ldb.ConflictStrategy
		switch strings.ToLower(strategyStr) {
		case "overwrite", "over", "o":
			strategy = ldb.Overwrite
		case "keep", "keep-existing", "k":
			strategy = ldb.KeepExisting
		default:
			fmt.Fprintf(os.Stderr, "unknown strategy: %s\n", strategyStr)
			os.Exit(2)
		}

		opts := ldb.FuseOptions{CacheSizeMiB: cache, BatchSize: batch, MaxBatchBytes: batchBytesMiB * 1024 * 1024, Strategy: strategy, CompactAfter: compact, PipelineDepth: pipeline,
			// Preserve previous default behavior: enable compaction assistance for fuse
			CompactOnStall: true}
		if err := ldb.FuseLevelDB(dest, srcs, opts); err != nil {
			fmt.Fprintf(os.Stderr, "fuse failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Fuse completed successfully.")
	case "copy":
		fs := flag.NewFlagSet("copy", flag.ExitOnError)
		var batch int
		var batchBytesMiB int
		var cache int
		var pipeline int
		var compact bool
		var fresh bool
		var noCompact bool
		var l0Trigger int
		var l0Slowdown int
		var l0Pause int
		// For copy, strategy doesn't really matter since there is only one source, but keep for consistency.
		var strategyStr string
		fs.StringVar(&strategyStr, "strategy", "overwrite", "Conflict strategy: overwrite | keep (non-effect for single source unless dest already has keys)")
		fs.IntVar(&batch, "batch", 1000, "Batch size for writes")
		fs.IntVar(&batchBytesMiB, "batch-bytes", 8, "Max batch size in MiB before flush")
		fs.IntVar(&cache, "cache", 0, "Cache size (MiB) for DBs; 0 uses defaults from Options()")
		fs.IntVar(&pipeline, "pipeline", 2, "Pipeline depth (batches queued for async writes)")
		fs.BoolVar(&compact, "compact", false, "Compact destination after copy")
		fs.BoolVar(&fresh, "fresh", false, "If destination exists, remove it first (DANGEROUS). Guarantees clean copy without pre-existing compaction backlog.")
		fs.BoolVar(&noCompact, "no-compact", true, "Disable any automatic compaction/throttling assistance. If destination has many L0 files, copy may stall. Prefer --fresh instead.")
		fs.IntVar(&l0Trigger, "l0-trigger", 300000, "Override LevelDB CompactionL0Trigger (start compaction)")
		fs.IntVar(&l0Slowdown, "l0-slowdown", 400000, "Override LevelDB WriteL0SlowdownTrigger (soft backpressure)")
		fs.IntVar(&l0Pause, "l0-pause", 500000, "Override LevelDB WriteL0PauseTrigger (hard pause)")

		if err := fs.Parse(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		args := fs.Args()
		if len(args) != 2 {
			usage()
			os.Exit(2)
		}
		src := args[0]
		dest := args[1]

		var strategy ldb.ConflictStrategy
		switch strings.ToLower(strategyStr) {
		case "overwrite", "over", "o":
			strategy = ldb.Overwrite
		case "keep", "keep-existing", "k":
			strategy = ldb.KeepExisting
		default:
			fmt.Fprintf(os.Stderr, "unknown strategy: %s\n", strategyStr)
			os.Exit(2)
		}

		if fresh {
			// Danger zone: remove existing destination directory entirely
			if err := os.RemoveAll(dest); err != nil {
				fmt.Fprintf(os.Stderr, "failed to remove destination '%s': %v\n", dest, err)
				os.Exit(1)
			}
		}

		// Apply runtime LevelDB thresholds via environment so NewLevelDB picks them up.
		if l0Trigger > 0 {
			_ = os.Setenv("KSDB_COMPACTION_L0_TRIGGER", fmt.Sprint(l0Trigger))
		}
		if l0Slowdown > 0 {
			_ = os.Setenv("KSDB_WRITE_L0_SLOWDOWN", fmt.Sprint(l0Slowdown))
		}
		if l0Pause > 0 {
			_ = os.Setenv("KSDB_WRITE_L0_PAUSE", fmt.Sprint(l0Pause))
		}
		// no-compact defaults already accompanied by high thresholds; nothing else to do here

		// Echo effective settings for transparency
		fmt.Fprintf(os.Stderr, "Starting copy from '%s' to '%s'...\n", src, dest)
		fmt.Fprintf(os.Stderr, "Compaction assistance: %v\n", !noCompact)
		if v := os.Getenv("KSDB_COMPACTION_L0_TRIGGER"); v != "" {
			fmt.Fprintf(os.Stderr, "L0 trigger: %s\n", v)
		}
		if v := os.Getenv("KSDB_WRITE_L0_SLOWDOWN"); v != "" {
			fmt.Fprintf(os.Stderr, "L0 slowdown: %s\n", v)
		}
		if v := os.Getenv("KSDB_WRITE_L0_PAUSE"); v != "" {
			fmt.Fprintf(os.Stderr, "L0 pause: %s\n", v)
		}
		opts := ldb.FuseOptions{CacheSizeMiB: cache, BatchSize: batch, MaxBatchBytes: batchBytesMiB * 1024 * 1024, Strategy: strategy, CompactAfter: compact, PipelineDepth: pipeline,
			// Explicitly set compaction assistance based on user flag
			CompactOnStall: !noCompact}
		if err := ldb.CopyLevelDB(src, dest, opts); err != nil {
			fmt.Fprintf(os.Stderr, "copy failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Copy completed successfully.")
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}
