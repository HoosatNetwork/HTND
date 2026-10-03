package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockversion"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// gateVerdict is what one of the gated checks would make of this datadir: PASS and FAIL are the
// (true, nil) and (false, nil) returns, ERROR is a non-nil error - which validateImportedPruningPointChain
// propagates, so for the import it is as fatal as FAIL, just untyped.
type gateVerdict struct {
	outcome string
	detail  string
}

func gatePass(format string, args ...any) gateVerdict {
	return gateVerdict{"PASS", fmt.Sprintf(format, args...)}
}

func gateFail(format string, args ...any) gateVerdict {
	return gateVerdict{"FAIL", fmt.Sprintf(format, args...)}
}

func gateError(format string, args ...any) gateVerdict {
	return gateVerdict{"ERROR", fmt.Sprintf(format, args...)}
}

// pruningListCheck runs, read-only, the two checks dagconfig.ValidateIBDPruningListVersion turns on for an
// imported pruning point (HTN-006): pruningManager.IsValidPruningPoint on the current pruning point and
// pruningManager.ArePruningPointsInValidChain on the stored list. Both are reproduced step for step against this
// datadir's stores, including the order they read data in, so the verdict is what the gate would return here and,
// when it is not PASS, the step and block where it stops.
//
// The gate runs at import time on a syncee that holds only a headers proof; a fully synced datadir holds more data
// than that, so a FAIL or ERROR here means the gate would also refuse on a syncee, while a PASS here does not prove
// a syncee would pass. To check a syncee itself, point -prefix at the staging consensus an IBD is still building.
//
// anchorBlockVersion is the version below which ArePruningPointsInValidChain stops following header commitments. The
// node passes the version its header pruning point gate activates at.
//
// A third walk follows each stored pruning point's own header commitment back through the list one index at a time,
// all the way to genesis, with no anchor and no tolerance for skipped indices. It is not what the gate runs; it shows
// how far the list lines up with the headers when nothing is tolerated.
func pruningListCheck(s *stores, sa *model.StagingArea, params *dagconfig.Params, anchorBlockVersion uint16,
) (pruningListVerdicts, error) {
	fmt.Printf("\n=== pruning point list check (HTN-006, gated at ValidateIBDPruningListVersion)\n")

	pruningPoint, err := s.pruning.PruningPoint(s.db, sa)
	if err != nil {
		return pruningListVerdicts{}, fmt.Errorf("pruning point: %w", err)
	}
	currentIndex, err := s.pruning.CurrentPruningPointIndex(s.db, sa)
	if err != nil {
		return pruningListVerdicts{}, fmt.Errorf("current pruning point index: %w", err)
	}
	headersTip, err := s.headersTip.HeadersSelectedTip(s.db, sa)
	if err != nil {
		return pruningListVerdicts{}, fmt.Errorf("headers selected tip: %w", err)
	}
	blockVersion, err := blockversion.Current(s.db, sa, s.gd, s.headersTip, s.daa, params.POWScores)
	if err != nil {
		return pruningListVerdicts{}, fmt.Errorf("current block version: %w", err)
	}
	pruningDepth := params.PruningDepthForBlockVersion(blockVersion)

	fmt.Printf("  pruning point       [%d] %s\n", currentIndex, pruningPoint)
	fmt.Printf("  headers selected tip     %s\n", headersTip)
	fmt.Printf("  block version %d => pruningDepth %d (%s parameters)\n", blockVersion, pruningDepth, params.Name)

	_, dagTopology, err := newDAGTopology(s, sa)
	if err != nil {
		return pruningListVerdicts{}, fmt.Errorf("reachability: %w", err)
	}

	var v pruningListVerdicts
	fmt.Printf("\n  -- IsValidPruningPoint(%s)\n", pruningPoint)
	v.validPruningPoint = checkIsValidPruningPoint(s, sa, dagTopology, params.GenesisHash, pruningPoint, headersTip,
		pruningDepth)
	fmt.Printf("     => %s: %s\n", v.validPruningPoint.outcome, v.validPruningPoint.detail)

	fmt.Printf("\n  -- ArePruningPointsInValidChain (pruning point header walk down to the first pruning point below "+
		"block version %d)\n", anchorBlockVersion)
	v.validChain = checkPruningPointsInValidChain(s, sa, params, pruningPoint, headersTip, currentIndex,
		anchorBlockVersion)
	fmt.Printf("     => %s: %s\n", v.validChain.outcome, v.validChain.detail)

	fmt.Printf("\n  -- pruning point header walk (not gated; each stored pruning point's own header commitment)\n")
	v.headerWalk = checkPruningPointHeaderWalk(s, sa, params.GenesisHash, pruningPoint, headersTip, currentIndex)
	fmt.Printf("     => %s: %s\n", v.headerWalk.outcome, v.headerWalk.detail)
	printPruningPointAlignment(s, sa, currentIndex)

	fmt.Printf("\n  -- verdict\n")
	if v.validPruningPoint.outcome == "PASS" && v.validChain.outcome == "PASS" {
		fmt.Printf("     with the gate active, importing this pruning point would pass both checks\n")
	} else {
		fmt.Printf("     with the gate active, importing this pruning point would be REFUSED "+
			"(IsValidPruningPoint %s, ArePruningPointsInValidChain %s)\n",
			v.validPruningPoint.outcome, v.validChain.outcome)
	}
	return v, nil
}

