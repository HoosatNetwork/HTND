package testutils

import (
	"slices"
	"sync"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
)

var blockVersionTestLock sync.Mutex

func cloneParams(params dagconfig.Params) dagconfig.Params {
	cloned := params
	cloned.DNSSeeds = slices.Clone(params.DNSSeeds)
	cloned.K = slices.Clone(params.K)
	cloned.TargetTimePerBlock = slices.Clone(params.TargetTimePerBlock)
	cloned.FinalityDuration = slices.Clone(params.FinalityDuration)
	cloned.DifficultyAdjustmentWindowSize = slices.Clone(params.DifficultyAdjustmentWindowSize)
	cloned.PruningMultiplier = slices.Clone(params.PruningMultiplier)
	cloned.MaxBlockMass = slices.Clone(params.MaxBlockMass)
	cloned.MaxBlockParents = slices.Clone(params.MaxBlockParents)
	cloned.MergeDepth = slices.Clone(params.MergeDepth)
	cloned.POWScores = slices.Clone(params.POWScores)
	return cloned
}

// ForAllNets runs the passed testFunc with all available networks
// if setDifficultyToMinumum = true - will modify the net params to have minimal difficulty, like in SimNet
func ForAllNets(t *testing.T, skipPow bool, testFunc func(*testing.T, *consensus.Config)) {
	allParams := []dagconfig.Params{
		dagconfig.MainnetParams,
		dagconfig.TestnetParams,
	}

	for _, params := range allParams {
		consensusConfig := consensus.Config{Params: cloneParams(params)}
		t.Run(consensusConfig.Name, func(t *testing.T) {
			blockVersionTestLock.Lock()
			defer blockVersionTestLock.Unlock()
			// NOTE: Do not run these subtests in parallel.
			// The consensus code mutates the global block version via constants.SetBlockVersion
			// during validation/building, so parallel subtests will interfere with each other.
			previousBlockVersion := constants.GetBlockVersion()
			constants.SetBlockVersion(1)
			t.Cleanup(func() {
				constants.SetBlockVersion(previousBlockVersion)
			})
			consensusConfig.SkipProofOfWork = skipPow
			t.Logf("Running test for %s", consensusConfig.Name)
			testFunc(t, &consensusConfig)
		})
	}
}
