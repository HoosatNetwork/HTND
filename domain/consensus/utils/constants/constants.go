package constants

import (
	"math"
	"sync/atomic"
)

var (
	// BlockVersion represents the current block version. Use GetBlockVersion/SetBlockVersion
	// to access it atomically.
	// 1 Pyrinhash
	// 2 HoohashV1
	// 3 HoohashV1.0.1
	// 4 HoohashV1.0.1 + Pow hash validation network wide
	// 5 HoohashV1.1.0 + Pow hash validation network wide
	// 6 HoohashV1.1.0 + additional features
	blockVersion uint32 = 1

	PoWIntegrityMinVersion uint16 = 4
	BanMinVersion          uint16 = 5
)

// GetBlockVersion returns the current block version (atomic load).
func GetBlockVersion() uint16 {
	v := atomic.LoadUint32(&blockVersion)
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

// SetBlockVersion sets the current block version (atomic store).
func SetBlockVersion(v uint16) {
	current := atomic.LoadUint32(&blockVersion)
	if uint32(v) > current {
		log.Infof("Set block version to %d", v)
		atomic.StoreUint32(&blockVersion, uint32(v))
	}
}

func ForceSetBlockVersion(v uint) {
	// Prevent overflow: only store if v fits in uint32
	if v > uint(^uint32(0)) {
		panic("ForceSetBlockVersion: value overflows uint32")
	}
	atomic.StoreUint32(&blockVersion, uint32(v))
}

var BannedAddresses = []string{
	"",
}

const (
	DevFee        = 5
	DevFeeMin     = 1
	DevFeeAddress = "hoosat:qp4ad2eh72xc8dtjjyz4llxzq9utn6k26uyl644xxw70wskdfl85zsqj9k4vz"

	// MaxTransactionVersion is the current latest supported transaction version.
	MaxTransactionVersion uint16 = 0

	// MaxScriptPublicKeyVersion is the current latest supported public key script version.
	MaxScriptPublicKeyVersion uint16 = 0

	// SompiPerHoosat is the number of sompi in one hoosat (1 HTN).
	SompiPerHoosat = 100_000_000

	// MaxSompi is the maximum transaction amount allowed in sompi.
	MaxSompi = uint64(17_100_000_000 * SompiPerHoosat)

	// MaxTxInSequenceNum is the maximum sequence number the sequence field
	// of a transaction input can be.
	MaxTxInSequenceNum uint64 = math.MaxUint64

	// SequenceLockTimeDisabled is a flag that if set on a transaction
	// input's sequence number, the sequence number will not be interpreted
	// as a relative locktime.
	SequenceLockTimeDisabled uint64 = 1 << 63

	// SequenceLockTimeMask is a mask that extracts the relative locktime
	// when masked against the transaction input sequence number.
	SequenceLockTimeMask uint64 = 0x00000000ffffffff

	// LockTimeThreshold is the number below which a lock time is
	// interpreted to be a DAA score.
	LockTimeThreshold = 5e11 // Tue Nov 5 00:53:20 1985 UTC

	// UnacceptedDAAScore is used to for UTXOEntries that were created by transactions in the mempool, or otherwise
	// not-yet-accepted transactions.
	UnacceptedDAAScore = math.MaxUint64
)
