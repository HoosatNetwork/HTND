package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	externalapi "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/prefixmanager"
	prefixpkg "github.com/HoosatNetwork/HTND/domain/prefixmanager/prefix"
	dbpkg "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/pebble"
	infralogger "github.com/HoosatNetwork/HTND/infrastructure/logger"
)

type LevelInfo struct {
	Level                            int            `json:"level"`
	HeadersCount                     int            `json:"headers_count"`
	First                            string         `json:"first,omitempty"`
	Last                             string         `json:"last,omitempty"`
	BlueScoreMin                     uint64         `json:"blue_score_min"`
	BlueScoreMax                     uint64         `json:"blue_score_max"`
	BlueScoreAvg                     float64        `json:"blue_score_avg"`
	BlueScoreHistogram               map[string]int `json:"blue_score_histogram,omitempty"`
	HeaderHashes                     []string       `json:"header_hashes,omitempty"`
	DynamicKHistogram                map[string]int `json:"dynamic_k_histogram,omitempty"`
	SelectedParentBlueScoreHistogram map[string]int `json:"selected_parent_blue_score_delta_histogram,omitempty"`
	SelectedParentMissing            int            `json:"selected_parent_missing,omitempty"`
	SelectedParentMissingHashes      []string       `json:"selected_parent_missing_hashes,omitempty"`
}

type ProofReport struct {
	Timestamp       time.Time   `json:"timestamp"`
	TotalHeaders    int         `json:"total_headers"`
	Levels          []LevelInfo `json:"levels"`
	DurationSeconds float64     `json:"duration_seconds"`
}

