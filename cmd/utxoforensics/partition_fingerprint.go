package main

// Partitioned pruning-point UTXO set fingerprints: a small, shareable file that lets two operators find
// exactly which coins their pruning point UTXO sets disagree on without either of them shipping a
// datadir copy. A MuHash over the whole set only says "different"; hashing each txid-prefix partition
// separately says "different in these partitions", and dumping only those partitions names the coins.
//
//	-ppfingerprint out.json          (needs -db) write per-partition MuHash/count/sompi of the set
//	-ppfingerprint-compare a,b       (no -db) list the partitions two fingerprint files disagree on
//	-ppfingerprint-dump p1,p2,...    (needs -db) print every entry in those partitions, one per line
//
// All three use -src (bucket or imported) and -ppfingerprint-bits (default 12 = 4096 partitions).

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

var (
	ppFingerprintOut = flag.String("ppfingerprint", "", "write a partitioned fingerprint of the pruning "+
		"point UTXO set (-src) to this JSON file: per txid-prefix partition MuHash, entry count and sompi. "+
		"Small enough to share; compare two with -ppfingerprint-compare")
	ppFingerprintCompare = flag.String("ppfingerprint-compare", "", "\"a.json,b.json\": list the partitions "+
		"where two -ppfingerprint files disagree (no -db needed)")
	ppFingerprintDump = flag.String("ppfingerprint-dump", "", "comma-separated partition prefixes (hex, as "+
		"printed by -ppfingerprint-compare): print every entry of the pruning point UTXO set (-src) in them")
	ppFingerprintBits = flag.Int("ppfingerprint-bits", 12, "partition by this many leading bits of the "+
		"transaction ID (4..20)")
)

type partitionFingerprint struct {
	Prefix string `json:"prefix"`
	Count  uint64 `json:"count"`
	Sompi  uint64 `json:"sompi"`
	MuHash string `json:"muhash"`
}

type setFingerprint struct {
	PruningPoint     string                 `json:"pruningPoint"`
	HeaderCommitment string                 `json:"headerCommitment"`
	Source           string                 `json:"source"`
	Bits             int                    `json:"bits"`
	Entries          uint64                 `json:"entries"`
	Sompi            uint64                 `json:"sompi"`
	SetMuHash        string                 `json:"setMuHash"`
	Partitions       []partitionFingerprint `json:"partitions"`
}

func partitionOf(outpoint *externalapi.DomainOutpoint, bits int) uint32 {
	id := outpoint.TransactionID.ByteSlice()
	v := uint32(id[0])<<16 | uint32(id[1])<<8 | uint32(id[2])
	return v >> (24 - uint(bits))
}

func partitionName(p uint32, bits int) string {
	digits := (bits + 3) / 4
	return fmt.Sprintf("%0*x/%d", digits, p, bits)
}

func checkBits() int {
	if *ppFingerprintBits < 4 || *ppFingerprintBits > 20 {
		fmt.Fprintln(os.Stderr, "-ppfingerprint-bits must be within 4..20")
		os.Exit(2)
	}
	return *ppFingerprintBits
}

func writeSetFingerprint(s *stores, sa *model.StagingArea, outPath string) {
	bits := checkBits()
	fp := setFingerprint{Source: *srcA, Bits: bits}
	if pp, err := s.pruning.PruningPoint(s.db, sa); err == nil {
		fp.PruningPoint = pp.String()
		if header, err := s.headers.BlockHeader(s.db, sa, pp); err == nil {
			fp.HeaderCommitment = header.UTXOCommitment().String()
		}
	}
	n := 1 << uint(bits)
	sets := make([]model.Multiset, n)
	counts := make([]uint64, n)
	sompi := make([]uint64, n)
	whole := multiset.New()
	iterator, err := utxoSetIterator(s, *srcA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "iterator: %v\n", err)
		return
	}
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Fprintf(os.Stderr, "iterator.Get: %v\n", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serialize %s:%d: %v\n", &outpoint.TransactionID, outpoint.Index, err)
			return
		}
		p := partitionOf(outpoint, bits)
		if sets[p] == nil {
			sets[p] = multiset.New()
		}
		sets[p].Add(serialized)
		whole.Add(serialized)
		counts[p]++
		sompi[p] += entry.Amount()
		fp.Entries++
		fp.Sompi += entry.Amount()
	}
	fp.SetMuHash = whole.Hash().String()
	for p := 0; p < n; p++ {
		part := partitionFingerprint{Prefix: partitionName(uint32(p), bits), Count: counts[p], Sompi: sompi[p]}
		if sets[p] != nil {
			part.MuHash = sets[p].Hash().String()
		}
		fp.Partitions = append(fp.Partitions, part)
	}
	encoded, err := json.MarshalIndent(fp, "", " ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", outPath, err)
		return
	}
	fmt.Printf("\n=== partitioned fingerprint of the pruning point UTXO set (%s)\n  pruning point %s, header "+
		"commitment %s\n  %d entries, %d sompi, set MuHash %s (matches header: %t)\n  %d partitions written to %s\n",
		fp.Source, fp.PruningPoint, fp.HeaderCommitment, fp.Entries, fp.Sompi, fp.SetMuHash,
		fp.SetMuHash == fp.HeaderCommitment, n, outPath)
}