// pruningListVerdicts holds the two gated checks' verdicts and the ungated header walk's.
type pruningListVerdicts struct {
	validPruningPoint, validChain, headerWalk gateVerdict
}

// checkIsValidPruningPoint mirrors pruningManager.IsValidPruningPoint.
func checkIsValidPruningPoint(s *stores, sa *model.StagingArea, dagTopology model.DAGTopologyManager,
	genesis, pruningPoint, headersTip *externalapi.DomainHash, pruningDepth uint64,
) gateVerdict {
	if pruningPoint.Equal(genesis) {
		return gatePass("the pruning point is genesis")
	}

	tipData, err := s.gd.Get(s.db, sa, headersTip, false)
	if err != nil {
		return gateError("GHOSTDAG data of the headers selected tip: %v", err)
	}

	inChain, err := dagTopology.IsInSelectedParentChainOf(sa, pruningPoint, headersTip)
	if err != nil {
		return gateError("IsInSelectedParentChainOf: %v", err)
	}
	if !inChain {
		return gateFail("the pruning point is not in the selected chain of the headers selected tip")
	}
	fmt.Printf("     in the selected chain of the headers selected tip: yes\n")

	pruningPointData, err := s.gd.Get(s.db, sa, pruningPoint, false)
	if err != nil {
		return gateError("GHOSTDAG data of the pruning point: %v", err)
	}

	tipScore, pruningPointScore := tipData.BlueScore(), pruningPointData.BlueScore()
	fmt.Printf("     stored blue score: tip %d, pruning point %d\n", tipScore, pruningPointScore)
	if tipHeader, err := s.headers.BlockHeader(s.db, sa, headersTip); err == nil {
		if ppHeader, err := s.headers.BlockHeader(s.db, sa, pruningPoint); err == nil {
			fmt.Printf("     header blue score: tip %d, pruning point %d (the check reads the stored values, "+
				"not these)\n", tipHeader.BlueScore(), ppHeader.BlueScore())
		}
	}

	if pruningPointScore > tipScore {
		return gateFail("the pruning point's stored blue score exceeds the tip's (%d > %d)", pruningPointScore,
			tipScore)
	}
	depth := tipScore - pruningPointScore
	if depth < pruningDepth-1 {
		return gateFail("depth %d is below pruningDepth-1 = %d", depth, pruningDepth-1)
	}
	return gatePass("depth %d >= pruningDepth-1 = %d", depth, pruningDepth-1)
}

