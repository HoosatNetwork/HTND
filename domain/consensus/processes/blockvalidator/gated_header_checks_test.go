package blockvalidator_test

import (
	"errors"
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// gatedChecks lists every gate that re-enables a previously disabled header or structural check.
var gatedChecks = map[string]func(*dagconfig.HardForkGates) *uint16{
	"ParentsIncestVersion":      func(g *dagconfig.HardForkGates) *uint16 { return &g.ParentsIncestVersion },
	"MergeSetSizeLimitVersion":  func(g *dagconfig.HardForkGates) *uint16 { return &g.MergeSetSizeLimitVersion },
	"HeaderDAAScoreVersion":     func(g *dagconfig.HardForkGates) *uint16 { return &g.HeaderDAAScoreVersion },
	"HeaderBlueWorkVersion":     func(g *dagconfig.HardForkGates) *uint16 { return &g.HeaderBlueWorkVersion },
	"HeaderBlueScoreVersion":    func(g *dagconfig.HardForkGates) *uint16 { return &g.HeaderBlueScoreVersion },
	"HeaderPruningPointVersion": func(g *dagconfig.HardForkGates) *uint16 { return &g.HeaderPruningPointVersion },
	"IndirectParentsVersion":    func(g *dagconfig.HardForkGates) *uint16 { return &g.IndirectParentsVersion },
}

func setGatedChecks(tc testapi.TestConsensus, version uint16) {
	for _, gate := range gatedChecks {
		*gate(tc.HardForkGates()) = version
	}
}

// TestGatedChecksAreUnscheduledOnMainnet pins that none of these checks is scheduled on mainnet: each
// would reject blocks of the existing chain if it applied to a version mainnet has reached.
func TestGatedChecksAreUnscheduledOnMainnet(t *testing.T) {
	gates := dagconfig.MainnetParams.HardForkGates
	for name, gate := range gatedChecks {
		if *gate(&gates) != ^uint16(0) {
			t.Errorf("%s is scheduled on mainnet at block version %d", name, *gate(&gates))
		}
	}
}

// TestGatedChecksAcceptBuiltBlocks is the check that matters most before activation: with every gate
// active from version 1, a DAG of blocks this node builds itself - chains, siblings and merges - must
// still be valid. A check that rejects honest blocks would split the network at activation.
func TestGatedChecksAcceptBuiltBlocks(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGatedChecksAcceptBuiltBlocks")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)
		setGatedChecks(tc, 1)

		tip := consensusConfig.GenesisHash
		for i := 0; i < 60; i++ {
			left, _, err := tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock left #%d: %+v", i, err)
			}
			right, _, err := tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock right #%d: %+v", i, err)
			}
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{left, right}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock merge #%d: %+v", i, err)
			}
			if status := blockStatusOf(t, tc, tip); status != externalapi.StatusUTXOValid {
				t.Fatalf("merge block #%d has status %s with every gated check active", i, status)
			}
		}
	})
}

// TestGatedHeaderChecksRejectTamperedHeaders pins, for each gated header field, that a header
// misstating it is rejected with the field's rule error once the gate is active, and is not rejected
// for that reason before. The blocks are inserted header-only, as headers-first IBD inserts them: with
// a body, the coinbase blue score check would reject a misstated blue score first.
func TestGatedHeaderChecksRejectTamperedHeaders(t *testing.T) {
	type tamperFunc func(h externalapi.BlockHeader, delta uint64, other *externalapi.DomainHash) externalapi.BlockHeader
	rebuild := func(h externalapi.BlockHeader, parents []externalapi.BlockLevelParents, daaScore, blueScore uint64,
		blueWork *big.Int, pruningPoint *externalapi.DomainHash,
	) externalapi.BlockHeader {
		return blockheader.NewImmutableBlockHeader(h.Version(), parents, h.HashMerkleRoot(),
			h.AcceptedIDMerkleRoot(), h.UTXOCommitment(), h.TimeInMilliseconds(), h.Bits(), h.Nonce(),
			daaScore, blueScore, blueWork, pruningPoint)
	}
	cases := []struct {
		gate        string
		expectedErr error
		tamper      tamperFunc
	}{
		{"HeaderDAAScoreVersion", ruleerrors.ErrUnexpectedDAAScore,
			func(h externalapi.BlockHeader, delta uint64, _ *externalapi.DomainHash) externalapi.BlockHeader {
				return rebuild(h, h.Parents(), h.DAAScore()+delta, h.BlueScore(), h.BlueWork(), h.PruningPoint())
			}},
		{"HeaderBlueScoreVersion", ruleerrors.ErrUnexpectedBlueScore,
			func(h externalapi.BlockHeader, delta uint64, _ *externalapi.DomainHash) externalapi.BlockHeader {
				return rebuild(h, h.Parents(), h.DAAScore(), h.BlueScore()+delta, h.BlueWork(), h.PruningPoint())
			}},
		{"HeaderBlueWorkVersion", ruleerrors.ErrUnexpectedBlueWork,
			func(h externalapi.BlockHeader, delta uint64, _ *externalapi.DomainHash) externalapi.BlockHeader {
				blueWork := new(big.Int).Add(h.BlueWork(), new(big.Int).SetUint64(delta))
				return rebuild(h, h.Parents(), h.DAAScore(), h.BlueScore(), blueWork, h.PruningPoint())
			}},
		{"HeaderPruningPointVersion", ruleerrors.ErrUnexpectedPruningPoint,
			func(h externalapi.BlockHeader, _ uint64, other *externalapi.DomainHash) externalapi.BlockHeader {
				return rebuild(h, h.Parents(), h.DAAScore(), h.BlueScore(), h.BlueWork(), other)
			}},
		{"IndirectParentsVersion", ruleerrors.ErrUnexpectedParents,
			func(h externalapi.BlockHeader, _ uint64, other *externalapi.DomainHash) externalapi.BlockHeader {
				parents := append([]externalapi.BlockLevelParents{h.Parents()[0]}, externalapi.BlockLevelParents{other})
				return rebuild(h, parents, h.DAAScore(), h.BlueScore(), h.BlueWork(), h.PruningPoint())
			}},
	}

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		for _, c := range cases {
			t.Run(c.gate, func(t *testing.T) {
				tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
					"TestGatedHeaderChecksRejectTamperedHeaders"+c.gate)
				if err != nil {
					t.Fatalf("NewTestConsensus: %+v", err)
				}
				defer teardown(false)

				// A short chain, so a wrong pruning point or indirect parent can name a real block other
				// than the expected one.
				chain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
				for range 3 {
					hash, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
					if err != nil {
						t.Fatalf("AddBlock: %+v", err)
					}
					chain = append(chain, hash)
				}
				tip := chain[len(chain)-1]
				gate := gatedChecks[c.gate](tc.HardForkGates())

				for _, active := range []bool{true, false} {
					header, err := tc.BuildHeaderWithParents([]*externalapi.DomainHash{tip})
					if err != nil {
						t.Fatalf("BuildHeaderWithParents: %+v", err)
					}
					// A different tamper per pass, so the second block is not the one already rejected.
					if active {
						*gate = 1
						header = c.tamper(header, 1, chain[1])
					} else {
						*gate = ^uint16(0)
						header = c.tamper(header, 2, chain[2])
					}
					err = tc.ValidateAndInsertBlock(&externalapi.DomainBlock{Header: header}, false, true)
					if active && !errors.Is(err, c.expectedErr) {
						t.Fatalf("with %s active, expected %v, got %+v", c.gate, c.expectedErr, err)
					}
					if !active && errors.Is(err, c.expectedErr) {
						t.Fatalf("with %s inactive, the block was still rejected for it: %+v", c.gate, err)
					}
				}
			})
		}
	})
}

