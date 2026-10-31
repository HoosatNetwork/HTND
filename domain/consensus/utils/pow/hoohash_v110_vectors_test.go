package pow_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/pow"
)

// hoohashVector is a mainnet block header and its expected proof-of-work value.
type hoohashVector struct {
	Hash                 string     `json:"hash"`
	PoW                  string     `json:"pow"` // big-endian hex of the value compared against the target
	Version              uint16     `json:"version"`
	Parents              [][]string `json:"parents"`
	HashMerkleRoot       string     `json:"hashMerkleRoot"`
	AcceptedIDMerkleRoot string     `json:"acceptedIdMerkleRoot"`
	UTXOCommitment       string     `json:"utxoCommitment"`
	Timestamp            int64      `json:"timestamp"`
	Bits                 uint32     `json:"bits"`
	Nonce                uint64     `json:"nonce"`
	DAAScore             uint64     `json:"daaScore"`
	BlueScore            uint64     `json:"blueScore"`
	BlueWork             string     `json:"blueWork"`
	PruningPoint         string     `json:"pruningPoint"`
}

// TestHoohashV110MainnetVectors checks HoohashV110 against consecutive mainnet blocks. The expected
// values were computed with GOARCH=amd64 (GOAMD64=v1), whose results the network accepts. Every
// platform must reproduce them bit for bit, otherwise it disagrees with the network on PoW validity.
func TestHoohashV110MainnetVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/hoohash_v110_mainnet_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []hoohashVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	hash := func(s string) *externalapi.DomainHash {
		h, err := externalapi.NewDomainHashFromString(s)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	for i, v := range vectors {
		parents := make([]externalapi.BlockLevelParents, len(v.Parents))
		for level, hashes := range v.Parents {
			for _, s := range hashes {
				parents[level] = append(parents[level], hash(s))
			}
		}
		blueWork, ok := new(big.Int).SetString(v.BlueWork, 16)
		if !ok {
			t.Fatalf("vector %d: bad blueWork %q", i, v.BlueWork)
		}
		header := blockheader.NewImmutableBlockHeader(v.Version, parents, hash(v.HashMerkleRoot),
			hash(v.AcceptedIDMerkleRoot), hash(v.UTXOCommitment), v.Timestamp, v.Bits, v.Nonce,
			v.DAAScore, v.BlueScore, blueWork, hash(v.PruningPoint))
		if got := consensushashing.HeaderHash(header).String(); got != v.Hash {
			t.Errorf("vector %d: header hash %s, want %s", i, got, v.Hash)
			continue
		}
		state := pow.NewState(header.ToMutable())
		powNum, _ := state.CalculateProofOfWorkValue()
		if got := fmt.Sprintf("%064x", powNum); got != v.PoW {
			t.Errorf("vector %d (block %s): PoW value %s, want %s", i, v.Hash, got, v.PoW)
		}
		if powNum.Cmp(&state.Target) > 0 {
			t.Errorf("vector %d (block %s): PoW value above target", i, v.Hash)
		}
	}
}
