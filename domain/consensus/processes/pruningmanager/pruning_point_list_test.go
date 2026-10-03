package pruningmanager_test

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// pruningListTestChain builds a 200-block chain on mainnet parameters shrunk so the pruning point advances every 10
// blocks with a pruning depth of 42 blocks, which makes each pruning point's header commit to the one 5 indices back.
// powScores is the activation table, so a test can put the block version boundary where it needs the anchor.
func pruningListTestChain(t *testing.T, name string, powScores []uint64,
) (tc testapi.TestConsensus, chain []*externalapi.DomainHash, teardown func(bool)) {
	config := &consensus.Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	config.DisableDifficultyAdjustment = true
	config.POWScores = powScores
	// One entry per reachable version: checkBlockTimestampInIsolation indexes TargetTimePerBlock without clamping.
	versions := len(powScores) + 1
	targetTime := config.TargetTimePerBlock[0]
	config.TargetTimePerBlock = make([]time.Duration, versions)
	config.FinalityDuration = make([]time.Duration, versions)
	config.K = make([]externalapi.KType, versions)
	config.PruningMultiplier = make([]uint64, versions)
	for i := range versions {
		config.TargetTimePerBlock[i] = targetTime
		config.FinalityDuration[i] = 10 * targetTime
		config.K[i] = 2
		config.PruningMultiplier[i] = 1
	}
	config.MergeSetSizeLimit = 2

	previousBlockVersion := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previousBlockVersion)) })

	tc, teardown, err := consensus.NewFactory().NewTestConsensus(config, name)
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	chain = []*externalapi.DomainHash{config.GenesisHash}
	for i := range 200 {
		tip, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
		if err != nil {
			teardown(false)
			t.Fatalf("AddBlock #%d: %+v", i, err)
		}
		chain = append(chain, tip)
	}
	return tc, chain, teardown
}

// storedPruningPoints returns the stored pruning point list, oldest first.
func storedPruningPoints(t *testing.T, tc testapi.TestConsensus) []*externalapi.DomainHash {
	stagingArea := model.NewStagingArea()
	currentIndex, err := tc.PruningStore().CurrentPruningPointIndex(tc.DatabaseContext(), stagingArea)
	if err != nil {
		t.Fatalf("CurrentPruningPointIndex: %+v", err)
	}
	list := make([]*externalapi.DomainHash, 0, currentIndex+1)
	for i := uint64(0); i <= currentIndex; i++ {
		hash, err := tc.PruningStore().PruningPointByIndex(tc.DatabaseContext(), stagingArea, i)
		if err != nil {
			t.Fatalf("PruningPointByIndex(%d): %+v", i, err)
		}
		list = append(list, hash)
	}
	return list
}

// stagedList stages list as the whole pruning point list in a new staging area, which is never committed. The pruning
// store moves its staged current index only upwards from zero, so every index is staged, oldest first, the way
// ImportPruningPoints stages them.
func stagedList(t *testing.T, tc testapi.TestConsensus, list []*externalapi.DomainHash) *model.StagingArea {
	stagingArea := model.NewStagingArea()
	for i, hash := range list {
		err := tc.PruningStore().StagePruningPointByIndex(tc.DatabaseContext(), stagingArea, hash, uint64(i))
		if err != nil {
			t.Fatalf("StagePruningPointByIndex(%d): %+v", i, err)
		}
	}
	return stagingArea
}

func assertPruningListVerdict(t *testing.T, tc testapi.TestConsensus, stagingArea *model.StagingArea,
	anchorBlockVersion uint16, want bool, scenario string,
) {
	t.Helper()
	got, err := tc.PruningManager().ArePruningPointsInValidChain(stagingArea, anchorBlockVersion)
	if err != nil {
		t.Fatalf("%s, anchor %d: ArePruningPointsInValidChain returned an error, want %t: %+v", scenario,
			anchorBlockVersion, want, err)
	}
	if got != want {
		t.Fatalf("%s, anchor %d: ArePruningPointsInValidChain = %t, want %t", scenario, anchorBlockVersion, got, want)
	}
}

// TestArePruningPointsInValidChain pins the rewritten list check on a chain whose pruning point has advanced 15 times.
// The list the consensus wrote passes with commitments followed to genesis and with the anchor never reached; the
// selected-chain walk it replaces returned an error at index 1 here, because it took genesis' zero-hash commitment for a
// list entry. A pruning point staged on top that no header commits to fails, too shallow or off the chain alike, and
// does so as (false, nil), not as an error. So does a list with two of the newest pruning points swapped: the headers
// above the pruning point name both, so their order is checked whatever the anchor.
func TestArePruningPointsInValidChain(t *testing.T) {
	tc, chain, teardown := pruningListTestChain(t, "TestArePruningPointsInValidChain", []uint64{math.MaxUint64})
	defer teardown(false)

	list := storedPruningPoints(t, tc)
	if len(list) < 10 {
		t.Fatalf("the pruning point advanced only %d time(s), too few to test a list", len(list)-1)
	}

	// A block beside the chain, above the pruning point and deep enough below the tip to pass for one by depth alone.
	sideBlock, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-46]}, nil, nil)
	if err != nil {
		t.Fatalf("AddBlock: %+v", err)
	}

	for _, anchor := range []uint16{1, ^uint16(0)} {
		assertPruningListVerdict(t, tc, model.NewStagingArea(), anchor, true, "the list the consensus wrote")
		assertPruningListVerdict(t, tc, stagedList(t, tc, list), anchor, true, "the same list, restaged")

		shallow := append(append([]*externalapi.DomainHash{}, list...), chain[len(chain)-2])
		assertPruningListVerdict(t, tc, stagedList(t, tc, shallow), anchor, false,
			"a pruning point one block below the tip staged on top")

		foreign := append(append([]*externalapi.DomainHash{}, list...), sideBlock)
		assertPruningListVerdict(t, tc, stagedList(t, tc, foreign), anchor, false,
			"a block off the selected chain staged on top")

		reordered := append([]*externalapi.DomainHash{}, list...)
		last := len(reordered) - 1
		reordered[last-1], reordered[last-2] = reordered[last-2], reordered[last-1]
		assertPruningListVerdict(t, tc, stagedList(t, tc, reordered), anchor, false,
			"two pruning points below the current one swapped")
	}
}

