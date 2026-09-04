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

const DomainHashSize = 32

func BlockVersionProgressed(old uint32, new uint16) {
	if old >= 3 && new == 6 {
		// Generated with https://patorjk.com/software/taag/#p=display&f=Graffiti&t=Zenith+Hard+Fork+Active&x=none&v=4&h=4&w=80&we=false
		log.Info("__________            .__  __  .__        ___ ___                  .___ ___________           __        _____          __  .__              ")
		log.Info("\\____    /____   ____ |__|/  |_|  |__    /   |   \\_____ _______  __| _/ \\_   _____/__________|  | __   /  _  \\   _____/  |_|__|__  __ ____  ")
		log.Info("  /     // __ \\ /    \\|  \\   __\\  |  \\  /    ~    \\__  \\_   __ \\/ __ |   |    __)/  _ \\_  __ \\  |/ /  /  /_\\  \\_/ ___\\   __\\  \\  \\/ // __ \\ ")
		log.Info(" /     /\\  ___/|   |  \\  ||  | |   Y  \\ \\    Y    // __ \\|  | \\/ /_/ |   |     \\(  <_> )  | \\/    <  /    |    \\  \\___|  | |  |\\   /\\  ___/ ")
		log.Info("/_______ \\___  >___|  /__||__| |___|  /  \\___|_  /(____  /__|  \\____ |   \\___  / \\____/|__|  |__|_ \\ \\____|__  /\\___  >__| |__| \\_/  \\___  >")
		log.Info("        \\/   \\/     \\/              \\/         \\/      \\/           \\/       \\/                   \\/         \\/     \\/                   \\/ ")
	}
}

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
		BlockVersionProgressed(current, v)
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

	// MaxDAGKnightTips is the maximum number of tips to pass to DAGKnight OrderDAG.
	// With O(n^2) complexity, keeping this under ~200 ensures reasonable performance.
	// 200 tips = ~20K pairwise comparisons, 500 tips = ~125K, 1000 tips = ~500K.
	MaxDAGKnightTips = 24

	// MaxOrderDAGSize is the absolute maximum number of blocks that OrderDAG will process.
	// If the input exceeds this, it will be limited by taking top blocks by blue score.
	// This prevents pathological cases where deep recursion with large sets causes slowdowns.
	MaxOrderDAGSize = 200

	// MaxKColouringSize is the maximum number of blocks for KColouring computation.
	// KColouring is recursive and expensive, so limiting its input size is critical.
	MaxKColouringSize = 100
)

// BlockVersionForDAAScore returns the block version that a block with the given DAA score is
// expected to have, per powScores.
//
// Prefer this over the ambient GetBlockVersion() wherever a value is being derived FOR A SPECIFIC
// BLOCK - a consensus parameter, a validation bound, a coloring input. blockVersion is a
// process-global one-way ratchet that starts at 1 on every restart and only rises as blocks arrive,
// so it reflects how long this process has been running and what it happened to see, not the
// version that governs the block at hand. Indexing a per-version parameter table with it therefore
// yields a different answer on a freshly restarted node than on one that has been up for a while,
// for the very same block.
func BlockVersionForDAAScore(powScores []uint64, daaScore uint64) uint16 {
	var version uint16 = 1
	for _, powScore := range powScores {
		if daaScore >= powScore {
			version++
		}
	}
	return version
}
