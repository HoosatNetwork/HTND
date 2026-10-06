// Command muhashjournal analyzes the MuHash journals htnd writes with --muhash-journal=<path>: a
// record per multiset computation, listing every element added to or removed from the block's UTXO
// multiset. See package muhashjournal for what is recorded and why.
//
// It only reads journal files, never a datadir.
//
// Subcommands:
//
//	summary <journal>              Records, how many reproduce their header, and the entry blocks -
//	                               where the parent matched its header and the block does not.
//	verify <journal>               Replay every record from its parent state and confirm it
//	                               reproduces the recorded result. Run this first: an analysis of a
//	                               journal that does not replay is meaningless.
//	show <journal> <block>         Every record for a block, with its ops decoded.
//	whatif <journal> [block]       For the given block, or every entry block, search for a single
//	                               change to its ops (a missing or doubled element, another DAA stamp,
//	                               a flipped coinbase flag) that reproduces its header commitment.
//	diff <journalA> <journalB>     Pair records of the same block across two nodes and list the
//	                               elements only one side hashed. Pairs by block hash, or by B's
//	                               result equalling A's header commitment, which lines a validating
//	                               node up with the miner's template for the block it mined.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/muhashjournal"
)

func main() {
	maxTrials := flag.Int("max-trials", 200000, "whatif: stop after this many trials per record (0 = unlimited)")
	maxPairs := flag.Int("max-pairs", 20, "diff: report at most this many differing pairs")
	maxOps := flag.Int("max-ops", 50, "diff/show: print at most this many ops per side (0 = all)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: muhashjournal [flags] summary|verify|show|whatif|diff <journal> [...]")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(2)
	}

	records, err := muhashjournal.Read(args[1])
	if err != nil {
		fail(err)
	}

	switch args[0] {
	case "summary":
		summary(records)
	case "verify":
		verify(records)
	case "show":
		if len(args) < 3 {
			fail(fmt.Errorf("show needs a block hash"))
		}
		show(records, args[2], *maxOps)
	case "whatif":
		block := ""
		if len(args) >= 3 {
			block = args[2]
		}
		whatIf(records, block, *maxTrials)
	case "diff":
		if len(args) < 3 {
			fail(fmt.Errorf("diff needs two journals"))
		}
		other, err := muhashjournal.Read(args[2])
		if err != nil {
			fail(err)
		}
		diff(records, other, *maxPairs, *maxOps)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "muhashjournal:", err)
	os.Exit(1)
}

func summary(records []*muhashjournal.Record) {
	kinds := map[string]int{}
	matching, mismatching, inherited := 0, 0, 0
	for _, record := range records {
		kinds[record.Kind]++
		if record.HeaderCommitment == "" {
			continue
		}
		if record.MatchesHeader() {
			matching++
		} else {
			mismatching++
			if record.ParentHeaderCommitment != "" && !record.ParentMatchesHeader() {
				inherited++
			}
		}
	}
	fmt.Printf("%d records: %d block, %d template, %d virtual\n", len(records), kinds[muhashjournal.KindBlock],
		kinds[muhashjournal.KindTemplate], kinds[muhashjournal.KindVirtual])
	fmt.Printf("blocks: %d reproduce their header, %d do not (%d of those have a parent that already did not)\n",
		matching, mismatching, inherited)
	entries := muhashjournal.EntryBlocks(records)
	fmt.Printf("%d entry blocks (parent matched its header, block does not):\n", len(entries))
	for _, record := range entries {
		fmt.Printf("  %s DAA %d: %d ops, merge set %d, header %s, result %s\n", record.Block, record.DAAScore,
			len(record.Ops), len(record.MergeSet), record.HeaderCommitment, record.ResultMultiset)
	}
}

func verify(records []*muhashjournal.Record) {
	failed := 0
	for _, record := range records {
		if err := record.Verify(); err != nil {
			failed++
			fmt.Printf("%s %s DAA %d: %s\n", record.Kind, record.Block, record.DAAScore, err)
		}
	}
	fmt.Printf("%d of %d records replay to their recorded result\n", len(records)-failed, len(records))
	if failed > 0 {
		os.Exit(1)
	}
}

func show(records []*muhashjournal.Record, block string, maxOps int) {
	found := false
	for _, record := range records {
		if record.Block != block {
			continue
		}
		found = true
		printRecord(record)
		printOps("ops", record.Ops, maxOps)
	}
	if !found {
		fail(fmt.Errorf("no record for block %s", block))
	}
}

