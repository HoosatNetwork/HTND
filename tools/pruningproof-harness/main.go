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

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
	prefixpkg "github.com/Hoosat-Oy/HTND/domain/prefixmanager/prefix"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database/pebble"
)

type LevelInfo struct {
	Level              int            `json:"level"`
	HeadersCount       int            `json:"headers_count"`
	First              string         `json:"first,omitempty"`
	Last               string         `json:"last,omitempty"`
	BlueScoreMin       uint64         `json:"blue_score_min"`
	BlueScoreMax       uint64         `json:"blue_score_max"`
	BlueScoreAvg       float64        `json:"blue_score_avg"`
	BlueScoreHistogram map[string]int `json:"blue_score_histogram,omitempty"`
	HeaderHashes       []string       `json:"header_hashes,omitempty"`
}

type ProofReport struct {
	Timestamp       time.Time   `json:"timestamp"`
	TotalHeaders    int         `json:"total_headers"`
	Levels          []LevelInfo `json:"levels"`
	DurationSeconds float64     `json:"duration_seconds"`
}

func main() {
	dbPath := flag.String("db", "", "path to LevelDB database (required)")
	cacheSize := flag.Int("cache", 256, "LevelDB cache size in MiB")
	format := flag.String("format", "text", "output format: text|json")
	out := flag.String("out", "", "output file path (for json); default stdout")
	limit := flag.Int("limit", 0, "max number of header hashes to include per level (0 = all)")
	buckets := flag.Int("buckets", 10, "number of buckets for blue-score histogram (0 = disabled)")
	verbose := flag.Bool("verbose", false, "print per-header details in text mode")
	statsOnly := flag.Bool("stats-only", false, "only print summary statistics")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: pruningproof-harness -db <path> [-cache N] [-format json] [-out file.json]")
		os.Exit(2)
	}

	start := time.Now()
	db, err := pebble.NewPebbleDB(*dbPath, *cacheSize)
	if err != nil {
		log.Fatalf("failed to open LevelDB at %s: %v", *dbPath, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("warning: failed to close DB: %v", err)
		}
	}()

	cfg := &consensus.Config{Params: dagconfig.MainnetParams}
	f := consensus.NewFactory()

	// Use an empty prefix (same as tests)
	p := &prefixpkg.Prefix{}

	c, shouldMigrate, err := f.NewConsensus(cfg, db, p, nil)
	if err != nil {
		log.Fatalf("NewConsensus failed: %v", err)
	}
	if shouldMigrate {
		log.Fatalf("database requires migration; cannot run harness against this DB")
	}

	proof, err := c.BuildPruningPointProof()
	if err != nil {
		log.Fatalf("BuildPruningPointProof failed: %v", err)
	}

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
					for i := 0; i < b; i++ {
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
						idx := int(math.Floor((float64(bs) - float64(minBlue)) / width))
						if idx < 0 {
							idx = 0
						}
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
			m := *limit
			if m < 0 {
				m = 0
			}
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
		}
		if n > 0 {
			li.BlueScoreAvg = float64(sumBlue) / float64(n)
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
