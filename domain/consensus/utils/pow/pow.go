package pow

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashes"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/serialization"
	"github.com/Hoosat-Oy/HTND/util/difficulty"

	"math/big"

	"github.com/pkg/errors"
)

// State is an intermediate data structure with pre-computed values to speed up mining.
type State struct {
	mat          matrix
	floatMat     floatMatrix
	Timestamp    int64
	Nonce        uint64
	Target       big.Int
	PrevHeader   externalapi.DomainHash
	BlockVersion uint16
}

// NewState creates a new state with pre-computed values to speed up mining
// It takes the target from the Bits field
func NewState(header externalapi.MutableBlockHeader) *State {
	target := difficulty.CompactToBig(header.Bits())
	// Zero out the time and nonce.
	timestamp, nonce := header.TimeInMilliseconds(), header.Nonce()
	header.SetTimeInMilliseconds(0)
	header.SetNonce(0)
	prevHeader := consensushashing.HeaderHash(header)
	header.SetTimeInMilliseconds(timestamp)
	header.SetNonce(nonce)
	if header.Version() == 1 {
		return &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			mat:          *GenerateMatrix(prevHeader),
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
		}
	} else if header.Version() == 2 {
		return &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			mat:          *GenerateHoohashMatrix(prevHeader),
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
		}
	} else if header.Version() == 3 || header.Version() == 4 {
		return &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			mat:          *GenerateMatrix(prevHeader),
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
		}
	} else if header.Version() >= 5 {
		return &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			floatMat:     *GenerateHoohashMatrixV110(prevHeader),
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
		}
	} else {
		return &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			mat:          *GenerateMatrix(prevHeader),
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
		}
	}

}

func (state *State) CalculateProofOfWorkValue() (*big.Int, *externalapi.DomainHash) {
	if state.BlockVersion == 1 {
		return state.CalculateProofOfWorkValuePyrinhash()
	} else if state.BlockVersion == 2 {
		return state.CalculateProofOfWorkValueHoohashV1()
	} else if state.BlockVersion == 3 || state.BlockVersion == 4 {
		return state.CalculateProofOfWorkValueHoohashV101()
	} else if state.BlockVersion >= 5 {
		return state.CalculateProofOfWorkValueHoohashV110()
	} else {
		return state.CalculateProofOfWorkValuePyrinhash() // default to the oldest version.
	}
}

func (state *State) CalculateProofOfWorkValueHoohashV1() (*big.Int, *externalapi.DomainHash) {
	// PRE_POW_HASH || TIME || 32 zero byte padding || NONCE
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	err := serialization.WriteElement(writer, state.Timestamp)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	err = serialization.WriteElement(writer, state.Nonce)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	powHash := writer.Finalize()
	multiplied := state.mat.HoohashMatrixMultiplicationV1(powHash)
	return toBig(multiplied), multiplied
}

func (state *State) CalculateProofOfWorkValueHoohashV101() (*big.Int, *externalapi.DomainHash) {
	// PRE_POW_HASH || TIME || 32 zero byte padding || NONCE
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	err := serialization.WriteElement(writer, state.Timestamp)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	err = serialization.WriteElement(writer, state.Nonce)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	powHash := writer.Finalize()
	multiplied := state.mat.HoohashMatrixMultiplicationV101(powHash)
	return toBig(multiplied), multiplied
}

func (state *State) CalculateProofOfWorkValueHoohashV110() (*big.Int, *externalapi.DomainHash) {
	// PRE_POW_HASH || TIME || 32 zero byte padding || NONCE
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	err := serialization.WriteElement(writer, state.Timestamp)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	err = serialization.WriteElement(writer, state.Nonce)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	powHash := writer.Finalize()
	multiplied := state.floatMat.HoohashMatrixMultiplicationV110(powHash, state.Nonce)
	return toBig(multiplied), multiplied
}

// CalculateProofOfWorkValue hashes the internal header and returns its big.Int value
func (state *State) CalculateProofOfWorkValuePyrinhash() (*big.Int, *externalapi.DomainHash) {
	// PRE_POW_HASH || TIME || 32 zero byte padding || NONCE
	writer := hashes.PoWHashWriter() // Blake 3
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	err := serialization.WriteElement(writer, state.Timestamp)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	err = serialization.WriteElement(writer, state.Nonce)
	if err != nil {
		panic(errors.Wrap(err, "this should never happen. Hash digest should never return an error"))
	}
	powHash := writer.Finalize()
	hash := state.mat.bHeavyHash(powHash)
	return toBig(hash), hash
}

// IncrementNonce the nonce in State by 1
func (state *State) IncrementNonce() {
	state.Nonce++
}

// CheckProofOfWork check's if the block has a valid PoW according to the provided target
// it does not check if the difficulty itself is valid or less than the maximum for the appropriate network
func (state *State) CheckProofOfWork(block *externalapi.DomainBlock, powSkip bool) bool {
	powNum, _ := state.CalculateProofOfWorkValue()
	if state.BlockVersion < constants.PoWIntegrityMinVersion {
		return powNum.Cmp(&state.Target) <= 0
	} else if powSkip && state.BlockVersion >= constants.PoWIntegrityMinVersion {
		return powNum.Cmp(&state.Target) <= 0
	} else if state.BlockVersion >= constants.PoWIntegrityMinVersion {
		if !block.PoWHash.Equal(new(externalapi.DomainHash)) {
			submittedPowNum := toBig(block.PoWHash)
			if submittedPowNum.Cmp(powNum) == 0 {
				return powNum.Cmp(&state.Target) <= 0
			}
		}
	}
	return false
}

// CheckProofOfWorkByBits check's if the block has a valid PoW according to its Bits field
// it does not check if the difficulty itself is valid or less than the maximum for the appropriate network
func CheckProofOfWorkByBits(header externalapi.MutableBlockHeader, block *externalapi.DomainBlock, powSkip bool) bool {
	return NewState(header).CheckProofOfWork(block, powSkip)
}

// ToBig converts a externalapi.DomainHash into a big.Int treated as a little endian string.
func toBig(hash *externalapi.DomainHash) *big.Int {
	// We treat the Hash as little-endian for PoW purposes, but the big package wants the bytes in big-endian, so reverse them.
	buf := hash.ByteSlice()
	blen := len(buf)
	for i := 0; i < blen/2; i++ {
		buf[i], buf[blen-1-i] = buf[blen-1-i], buf[i]
	}

	return new(big.Int).SetBytes(buf)
}

// BlockLevel returns the block level of the given header.
func BlockLevel(header externalapi.BlockHeader, maxBlockLevel int) int {
	// Genesis is defined to be the root of all blocks at all levels, so we define it to be the maximal
	// block level.
	if len(header.DirectParents()) == 0 {
		return maxBlockLevel
	}

	proofOfWorkValue, _ := NewState(header.ToMutable()).CalculateProofOfWorkValue()
	level := max(
		// If the block has a level lower than genesis make it zero.
		maxBlockLevel-proofOfWorkValue.BitLen(), 0)
	return level
}