// TestGatedParentsIncest pins that a block one of whose direct parents is an ancestor of another is
// rejected from HardForkGates.ParentsIncestVersion and not before.
func TestGatedParentsIncest(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGatedParentsIncest")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		ancestor, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		descendant, _, err := tc.AddBlock([]*externalapi.DomainHash{ancestor}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		tc.HardForkGates().ParentsIncestVersion = 1
		_, _, err = tc.AddBlock([]*externalapi.DomainHash{ancestor, descendant}, nil, nil)
		if !errors.Is(err, ruleerrors.ErrInvalidParentsRelation) {
			t.Fatalf("with the gate active, expected ErrInvalidParentsRelation, got %+v", err)
		}

		tc.HardForkGates().ParentsIncestVersion = ^uint16(0)
		// A different coinbase gives a different block than the one already rejected.
		coinbaseData := &externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1}},
			ExtraData:       []byte{1},
		}
		_, _, err = tc.AddBlock([]*externalapi.DomainHash{ancestor, descendant}, coinbaseData, nil)
		if errors.Is(err, ruleerrors.ErrInvalidParentsRelation) {
			t.Fatalf("with the gate inactive, the block was still rejected for parent incest: %+v", err)
		}
	})
}

// TestGatedMergeSetSizeLimit pins that a block merging more blocks than MergeSetSizeLimit is rejected
// from HardForkGates.MergeSetSizeLimitVersion and not before.
func TestGatedMergeSetSizeLimit(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.MergeSetSizeLimit = 2
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGatedMergeSetSizeLimit")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		base, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		siblings := make([]*externalapi.DomainHash, 0, 4)
		for i := range 4 {
			coinbaseData := &externalapi.DomainCoinbaseData{
				ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{byte(i)}},
			}
			sibling, _, err := tc.AddBlock([]*externalapi.DomainHash{base}, coinbaseData, nil)
			if err != nil {
				t.Fatalf("AddBlock sibling %d: %+v", i, err)
			}
			siblings = append(siblings, sibling)
		}

		tc.HardForkGates().MergeSetSizeLimitVersion = 1
		_, _, err = tc.AddBlock(siblings, nil, nil)
		if !errors.Is(err, ruleerrors.ErrViolatingMergeLimit) {
			t.Fatalf("with the gate active, expected ErrViolatingMergeLimit, got %+v", err)
		}

		tc.HardForkGates().MergeSetSizeLimitVersion = ^uint16(0)
		coinbaseData := &externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{9}},
		}
		_, _, err = tc.AddBlock(siblings, coinbaseData, nil)
		if errors.Is(err, ruleerrors.ErrViolatingMergeLimit) {
			t.Fatalf("with the gate inactive, the block was still rejected for its merge set: %+v", err)
		}
	})
}

func blockStatusOf(t *testing.T, tc testapi.TestConsensus, blockHash *externalapi.DomainHash) externalapi.BlockStatus {
	t.Helper()
	status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), blockHash)
	if err != nil {
		t.Fatalf("BlockStatusStore.Get(%s): %+v", blockHash, err)
	}
	return status
}