// checkPruningPointsInValidChain mirrors pruningManager.ArePruningPointsInValidChain: the headers above the pruning
// point must commit to it, then each matched stored pruning point's own header commitment must name a stored pruning
// point within the window below it, down to the first matched pruning point below anchorBlockVersion.
func checkPruningPointsInValidChain(s *stores, sa *model.StagingArea, params *dagconfig.Params,
	pruningPoint, headersTip *externalapi.DomainHash, currentIndex uint64, anchorBlockVersion uint16,
) gateVerdict {
	genesis := params.GenesisHash
	if currentIndex == 0 {
		if pruningPoint.Equal(genesis) {
			return gatePass("the only stored pruning point is genesis")
		}
		return gateFail("the only stored pruning point %s is not genesis", pruningPoint)
	}

	// Newest first: the distinct commitments of the headers on the selected chain above the pruning point.
	var tipCommitments []*externalapi.DomainHash
	above := make(map[externalapi.DomainHash]struct{})
	current := headersTip
	walked := 0
	for !current.Equal(pruningPoint) {
		if current.Equal(model.VirtualGenesisBlockHash) {
			return gateFail("the selected chain reaches virtual genesis, where this node's data ends, %d blocks below "+
				"the tip without passing the pruning point", walked)
		}
		header, err := s.headers.BlockHeader(s.db, sa, current)
		if err != nil {
			return gateError("header of %s, %d selected-chain blocks below the tip: %v", current, walked, err)
		}
		above[*current] = struct{}{}
		if len(tipCommitments) == 0 || !tipCommitments[len(tipCommitments)-1].Equal(header.PruningPoint()) {
			tipCommitments = append(tipCommitments, header.PruningPoint())
		}
		data, err := s.gd.Get(s.db, sa, current, false)
		if err != nil {
			return gateError("GHOSTDAG data of %s, %d selected-chain blocks below the tip: %v", current, walked, err)
		}
		if data.SelectedParent() == nil {
			return gateFail("the selected chain ends at %s without passing the pruning point", current)
		}
		current = data.SelectedParent()
		walked++
	}
	adopted := tipCommitments[:0]
	for _, commitment := range tipCommitments {
		if _, isAbove := above[*commitment]; !isAbove {
			adopted = append(adopted, commitment)
		}
	}
	fmt.Printf("     %d selected-chain blocks above the pruning point commit to %d distinct adopted pruning point(s)\n",
		walked, len(adopted))

	blockVersion := func(hash *externalapi.DomainHash, header externalapi.BlockHeader) (uint16, error) {
		daaScore, err := s.daa.DAAScore(s.db, sa, hash)
		if database.IsNotFoundError(err) {
			daaScore = header.DAAScore()
		} else if err != nil {
			return 0, err
		}
		return constants.BlockVersionForDAAScore(params.POWScores, daaScore), nil
	}
	window := func(blockVersion uint16) uint64 {
		finalityDepth := max(params.FinalityDepthForBlockVersion(blockVersion), 1)
		pruningDepth := params.PruningDepthForBlockVersion(blockVersion)
		return 2 * ((pruningDepth + finalityDepth - 1) / finalityDepth)
	}
	pending := make(map[externalapi.DomainHash]uint64)
	expect := func(hash *externalapi.DomainHash, committerIndex, window uint64) {
		if _, ok := pending[*hash]; ok {
			return
		}
		lowest := uint64(0)
		if committerIndex > window {
			lowest = committerIndex - window
		}
		pending[*hash] = lowest
	}

	pruningPointHeader, err := s.headers.BlockHeader(s.db, sa, pruningPoint)
	if err != nil {
		return gateError("header of the pruning point: %v", err)
	}
	pruningPointVersion, err := blockVersion(pruningPoint, pruningPointHeader)
	if err != nil {
		return gateError("DAA score of the pruning point: %v", err)
	}
	for _, commitment := range adopted {
		expect(commitment, currentIndex+1, window(pruningPointVersion))
	}

	following := true
	gaps := 0
	var newerBlueScore uint64
	for index := currentIndex; ; index-- {
		for hash, lowest := range pending {
			if lowest > index {
				return gateFail("a header commits to %s, but no stored pruning point at index %d or above is it; "+
					"%d newer index(es) checked, %d gap(s)", &hash, lowest, currentIndex-index, gaps)
			}
		}

		stored, err := s.pruning.PruningPointByIndex(s.db, sa, index)
		if err != nil {
			return gateError("stored pruning point [%d] is unreadable: %v", index, err)
		}
		_, matched := pending[*stored]
		delete(pending, *stored)

		if index == currentIndex && !matched {
			return gateFail("no header above the pruning point [%d] %s commits to it", index, stored)
		}
		if index == 0 {
			if !stored.Equal(genesis) {
				return gateFail("stored pruning point [0] %s is not genesis", stored)
			}
			if len(pending) > 0 {
				return gateFail("%d header commitment(s) name no stored pruning point", len(pending))
			}
			return gatePass("all %d index(es) checked back to genesis, %d gap(s), anchor not reached",
				currentIndex+1, gaps)
		}
		if stored.Equal(genesis) {
			return gateFail("genesis is stored at pruning point index %d", index)
		}

		allFound := func() gateVerdict {
			return gatePass("%d index(es) checked, %d gap(s); every commitment followed down to the anchor is in "+
				"the list", currentIndex-index+1, gaps)
		}
		if !matched {
			gaps++
			if !following && len(pending) == 0 {
				return allFound()
			}
			continue
		}
		header, err := s.headers.BlockHeader(s.db, sa, stored)
		if err != nil {
			return gateError("header of stored pruning point [%d] %s: %v", index, stored, err)
		}
		if index < currentIndex && header.BlueScore() >= newerBlueScore {
			return gateFail("stored pruning point [%d] %s has blue score %d, not below the newer matched pruning "+
				"point's %d", index, stored, header.BlueScore(), newerBlueScore)
		}
		newerBlueScore = header.BlueScore()
		if !following {
			if len(pending) == 0 {
				return allFound()
			}
			continue
		}
		version, err := blockVersion(stored, header)
		if err != nil {
			return gateError("DAA score of stored pruning point [%d] %s: %v", index, stored, err)
		}
		if version < anchorBlockVersion {
			following = false
			fmt.Printf("     anchor: [%d] %s at block version %d, below %d\n", index, stored, version,
				anchorBlockVersion)
			if len(pending) == 0 {
				return allFound()
			}
			continue
		}
		expect(header.PruningPoint(), index, window(version))
	}
}

