package mining

import (
	"math"
	"math/big"
	"math/rand"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/pow"
	"github.com/pkg/errors"
)

// SolveBlock increments the given block's nonce until it matches the difficulty requirements in its bits field
func SolveBlock(block *externalapi.DomainBlock, rd *rand.Rand) (*big.Int, string) {
	header := block.Header.ToMutable()
	state := pow.NewState(header)
	for state.Nonce = rd.Uint64(); state.Nonce < math.MaxUint64; state.Nonce++ {
		powNum, powHash := state.CalculateProofOfWorkValue()
		if powNum.Cmp(&state.Target) <= 0 {
			header.SetNonce(state.Nonce)
			block.Header = header.ToImmutable()
			return powNum, powHash.String()
		}
	}

	panic(errors.New("went over all the nonce space and couldn't find a single one that gives a valid block"))
}
