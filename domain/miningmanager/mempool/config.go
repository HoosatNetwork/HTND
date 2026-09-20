package mempool

import (
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"

	"github.com/HoosatNetwork/HTND/util"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

const (
	defaultMaximumTransactionCount = 1_000_000

	defaultTransactionExpireIntervalSeconds     uint64 = 60
	defaultTransactionExpireScanIntervalSeconds uint64 = 10
	defaultOrphanExpireIntervalSeconds          uint64 = 60
	defaultOrphanExpireScanIntervalSeconds      uint64 = 10

	defaultMaximumOrphanTransactionMass = 100000
	// defaultMaximumOrphanTransactionCount should remain small as long as we have recursion in
	// removeOrphans when removeRedeemers = true
	defaultMaximumOrphanTransactionCount = 50

	// defaultMinimumRelayTransactionFee specifies the minimum transaction fee for a transaction to be accepted to
	// the mempool and relayed. It is specified in sompi per 1kg (or 1000 grams) of transaction mass.
	defaultMinimumRelayTransactionFee = util.Amount(1000)

	// Compound transaction rate limiting defaults
	defaultCompoundTxRateLimitEnabled       = true
	defaultMaxCompoundTxPerAddressPerMinute = uint64(10) // Max 10 compound transactions per address per minute as in 10 blocks of 300 blocks.
	defaultCompoundTxRateLimitWindowMinutes = uint64(1)  // 1-minute sliding window
	defaultCompoundTxMinInputsThreshold     = uint64(21) // Consider transactions with 10+ inputs as potential compound

	// Standard transaction version range might be different from what consensus accepts, therefore
	// we define separate values in mempool.
	// However, currently there's exactly one transaction version, so mempool accepts the same version
	// as consensus.
	defaultMinimumStandardTransactionVersion = constants.MaxTransactionVersion
	defaultMaximumStandardTransactionVersion = constants.MaxTransactionVersion
)

// Config represents a mempool configuration
type Config struct {
	MaximumTransactionCount               uint64
	TransactionExpireIntervalDAAScore     uint64
	TransactionExpireScanIntervalDAAScore uint64
	TransactionExpireScanIntervalSeconds  uint64
	OrphanExpireIntervalDAAScore          uint64
	OrphanExpireScanIntervalDAAScore      uint64
	MaximumOrphanTransactionMass          uint64
	MaximumOrphanTransactionCount         uint64
	AcceptNonStandard                     bool
	MaximumMassPerBlock                   uint64
	MinimumRelayTransactionFee            util.Amount
	MinimumStandardTransactionVersion     uint16
	MaximumStandardTransactionVersion     uint16

	// Compound transaction rate limiting configuration
	CompoundTxRateLimitEnabled       bool
	MaxCompoundTxPerAddressPerMinute uint64
	CompoundTxRateLimitWindowMinutes uint64
	CompoundTxMinInputsThreshold     uint64

	// DAG/network parameters used for address encoding/decoding and script parsing
	DAGParams *dagconfig.Params

	// Wallet freezing configuration
	WalletFreezingEnabled bool
	FrozenAddresses       []string
}

// DefaultConfig returns the default mempool configuration
func DefaultConfig(dagParams *dagconfig.Params) *Config {
	targetBlocksPerSecond := time.Second.Seconds() / dagParams.TargetTimePerBlock[constants.GetBlockVersion()-1].Seconds()

	return &Config{
		MaximumTransactionCount:               defaultMaximumTransactionCount,
		TransactionExpireIntervalDAAScore:     uint64(float64(defaultTransactionExpireIntervalSeconds) / targetBlocksPerSecond),
		TransactionExpireScanIntervalDAAScore: uint64(float64(defaultTransactionExpireScanIntervalSeconds) / targetBlocksPerSecond),
		TransactionExpireScanIntervalSeconds:  defaultTransactionExpireScanIntervalSeconds,
		OrphanExpireIntervalDAAScore:          uint64(float64(defaultOrphanExpireIntervalSeconds) / targetBlocksPerSecond),
		OrphanExpireScanIntervalDAAScore:      uint64(float64(defaultOrphanExpireScanIntervalSeconds) / targetBlocksPerSecond),
		MaximumOrphanTransactionMass:          defaultMaximumOrphanTransactionMass,
		MaximumOrphanTransactionCount:         defaultMaximumOrphanTransactionCount,
		AcceptNonStandard:                     dagParams.RelayNonStdTxs,
		MaximumMassPerBlock:                   dagParams.MaxBlockMass[constants.GetBlockVersion()-1],
		MinimumRelayTransactionFee:            defaultMinimumRelayTransactionFee,
		MinimumStandardTransactionVersion:     defaultMinimumStandardTransactionVersion,
		MaximumStandardTransactionVersion:     defaultMaximumStandardTransactionVersion,

		// Compound transaction rate limiting
		CompoundTxRateLimitEnabled:       defaultCompoundTxRateLimitEnabled,
		MaxCompoundTxPerAddressPerMinute: defaultMaxCompoundTxPerAddressPerMinute,
		CompoundTxRateLimitWindowMinutes: defaultCompoundTxRateLimitWindowMinutes,
		CompoundTxMinInputsThreshold:     defaultCompoundTxMinInputsThreshold,

		// DAG params
		DAGParams: dagParams,

		// Wallet freezing
		WalletFreezingEnabled: true,
		FrozenAddresses: []string{
			"hoosat:qpkcfshjeazmwex3t7x7qlctmhhratqauhkd5j254vfnmnuec7k6q4yzppn5q", // Frozen wallet address
		},
	}
}