// checkPruningPointHeaderWalk validates the stored list the way a pruned node can: the selected chain from the headers
// selected tip down to the current pruning point supplies the newest expected pruning points, and from there each
// stored pruning point's own header names the next older one. Index 0 must be genesis.
func checkPruningPointHeaderWalk(s *stores, sa *model.StagingArea,
	genesis, pruningPoint, headersTip *externalapi.DomainHash, currentIndex uint64,
) gateVerdict {
	// Newest first.
	var expected []*externalapi.DomainHash
	push := func(hash *externalapi.DomainHash) {
		if len(expected) == 0 || !expected[len(expected)-1].Equal(hash) {
			expected = append(expected, hash)
		}
	}

	current := headersTip
	walked := 0
	for !current.Equal(pruningPoint) {
		header, err := s.headers.BlockHeader(s.db, sa, current)
		if err != nil {
			return gateError("header of %s, %d selected-chain blocks below the tip: %v", current, walked, err)
		}
		push(header.PruningPoint())
		data, err := s.gd.Get(s.db, sa, current, false)
		if err != nil {
			return gateError("GHOSTDAG data of %s, %d selected-chain blocks below the tip: %v", current, walked, err)
		}
		if data.SelectedParent() == nil {
			return gateFail("the selected chain ended at %s without passing the pruning point", current)
		}
		current = data.SelectedParent()
		walked++
	}
	fmt.Printf("     %d selected-chain blocks from the tip down to the pruning point commit to %d distinct "+
		"pruning point(s)\n", walked, len(expected))
	if len(expected) == 0 {
		return gateFail("the headers selected tip is the pruning point itself; there is nothing to compare")
	}
	if !expected[0].Equal(pruningPoint) {
		fmt.Printf("     the tip's header commits to %s, not to this node's pruning point\n", expected[0])
	}

	for i := currentIndex; ; i-- {
		stored, err := s.pruning.PruningPointByIndex(s.db, sa, i)
		if err != nil {
			return gateError("stored pruning point [%d] is unreadable after %d index(es) agreed: %v",
				i, currentIndex-i, err)
		}
		if len(expected) == 0 {
			return gateFail("no header commits to a pruning point for stored index [%d] %s", i, stored)
		}
		if !stored.Equal(expected[0]) {
			return gateFail("stored pruning point [%d] %s is not %s, which the headers above it commit to; "+
				"%d newer index(es) agree", i, stored, expected[0], currentIndex-i)
		}
		expected = expected[1:]

		if i == 0 {
			if !stored.Equal(genesis) {
				return gateFail("stored pruning point [0] %s is not genesis", stored)
			}
			if len(expected) != 0 {
				return gateFail("headers commit to %d pruning point(s) older than genesis", len(expected))
			}
			return gatePass("all %d index(es) agree back to genesis", currentIndex+1)
		}

		header, err := s.headers.BlockHeader(s.db, sa, stored)
		if err != nil {
			return gateError("header of stored pruning point [%d] %s is unreadable after %d index(es) "+
				"agreed: %v", i, stored, currentIndex-i+1, err)
		}
		push(header.PruningPoint())
	}
}