func printRecord(record *muhashjournal.Record) {
	fmt.Printf("%s %s (run %s, %s)\n", record.Kind, record.Block, record.RunID, record.Time)
	fmt.Printf("  selected parent %s, merging DAA score %d\n", record.SelectedParent, record.DAAScore)
	fmt.Printf("  parent multiset %s, parent header %s (match %t)\n", record.ParentMultiset,
		record.ParentHeaderCommitment, record.ParentMatchesHeader())
	fmt.Printf("  result multiset %s, header %s (match %t)\n", record.ResultMultiset, record.HeaderCommitment,
		record.MatchesHeader())
	for i, block := range record.MergeSet {
		fmt.Printf("  merge set %d: %s DAA %d, %d/%d accepted\n", i, block.Hash, block.DAAScore, block.Accepted,
			block.Total)
	}
}

func printOps(label string, ops []muhashjournal.Op, maxOps int) {
	fmt.Printf("  %s (%d):\n", label, len(ops))
	for i, op := range ops {
		if maxOps != 0 && i == maxOps {
			fmt.Printf("    ... %d more\n", len(ops)-i)
			return
		}
		fmt.Printf("    %s\n", op.Describe())
	}
}

func whatIf(records []*muhashjournal.Record, block string, maxTrials int) {
	var targets []*muhashjournal.Record
	if block == "" {
		targets = muhashjournal.EntryBlocks(records)
		if len(targets) == 0 {
			fmt.Println("no entry blocks in this journal; name a block to test it anyway")
			return
		}
	} else {
		for _, record := range records {
			if record.Block == block && record.HeaderCommitment != "" {
				targets = append(targets, record)
			}
		}
		if len(targets) == 0 {
			fail(fmt.Errorf("no record with a header for block %s", block))
		}
	}
	for _, record := range targets {
		printRecord(record)
		if record.ParentHeaderCommitment != "" && !record.ParentMatchesHeader() {
			fmt.Println("  NOTE: the parent multiset already differs from the parent's header, so no change to " +
				"this block's ops alone can reach its header; look at an earlier block")
		}
		hits, trials, err := muhashjournal.WhatIf(record, record.HeaderCommitment, maxTrials)
		if err != nil {
			fmt.Printf("  whatif failed: %s\n", err)
			continue
		}
		if len(hits) == 0 {
			fmt.Printf("  no single change among %d trials reproduces the header\n", trials)
			continue
		}
		fmt.Printf("  %d of %d trials reproduce the header:\n", len(hits), trials)
		for _, hit := range hits {
			fmt.Printf("    %s\n", hit.Description)
			if hit.Op != nil {
				fmt.Printf("      %s\n", hit.Op.Describe())
			}
		}
	}
}

func diff(a, b []*muhashjournal.Record, maxPairs, maxOps int) {
	pairs := muhashjournal.Match(a, b)
	fmt.Printf("%d records paired (%d in A, %d in B)\n", len(pairs), len(a), len(b))
	reported, identical := 0, 0
	for _, pair := range pairs {
		difference := muhashjournal.Compare(pair)
		if difference.Empty() {
			identical++
			continue
		}
		if reported == maxPairs {
			continue
		}
		reported++
		fmt.Printf("\nA %s %s <-> B %s %s (matched by %s)\n", pair.A.Kind, pair.A.Block, pair.B.Kind, pair.B.Block,
			pair.How)
		fmt.Printf("  A: DAA %d, parent %s, result %s, header %s\n", pair.A.DAAScore, pair.A.ParentMultiset,
			pair.A.ResultMultiset, pair.A.HeaderCommitment)
		fmt.Printf("  B: DAA %d, parent %s, result %s, header %s\n", pair.B.DAAScore, pair.B.ParentMultiset,
			pair.B.ResultMultiset, pair.B.HeaderCommitment)
		if !difference.ParentsAgree {
			fmt.Println("  parents DIFFER: the two started from different multisets, so the difference is at or " +
				"before the selected parent; the op lists below are only this block's share of it")
		}
		printOps("only in A", difference.OnlyInA, maxOps)
		printOps("only in B", difference.OnlyInB, maxOps)
	}
	fmt.Printf("\n%d pairs identical, %d differ (%d reported)\n", identical, len(pairs)-identical, reported)
}