func readSetFingerprint(path string) (*setFingerprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fp := &setFingerprint{}
	return fp, json.Unmarshal(data, fp)
}

func compareSetFingerprints(spec string) {
	paths := strings.Split(spec, ",")
	if len(paths) != 2 {
		fmt.Fprintln(os.Stderr, "-ppfingerprint-compare wants \"a.json,b.json\"")
		os.Exit(2)
	}
	a, err := readSetFingerprint(strings.TrimSpace(paths[0]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", paths[0], err)
		os.Exit(1)
	}
	b, err := readSetFingerprint(strings.TrimSpace(paths[1]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", paths[1], err)
		os.Exit(1)
	}
	fmt.Printf("A: pruning point %s (%s) %d entries %d sompi set %s header %s\n", a.PruningPoint, a.Source,
		a.Entries, a.Sompi, a.SetMuHash, a.HeaderCommitment)
	fmt.Printf("B: pruning point %s (%s) %d entries %d sompi set %s header %s\n", b.PruningPoint, b.Source,
		b.Entries, b.Sompi, b.SetMuHash, b.HeaderCommitment)
	if a.PruningPoint != b.PruningPoint {
		fmt.Println("DIFFERENT pruning points: the sets are not comparable; take both at the same pruning point")
		return
	}
	if a.Bits != b.Bits || len(a.Partitions) != len(b.Partitions) {
		fmt.Println("the files use different -ppfingerprint-bits; regenerate one of them")
		return
	}
	var differing []string
	for i := range a.Partitions {
		pa, pb := a.Partitions[i], b.Partitions[i]
		if pa.MuHash == pb.MuHash {
			continue
		}
		differing = append(differing, pa.Prefix)
		fmt.Printf("  %s: A %d entries %d sompi | B %d entries %d sompi (entries %+d, sompi %+d)\n", pa.Prefix,
			pa.Count, pa.Sompi, pb.Count, pb.Sompi, int64(pb.Count)-int64(pa.Count), int64(pb.Sompi)-int64(pa.Sompi))
	}
	sort.Strings(differing)
	fmt.Printf("%d of %d partitions differ\n", len(differing), len(a.Partitions))
	if len(differing) > 0 {
		prefixes := make([]string, len(differing))
		for i, d := range differing {
			prefixes[i] = strings.Split(d, "/")[0]
		}
		fmt.Printf("dump them on both nodes with: -ppfingerprint-bits %d -ppfingerprint-dump %s\n", a.Bits,
			strings.Join(prefixes, ","))
	}
}

func dumpPartitions(s *stores, spec string) {
	bits := checkBits()
	wanted := map[uint32]bool{}
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(strings.Split(raw, "/")[0])
		if raw == "" {
			continue
		}
		var p uint32
		if _, err := fmt.Sscanf(raw, "%x", &p); err != nil {
			fmt.Fprintf(os.Stderr, "bad partition %q: %v\n", raw, err)
			os.Exit(2)
		}
		wanted[p] = true
	}
	iterator, err := utxoSetIterator(s, *srcA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "iterator: %v\n", err)
		return
	}
	defer iterator.Close()
	var lines []string
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			fmt.Fprintf(os.Stderr, "iterator.Get: %v\n", err)
			return
		}
		if !wanted[partitionOf(outpoint, bits)] {
			continue
		}
		script := entry.ScriptPublicKey()
		lines = append(lines, fmt.Sprintf("%s:%d\tamount=%d\tdaa=%d\tcoinbase=%t\tscript=v%d:%s",
			&outpoint.TransactionID, outpoint.Index, entry.Amount(), entry.BlockDAAScore(), entry.IsCoinbase(),
			script.Version, hex.EncodeToString(script.Script)))
	}
	sort.Strings(lines)
	for _, line := range lines {
		fmt.Println(line)
	}
	fmt.Fprintf(os.Stderr, "%d entries in %d partitions\n", len(lines), len(wanted))
}
