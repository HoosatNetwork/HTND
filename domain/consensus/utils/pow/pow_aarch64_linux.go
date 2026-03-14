//go:build linux && arm64 && cgo
// +build linux,arm64,cgo

package pow

/*
#cgo LDFLAGS: -L./libs/aarch64-linux -lhoohash -lm -lblake3
#include <stdint.h>

// We only need the external Matrix Multiplier from your libhoohash.a
// This takes the pre-calculated Blake3 hash and runs the FPU math safely.
extern void HoohashMatrixMultiplication(double mat[64][64], const uint8_t *hashBytes, uint8_t *output, uint64_t nonce);
*/
import "C"

import (
	//  "fmt"
	"math/big"
	"unsafe"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashes"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/serialization"
	"github.com/Hoosat-Oy/HTND/util/difficulty"

)

var UseHoohashCLibrary bool

func SetUseHoohashCLibrary(use bool) {
	UseHoohashCLibrary = use
}

type State struct {
	mat          matrix
	floatMat     floatMatrix
	cMatrix      [64][64]C.double // Persist the C Matrix safely on the Go struct
	Timestamp    int64
	Nonce        uint64
	Target       big.Int
	PrevHeader   externalapi.DomainHash
	BlockVersion uint16
	useCLibrary  bool
}

func NewState(header externalapi.MutableBlockHeader) *State {
	target := difficulty.CompactToBig(header.Bits())
	timestamp, nonce := header.TimeInMilliseconds(), header.Nonce()
	header.SetTimeInMilliseconds(0)
	header.SetNonce(0)
	prevHeader := consensushashing.HeaderHash(header)
	header.SetTimeInMilliseconds(timestamp)
	header.SetNonce(nonce)

	if header.Version() == 1 {
		return &State{Target: *target, PrevHeader: *prevHeader, mat: *GenerateMatrix(prevHeader), Timestamp: timestamp, Nonce: nonce, BlockVersion: header.Version()}
	} else if header.Version() == 2 {
		return &State{Target: *target, PrevHeader: *prevHeader, mat: *GenerateHoohashMatrix(prevHeader), Timestamp: timestamp, Nonce: nonce, BlockVersion: header.Version()}
	} else if header.Version() == 3 || header.Version() == 4 {
		return &State{Target: *target, PrevHeader: *prevHeader, mat: *GenerateMatrix(prevHeader), Timestamp: timestamp, Nonce: nonce, BlockVersion: header.Version()}
	} else if header.Version() >= 5 {
		state := &State{
			Target:       *target,
			PrevHeader:   *prevHeader,
			Timestamp:    timestamp,
			Nonce:        nonce,
			BlockVersion: header.Version(),
			useCLibrary:  UseHoohashCLibrary,
		}

		// ALWAYS let Go generate the matrix. There's no divergence in this.
		state.floatMat = *GenerateHoohashMatrixV110(prevHeader)

		// Copy it once into the C array so we don't have memory leaks or GC panics.
		if state.useCLibrary {
			for i := 0; i < 64; i++ {
				for j := 0; j < 64; j++ {
					state.cMatrix[i][j] = C.double(state.floatMat[i][j])
				}
			}
		}
		return state
	} else {
		return &State{Target: *target, PrevHeader: *prevHeader, mat: *GenerateMatrix(prevHeader), Timestamp: timestamp, Nonce: nonce, BlockVersion: header.Version()}
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
		return state.CalculateProofOfWorkValuePyrinhash()
	}
}

func (state *State) CalculateProofOfWorkValueHoohashV1() (*big.Int, *externalapi.DomainHash) {
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	serialization.WriteElement(writer, state.Timestamp)
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	serialization.WriteElement(writer, state.Nonce)
	powHash := writer.Finalize()
	multiplied := state.mat.HoohashMatrixMultiplicationV1(powHash)
	return toBig(multiplied), multiplied
}

func (state *State) CalculateProofOfWorkValueHoohashV101() (*big.Int, *externalapi.DomainHash) {
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	serialization.WriteElement(writer, state.Timestamp)
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	serialization.WriteElement(writer, state.Nonce)
	powHash := writer.Finalize()
	multiplied := state.mat.HoohashMatrixMultiplicationV101(powHash)
	return toBig(multiplied), multiplied
}

func (state *State) CalculateProofOfWorkValueHoohashV110() (*big.Int, *externalapi.DomainHash) {
	// Run the first pass safely in Go 
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	err := serialization.WriteElement(writer, state.Timestamp)
	if err != nil { panic(err) }
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	err = serialization.WriteElement(writer, state.Nonce)
	if err != nil { panic(err) }

	firstPass := writer.Finalize()

	if state.useCLibrary {
		var result [32]byte
		firstPassBytes := firstPass.ByteSlice()

		// Pass the exact 32-byte Blake3 output directly into the C Matrix Math
		C.HoohashMatrixMultiplication(
			&state.cMatrix[0],
			(*C.uint8_t)(unsafe.Pointer(&firstPassBytes[0])),
			(*C.uint8_t)(unsafe.Pointer(&result[0])),
			C.uint64_t(state.Nonce),
		)

		hash, err := externalapi.NewDomainHashFromByteSlice(result[:])
		if err != nil { panic(err) }
		return toBig(hash), hash
	}

	multiplied := state.floatMat.HoohashMatrixMultiplicationV110(firstPass, state.Nonce)
	return toBig(multiplied), multiplied
}

