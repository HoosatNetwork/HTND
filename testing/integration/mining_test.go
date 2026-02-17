package integration

import (
	"math/rand"
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/mining"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/pow"
)

func mineNextBlock(t *testing.T, harness *appHarness) *externalapi.DomainBlock {
	blockTemplate, err := harness.rpcClient.GetBlockTemplate(harness.miningAddress, "integration")
	if err != nil {
		t.Fatalf("Error getting block template: %+v", err)
	}

	block, err := appmessage.RPCBlockToDomainBlock(blockTemplate.Block, "REAL_MAIN_POW_HASH")
	if err != nil {
		t.Fatalf("Error converting block: %s", err)
	}

	if harness.config.ActiveNetParams.SkipProofOfWork {
		// PoW validation is disabled for integration tests, so avoid expensive nonce search.
		_, powHash := pow.NewState(block.Header.ToMutable()).CalculateProofOfWorkValue()
		block.PoWHash = powHash.String()
	} else {
		rd := rand.New(rand.NewSource(time.Now().UnixNano()))
		_, powHash := mining.SolveBlock(block, rd)
		block.PoWHash = powHash
	}
	_, err = harness.rpcClient.SubmitBlockAlsoIfNonDAA(block, block.PoWHash)
	if err != nil {
		t.Fatalf("Error submitting block: %s", err)
	}

	return block
}