// printPruningPointAlignment lists every stored pruning point, newest first, with the pruning point its own header
// commits to, located in the stored list. It tells apart the two reasons the header walk can fail: a commitment that
// is in the list but skips an index (the walk's one-index-at-a-time model is too strict for this chain), and one that
// is not in the list at all (the miner of that pruning point followed a different pruning point lineage).
func printPruningPointAlignment(s *stores, sa *model.StagingArea, currentIndex uint64) {
	indexOf := make(map[externalapi.DomainHash]uint64, currentIndex+1)
	for i := uint64(0); i <= currentIndex; i++ {
		if hash, err := s.pruning.PruningPointByIndex(s.db, sa, i); err == nil {
			indexOf[*hash] = i
		}
	}

	fmt.Printf("\n  -- every stored pruning point and what its own header commits to (%d, newest first)\n",
		currentIndex+1)
	var unreadable, noHeader, inList, skipping, notInList int
	var previousCommitted uint64
	havePrevious := false
	for i := currentIndex; ; i-- {
		hash, err := s.pruning.PruningPointByIndex(s.db, sa, i)
		if err != nil {
			fmt.Printf("     [%d] <unreadable: %v>\n", i, err)
			unreadable++
			havePrevious = false
		} else if header, err := s.headers.BlockHeader(s.db, sa, hash); err != nil {
			fmt.Printf("     [%d] %s  <header unavailable>\n", i, hash)
			noHeader++
			havePrevious = false
		} else {
			committed := header.PruningPoint()
			fmt.Printf("     [%d] %s blue=%d daa=%d\n          commits to ", i, hash, header.BlueScore(),
				header.DAAScore())
			if *committed == (externalapi.DomainHash{}) {
				fmt.Printf("the zero hash (genesis has no pruning point)\n")
				havePrevious = false
			} else if j, ok := indexOf[*committed]; ok {
				inList++
				note := ""
				if havePrevious && previousCommitted > j+1 {
					skipping++
					note = fmt.Sprintf("  <- skips %d stored index(es)", previousCommitted-j-1)
				}
				fmt.Printf("[%d] %s%s\n", j, committed, note)
				previousCommitted, havePrevious = j, true
			} else {
				notInList++
				fmt.Printf("%s  NOT IN THE STORED LIST", committed)
				if committedHeader, err := s.headers.BlockHeader(s.db, sa, committed); err == nil {
					fmt.Printf(" (header held: blue=%d daa=%d)\n", committedHeader.BlueScore(),
						committedHeader.DAAScore())
				} else {
					fmt.Printf(" (no header held)\n")
				}
				havePrevious = false
			}
		}
		if i == 0 {
			break
		}
	}
	fmt.Printf("     %d pruning point(s): %d commit to a stored one (%d of those skip an index), %d commit to one "+
		"NOT in the stored list, %d without a header, %d unreadable\n",
		currentIndex+1, inList, skipping, notInList, noHeader, unreadable)
}