func main() {
	// bucketDelta buckets a uint64 delta into human-friendly ranges.
	bucketDelta := func(delta uint64) string {
		switch {
		case delta == 0:
			return "0"
		case delta <= 4:
			return "1-4"
		case delta <= 9:
			return "5-9"
		case delta <= 99:
			return "10-99"
		case delta <= 999:
			return "100-999"
		case delta <= 9999:
			return "1000-9999"
		case delta <= 99999:
			return "10000-99999"
		default:
			return ">=100000"
		}
	}
	dbPath := flag.String("db", "", "path to LevelDB database (required)")
	cacheSize := flag.Int("cache", 256, "LevelDB cache size in MiB")
	netName := flag.String("net", "auto", "network: auto|mainnet|testnet")
	format := flag.String("format", "text", "output format: text|json")
	out := flag.String("out", "", "output file path (for json); default stdout")
	limit := flag.Int("limit", 0, "max number of header hashes to include per level (0 = all)")
	buckets := flag.Int("buckets", 10, "number of buckets for blue-score histogram (0 = disabled)")
	verbose := flag.Bool("verbose", false, "print per-header details in text mode")
	ghostdag := flag.Bool("ghostdag", true, "collect GHOSTDAG dynamic-k stats per header")
	statsOnly := flag.Bool("stats-only", false, "only print summary statistics")
	inspectHash := flag.String("inspect-hash", "", "hex header hash to inspect (debug)")
	inspectOnly := flag.Bool("inspect-only", false, "inspect given hash and exit without building proof")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: pruningproof-harness -db <path> [-cache N] [-format json] [-out file.json]")
		os.Exit(2)
	}

	start := time.Now()
	// Initialize infrastructure logger so subsystem loggers (e.g., consensus)
	// print to stdout when running this harness.
	infralogger.InitLogStdout(infralogger.LevelInfo)
	db, err := pebble.NewPebbleDB(*dbPath, *cacheSize)
	if err != nil {
		log.Fatalf("failed to open LevelDB at %s: %v", *dbPath, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("warning: failed to close DB: %v", err)
		}
	}()

	// Detect active prefix stored in DB (if any) so we use the same prefix as the running node
	activePrefix, exists, err := prefixmanager.ActivePrefix(db)
	if err != nil {
		log.Fatalf("failed to read active prefix from DB: %v", err)
	}
	var p *prefixpkg.Prefix
	if exists {
		p = activePrefix
	} else {
		// No active prefix set: use empty prefix (like domain.New)
		p = &prefixpkg.Prefix{}
		if err := prefixmanager.SetPrefixAsActive(db, p); err != nil {
			log.Fatalf("failed to set active prefix: %v", err)
		}
	}

	// Auto-detect network by checking for the genesis header under the active prefix
	var params dagconfig.Params
	switch *netName {
	case "mainnet":
		params = dagconfig.MainnetParams
	case "testnet":
		params = dagconfig.TestnetParams
	case "auto":
		// Try testnet first (more specific), then mainnet. If detection fails, default to mainnet.
		prefixBucket := dbpkg.MakeBucket(p.Serialize())
		blockHeaderBucket := prefixBucket.Bucket([]byte("block-headers"))
		if dagconfig.TestnetParams.GenesisHash != nil {
			if has, _ := db.Has(blockHeaderBucket.Key(dagconfig.TestnetParams.GenesisHash.ByteSlice())); has {
				params = dagconfig.TestnetParams
				log.Printf("Auto-detected network: testnet")
				break
			}
		}
		if dagconfig.MainnetParams.GenesisHash != nil {
			if has, _ := db.Has(blockHeaderBucket.Key(dagconfig.MainnetParams.GenesisHash.ByteSlice())); has {
				params = dagconfig.MainnetParams
				log.Printf("Auto-detected network: mainnet")
				break
			}
		}
		// Fallback
		params = dagconfig.MainnetParams
		log.Printf("Auto-detect failed; defaulting to mainnet")
	default:
		log.Fatalf("unknown network %s; supported: auto,mainnet,testnet", *netName)
	}

	cfg := &consensus.Config{Params: params}
	f := consensus.NewFactory()

	c, shouldMigrate, err := f.NewConsensus(cfg, db, p, nil)
	if err != nil {
		log.Fatalf("NewConsensus failed: %v", err)
	}
	if shouldMigrate {
		log.Fatalf("database requires migration; cannot run harness against this DB")
	}

	// Print current pruning point
	pruningPoint, err := c.PruningPoint()
	if err != nil {
		log.Printf("PruningPoint().error: %v", err)
	} else if pruningPoint == nil {
		log.Printf("PruningPoint() returned nil")
	} else {
		log.Printf("Current pruning point: %s", pruningPoint.String())
	}

	// Show pruning point history (headers)
	ppHeaders, err := c.PruningPointHeaders()
	if err != nil {
		log.Printf("PruningPointHeaders().error: %v", err)
	} else {
		log.Printf("PruningPointHeaders: count=%d", len(ppHeaders))
		if len(ppHeaders) > 0 {
			first := consensushashing.HeaderHash(ppHeaders[0])
			last := consensushashing.HeaderHash(ppHeaders[len(ppHeaders)-1])
			log.Printf("  first=%s last=%s", first, last)
			for i, h := range ppHeaders {
				lvl := h.BlockLevel(cfg.MaxBlockLevel)
				parents := len(h.Parents())
				log.Printf("  header[%d]: hash=%s level=%d parents=%d blueScore=%d", i, consensushashing.HeaderHash(h), lvl, parents, h.BlueScore())
			}
		}
	}
	// If requested, inspect a specific header hash and optionally exit before building the full proof.
	if *inspectHash != "" {
		h, err := externalapi.NewDomainHashFromString(*inspectHash)
		if err != nil {
			log.Fatalf("invalid inspect-hash %s: %v", *inspectHash, err)
		}
		bi, err := c.GetBlockInfo(h)
		if err != nil {
			log.Printf("GetBlockInfo(%s) error: %v", h.String(), err)
		} else {
			log.Printf("GetBlockInfo(%s): Exists=%v SelectedParent=%v BlueScore=%d DynamicK=%d", h.String(), bi.Exists, bi.SelectedParent, bi.BlueScore, bi.DynamicK)
		}
		gd, err := c.TrustedGHOSTDAGData(h)
		if err != nil {
			log.Printf("TrustedGHOSTDAGData(%s) error: %v", h.String(), err)
		} else if gd == nil {
			log.Printf("TrustedGHOSTDAGData(%s): nil", h.String())
		} else {
			sp := gd.SelectedParent()
			spStr := "<nil>"
			if sp != nil {
				spStr = sp.String()
			}
			log.Printf("TrustedGHOSTDAGData(%s): DynamicK=%d SelectedParent=%s BlueScore=%d", h.String(), gd.DynamicK(), spStr, gd.BlueScore())
		}

		// Inspect selected-parent info if available
		if bi != nil && bi.Exists && bi.SelectedParent != nil {
			sp := bi.SelectedParent
			spInfo, err := c.GetBlockInfo(sp)
			if err != nil {
				log.Printf("GetBlockInfo(selectedParent %s) error: %v", sp.String(), err)
			} else {
				log.Printf("GetBlockInfo(selectedParent %s): Exists=%v SelectedParent=%v BlueScore=%d DynamicK=%d", sp.String(), spInfo.Exists, spInfo.SelectedParent, spInfo.BlueScore, spInfo.DynamicK)
			}
			spgd, err := c.TrustedGHOSTDAGData(sp)
			if err != nil {
				log.Printf("TrustedGHOSTDAGData(selectedParent %s) error: %v", sp.String(), err)
			} else if spgd == nil {
				log.Printf("TrustedGHOSTDAGData(selectedParent %s): nil", sp.String())
			} else {
				log.Printf("TrustedGHOSTDAGData(selectedParent %s): DynamicK=%d BlueScore=%d", sp.String(), spgd.DynamicK(), spgd.BlueScore())
			}
		}

		if *inspectOnly {
			return
		}
	}

	log.Printf("Start building pruning point proof")
	proof, err := c.BuildPruningPointProof()
	if err != nil {
		log.Fatalf("BuildPruningPointProof failed: %v", err)
	}
	log.Printf("Build the pruning point proof")

	report := ProofReport{
		Timestamp:    time.Now(),
		TotalHeaders: 0,
		Levels:       make([]LevelInfo, 0, len(proof.Headers)),
	}

	for level, headers := range proof.Headers {
		n := len(headers)
		report.TotalHeaders += n

		var firstHash, lastHash string
		if n > 0 {
			firstHash = consensushashing.HeaderHash(headers[0]).String()
			lastHash = consensushashing.HeaderHash(headers[n-1]).String()
		}

		var sumBlue uint64
		var minBlue uint64
		var maxBlue uint64
		hist := map[string]int{}

		if n > 0 {
			for i, h := range headers {
				bs := h.BlueScore()
				sumBlue += bs
				if i == 0 || bs < minBlue {
					minBlue = bs
				}
				if i == 0 || bs > maxBlue {
					maxBlue = bs
				}
			}

			if *buckets > 0 && maxBlue >= minBlue {
				if maxBlue == minBlue {
					key := fmt.Sprintf("%d", minBlue)
					hist[key] = n
				} else {
					b := *buckets
					width := float64(maxBlue-minBlue) / float64(b)
					for i := range b {
						lo := uint64(math.Floor(float64(minBlue) + float64(i)*width))
						hi := uint64(math.Floor(float64(minBlue) + float64(i+1)*width))
						if i == b-1 {
							hi = maxBlue
						}
						label := fmt.Sprintf("%d-%d", lo, hi)
						hist[label] = 0
					}
					for _, h := range headers {
						bs := h.BlueScore()
						idx := max(int(math.Floor((float64(bs)-float64(minBlue))/width)), 0)
						if idx >= b {
							idx = b - 1
						}
						lo := uint64(math.Floor(float64(minBlue) + float64(idx)*width))
						hi := uint64(math.Floor(float64(minBlue) + float64(idx+1)*width))
						if idx == b-1 {
							hi = maxBlue
						}
						label := fmt.Sprintf("%d-%d", lo, hi)
						hist[label]++
					}
				}
			}
		}

		var headerHashes []string
		if *limit != 0 && n > 0 {
			m := max(*limit, 0)
			if m > n {
				m = n
			}
			for i := 0; i < m; i++ {
				headerHashes = append(headerHashes, consensushashing.HeaderHash(headers[i]).String())
			}
		} else if *limit == 0 && n > 0 {
			for _, h := range headers {
				headerHashes = append(headerHashes, consensushashing.HeaderHash(h).String())
			}
		}

		li := LevelInfo{
			Level:              level,
			HeadersCount:       n,
			First:              firstHash,
			Last:               lastHash,
			BlueScoreMin:       minBlue,
			BlueScoreMax:       maxBlue,
			BlueScoreAvg:       0.0,
			BlueScoreHistogram: hist,
			HeaderHashes:       headerHashes,
			DynamicKHistogram:  nil,
		}
		if n > 0 {
			li.BlueScoreAvg = float64(sumBlue) / float64(n)
		}

		// Collect GHOSTDAG dynamic-K histogram and selected-parent blue-score delta per level if requested
		if *ghostdag && n > 0 {
			dkHist := map[string]int{}
			spHist := map[string]int{}
			missing := 0
			missingSP := 0
			missingSPHashes := make([]string, 0)
			maxSamples := 50
			for _, h := range headers {
				hash := consensushashing.HeaderHash(h)

				// Dynamic-K: try trusted GHOSTDAG data first, then fallback to BlockInfo
				var dkKey string
				gd, err := c.TrustedGHOSTDAGData(hash)
				if err == nil && gd != nil {
					dkKey = fmt.Sprintf("%d", gd.DynamicK())
				} else {
					bi, err2 := c.GetBlockInfo(hash)
					if err2 == nil && bi != nil && bi.Exists {
						dkKey = fmt.Sprintf("%d", bi.DynamicK)
					} else {
						missing++
					}
				}
				if dkKey != "" {
					dkHist[dkKey]++
				}

				// Selected-parent blue-score delta (bucketed)
				bi, err := c.GetBlockInfo(hash)
				if err == nil && bi != nil && bi.Exists && bi.SelectedParent != nil && !bi.SelectedParent.Equal(model.VirtualGenesisBlockHash) {
					spInfo, err2 := c.GetBlockInfo(bi.SelectedParent)
					if err2 == nil && spInfo != nil && spInfo.Exists {
						var delta uint64
						if bi.BlueScore >= spInfo.BlueScore {
							delta = bi.BlueScore - spInfo.BlueScore
						} else {
							delta = 0
						}
						label := bucketDelta(delta)
						spHist[label]++
					} else {
						missingSP++
						if len(missingSPHashes) < maxSamples {
							missingSPHashes = append(missingSPHashes, hash.String())
						}
					}
				} else {
					missingSP++
					if len(missingSPHashes) < maxSamples {
						missingSPHashes = append(missingSPHashes, hash.String())
					}
				}
			}
			if len(dkHist) > 0 {
				li.DynamicKHistogram = dkHist
			}
			if len(spHist) > 0 {
				li.SelectedParentBlueScoreHistogram = spHist
			}
			if missing > 0 {
				log.Printf("Level %d: missing %d trusted GHOSTDAG entries", level, missing)
			}
			if missingSP > 0 {
				li.SelectedParentMissing = missingSP
				if len(missingSPHashes) > 0 {
					li.SelectedParentMissingHashes = missingSPHashes
				}
				// log.Printf("Level %d: selected-parent missing %d entries", level, missingSP)
			}
		}
		report.Levels = append(report.Levels, li)

		if *format == "text" && !*statsOnly {
			fmt.Printf("Level %d: headers=%d\n", level, n)
			if n == 0 {
				continue
			}
			fmt.Printf("  first=%s last=%s\n", firstHash, lastHash)
			fmt.Printf("  blueScore: min=%d max=%d avg=%.2f\n", minBlue, maxBlue, li.BlueScoreAvg)
			if *buckets > 0 && len(hist) > 0 {
				keys := make([]string, 0, len(hist))
				for k := range hist {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fmt.Printf("  blueScore histogram:\n")
				for _, k := range keys {
					fmt.Printf("    %s: %d\n", k, hist[k])
				}
			}
			if *ghostdag && li.DynamicKHistogram != nil && len(li.DynamicKHistogram) > 0 {
				keys := make([]string, 0, len(li.DynamicKHistogram))
				for k := range li.DynamicKHistogram {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fmt.Printf("  dynamicK histogram:\n")
				for _, k := range keys {
					fmt.Printf("    %s: %d\n", k, li.DynamicKHistogram[k])
				}
			}
			if *verbose {
				fmt.Printf("  headers:\n")
				for i, hh := range headerHashes {
					fmt.Printf("    %d: %s\n", i, hh)
				}
			}
		}
	}

	elapsed := time.Since(start).Seconds()
	report.DurationSeconds = elapsed

	if *format == "json" {
		outBytes, _ := json.MarshalIndent(report, "", "  ")
		if *out == "" {
			fmt.Println(string(outBytes))
		} else {
			if err := os.WriteFile(*out, outBytes, 0644); err != nil {
				log.Fatalf("failed to write output file: %v", err)
			}
			fmt.Printf("Wrote JSON report to %s\n", *out)
		}
	} else {
		fmt.Printf("Total headers in proof: %d\n", report.TotalHeaders)
		fmt.Printf("Levels: %d duration=%.2fs\n", len(report.Levels), report.DurationSeconds)
	}
}