func (state *State) CalculateProofOfWorkValuePyrinhash() (*big.Int, *externalapi.DomainHash) {
	writer := hashes.PoWHashWriter()
	writer.InfallibleWrite(state.PrevHeader.ByteSlice())
	serialization.WriteElement(writer, state.Timestamp)
	zeroes := [32]byte{}
	writer.InfallibleWrite(zeroes[:])
	serialization.WriteElement(writer, state.Nonce)
	powHash := writer.Finalize()
	hash := state.mat.bHeavyHash(powHash)
	return toBig(hash), hash
}

func (state *State) IncrementNonce() { state.Nonce++ }

func (state *State) CheckProofOfWork(block *externalapi.DomainBlock, powSkip bool) bool {
	powNum, _ := state.CalculateProofOfWorkValue()
	if state.BlockVersion < constants.PoWIntegrityMinVersion {
		return powNum.Cmp(&state.Target) <= 0
	} else if powSkip && state.BlockVersion >= constants.PoWIntegrityMinVersion {
		return powNum.Cmp(&state.Target) <= 0
	} else if state.BlockVersion >= constants.PoWIntegrityMinVersion {
		powHash, err := externalapi.NewDomainHashFromString(block.PoWHash)
		if err != nil { return false }
		if !powHash.Equal(new(externalapi.DomainHash)) {
			submittedPowNum := toBig(powHash)
			if submittedPowNum.Cmp(powNum) == 0 {
				return powNum.Cmp(&state.Target) <= 0
			}
		}
	}
	return false
}

func CheckProofOfWorkByBits(header externalapi.MutableBlockHeader, block *externalapi.DomainBlock, powSkip bool) bool {
	return NewState(header).CheckProofOfWork(block, powSkip)
}

func toBig(hash *externalapi.DomainHash) *big.Int {
	buf := hash.ByteSlice()
	blen := len(buf)
	for i := 0; i < blen/2; i++ {
		buf[i], buf[blen-1-i] = buf[blen-1-i], buf[i]
	}
	return new(big.Int).SetBytes(buf)
}

func BlockLevel(header externalapi.BlockHeader, maxBlockLevel int) int {
	if len(header.DirectParents()) == 0 {
		return maxBlockLevel
	}

	state := NewState(header.ToMutable())

/*
	//  DEBUGGING FOR THE FAILING BLOCK ---
	blockHash := consensushashing.HeaderHash(header)
	if blockHash.String() == "9d0c2b145952b2476530d8faf456025c93358bb9bce775236872c960a5fc0cbe" {
		fmt.Printf("\n======================================================\n")
		fmt.Printf("[DEBUG] INTERCEPTED FAILING BLOCK: %s\n", blockHash.String())
		fmt.Printf("[DEBUG] Version: %d\n", state.BlockVersion)
		fmt.Printf("[DEBUG] Timestamp: %d\n", state.Timestamp)
		fmt.Printf("[DEBUG] Nonce: %d\n", state.Nonce)
		fmt.Printf("[DEBUG] Pre-PoW Hash: %s\n", state.PrevHeader.String())

		if state.BlockVersion >= 5 {
			fmt.Printf("[DEBUG] Go Matrix [0][0]: %f\n", state.floatMat[0][0])

			// 1. ManuallyGo Blake3
			writer := hashes.Blake3HashWriter()
			writer.InfallibleWrite(state.PrevHeader.ByteSlice())
			serialization.WriteElement(writer, state.Timestamp)
			zeroes := [32]byte{}
			writer.InfallibleWrite(zeroes[:])
			serialization.WriteElement(writer, state.Nonce)
			firstPassGo := writer.Finalize()
			fmt.Printf("[DEBUG] Blake3 First-Pass: %s\n", firstPassGo.String())

			// 2. Go Final Hash
			finalGoHash := state.floatMat.HoohashMatrixMultiplicationV110(firstPassGo, state.Nonce)
			fmt.Printf("[DEBUG] Pure Go Final Hash: %s (BitLen: %d)\n", finalGoHash.String(), toBig(finalGoHash).BitLen())

			// 3.  C Final Hash
			var resultC [32]byte
			firstPassBytes := firstPassGo.ByteSlice()
			
			// Copy the Go matrix into a temporary C matrix just for this debug call
			var tempCMat [64][64]C.double
			for i := 0; i < 64; i++ {
				for j := 0; j < 64; j++ {
					tempCMat[i][j] = C.double(state.floatMat[i][j])
				}
			}

			C.HoohashMatrixMultiplication(
				&tempCMat[0],
				(*C.uint8_t)(unsafe.Pointer(&firstPassBytes[0])),
				(*C.uint8_t)(unsafe.Pointer(&resultC[0])),
				C.uint64_t(state.Nonce),
			)
			finalCHash, _ := externalapi.NewDomainHashFromByteSlice(resultC[:])
			fmt.Printf("[DEBUG] Pure C Final Hash : %s (BitLen: %d)\n", finalCHash.String(), toBig(finalCHash).BitLen())
		}
		fmt.Printf("======================================================\n\n")
	}
	// --- END DEBUGGING ---
*/

	proofOfWorkValue, _ := state.CalculateProofOfWorkValue()
	level := max(maxBlockLevel-proofOfWorkValue.BitLen(), 0)
	return level
}