// TestArePruningPointsInValidChainAnchor pins where the walk stops following header commitments. Blocks from DAA score
// 125 on are version 2, so with the anchor at version 2 the pruning points from 130 up are followed and the first one
// below 125 is the anchor. A stored entry that only the anchor's own header names is replaced with a block no header
// commits to: that foreign commitment is below the anchor and tolerated, but with the anchor at version 1, where the
// same pruning point is followed, it fails the list. A replaced entry that a pruning point above the anchor names fails
// either way.
func TestArePruningPointsInValidChainAnchor(t *testing.T) {
	const activationDAAScore = 125
	tc, chain, teardown := pruningListTestChain(t, "TestArePruningPointsInValidChainAnchor",
		[]uint64{activationDAAScore})
	defer teardown(false)

	list := storedPruningPoints(t, tc)
	stagingArea := model.NewStagingArea()
	indexOf := make(map[externalapi.DomainHash]int, len(list))
	for i, hash := range list {
		indexOf[*hash] = i
	}
	daaScore := func(hash *externalapi.DomainHash) uint64 {
		score, err := tc.DAABlocksStore().DAAScore(tc.DatabaseContext(), stagingArea, hash)
		if err != nil {
			t.Fatalf("DAAScore: %+v", err)
		}
		return score
	}
	commitmentIndex := func(i int) int {
		header, err := tc.BlockHeaderStore().BlockHeader(tc.DatabaseContext(), stagingArea, list[i])
		if err != nil {
			t.Fatalf("BlockHeader: %+v", err)
		}
		j, ok := indexOf[*header.PruningPoint()]
		if !ok {
			t.Fatalf("stored pruning point %d commits to %s, which is not stored", i, header.PruningPoint())
		}
		return j
	}

	anchor := len(list) - 1
	for daaScore(list[anchor]) >= activationDAAScore {
		anchor--
	}
	if anchor >= len(list)-2 || anchor < 6 {
		t.Fatalf("the version boundary leaves pruning point %d of %d as the anchor; the test needs at least two "+
			"followed pruning points above it and a commitment below it", anchor, len(list)-1)
	}
	belowAnchor := commitmentIndex(anchor)
	aboveAnchor := commitmentIndex(anchor + 1)
	for i := anchor + 1; i < len(list); i++ {
		if commitmentIndex(i) == belowAnchor {
			t.Fatalf("pruning point %d above the anchor also commits to %d", i, belowAnchor)
		}
	}

	notInList := func() *externalapi.DomainHash {
		for _, hash := range chain {
			if _, ok := indexOf[*hash]; !ok {
				return hash
			}
		}
		t.Fatal("every chain block is a pruning point")
		return nil
	}()
	replaced := func(index int) *model.StagingArea {
		modified := append([]*externalapi.DomainHash{}, list...)
		modified[index] = notInList
		return stagedList(t, tc, modified)
	}

	assertPruningListVerdict(t, tc, model.NewStagingArea(), 2, true, "the list the consensus wrote")
	assertPruningListVerdict(t, tc, replaced(belowAnchor), 2, true,
		"a foreign commitment made by the anchor itself")
	assertPruningListVerdict(t, tc, replaced(belowAnchor), 1, false,
		"the same foreign commitment, with every pruning point followed")
	assertPruningListVerdict(t, tc, replaced(aboveAnchor), 2, false,
		"a foreign commitment made by a pruning point above the anchor")
}

// TestIsValidPruningPointScoredAboveTheTip pins the depth check against a pruning point whose stored blue score is
// above the headers selected tip's. Subtracting the scores as uint64 wrapped around to a huge depth and passed it.
func TestIsValidPruningPointScoredAboveTheTip(t *testing.T) {
	tc, chain, teardown := pruningListTestChain(t, "TestIsValidPruningPointScoredAboveTheTip",
		[]uint64{math.MaxUint64})
	defer teardown(false)

	stagingArea := model.NewStagingArea()
	pruningPoint := chain[100]
	valid, err := tc.PruningManager().IsValidPruningPoint(stagingArea, pruningPoint)
	if err != nil || !valid {
		t.Fatalf("IsValidPruningPoint of a block 100 below the tip = %t, %v; want true", valid, err)
	}

	data, err := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), stagingArea, pruningPoint, false)
	if err != nil {
		t.Fatalf("GHOSTDAGDataStore.Get: %+v", err)
	}
	tc.GHOSTDAGDataStore().Stage(stagingArea, pruningPoint, externalapi.NewBlockGHOSTDAGData(1_000, data.BlueWork(),
		data.SelectedParent(), data.MergeSetBlues(), data.MergeSetReds(), data.BluesAnticoneSizes(), data.DynamicK()),
		false)
	valid, err = tc.PruningManager().IsValidPruningPoint(stagingArea, pruningPoint)
	if err != nil {
		t.Fatalf("IsValidPruningPoint: %+v", err)
	}
	if valid {
		t.Fatal("a pruning point scored above the headers selected tip passed the depth check")
	}
}
