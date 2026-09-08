package mempool

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"

	"github.com/HoosatNetwork/HTND/util"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

const (
	defaultMaximumTransactionCount = 1_000_000

	// Ten minutes. Sixty seconds proved far too tight for chained transactions: a compounding run
	// submits a new transaction every few seconds, each depending on the last, and expiry takes an
	// expired transaction's dependants with it - so one link missing the window drops the whole
	// chain from every mempool at once. Of twenty-two compounding transactions submitted over two
	// and a half minutes, three survived.
	//
	// This is node-local policy, not consensus, so nodes running different windows interoperate
	// normally - a longer window here simply means this node keeps offering the transaction to
	// miners for longer.
	defaultTransactionExpireIntervalSeconds     uint64 = 600
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
	MaximumTransactionCount uint64

	// Expiry is configured in seconds and converted to DAA score at the point of use, by
	// transactionExpireIntervalDAAScore and friends. It used to be converted once, in DefaultConfig,
	// which was wrong twice over - see those methods.
	TransactionExpireIntervalSeconds     uint64
	TransactionExpireScanIntervalSeconds uint64
	OrphanExpireIntervalSeconds          uint64
	OrphanExpireScanIntervalSeconds      uint64
	MaximumOrphanTransactionMass         uint64
	MaximumOrphanTransactionCount        uint64
	AcceptNonStandard                    bool
	MaximumMassPerBlock                  uint64
	MinimumRelayTransactionFee           util.Amount
	MinimumStandardTransactionVersion    uint16
	MaximumStandardTransactionVersion    uint16

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
	return &Config{
		MaximumTransactionCount:              defaultMaximumTransactionCount,
		TransactionExpireIntervalSeconds:     defaultTransactionExpireIntervalSeconds,
		TransactionExpireScanIntervalSeconds: defaultTransactionExpireScanIntervalSeconds,
		OrphanExpireIntervalSeconds:          defaultOrphanExpireIntervalSeconds,
		OrphanExpireScanIntervalSeconds:      defaultOrphanExpireScanIntervalSeconds,
		MaximumOrphanTransactionMass:         defaultMaximumOrphanTransactionMass,
		MaximumOrphanTransactionCount:        defaultMaximumOrphanTransactionCount,
		AcceptNonStandard:                    dagParams.RelayNonStdTxs,
		MaximumMassPerBlock:                  dagParams.MaxBlockMass[constants.GetBlockVersion()-1],
		MinimumRelayTransactionFee:           defaultMinimumRelayTransactionFee,
		MinimumStandardTransactionVersion:    defaultMinimumStandardTransactionVersion,
		MaximumStandardTransactionVersion:    defaultMaximumStandardTransactionVersion,

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

// blocksPerSecond is the rate the network is currently targeting. Read at the point of use, never
// cached: the block version is a process-global that starts at 1 and is raised later as blocks
// arrive, so a value computed during startup describes version 1 forever.
func (c *Config) blocksPerSecond() float64 {
	targetTimePerBlock := c.DAGParams.TargetTimePerBlockForCurrentVersion().Seconds()
	if targetTimePerBlock <= 0 {
		return 1
	}
	return 1 / targetTimePerBlock
}

// secondsToDAAScore converts a duration in seconds into the number of blocks the network expects to
// produce in that time.
//
// This used to divide by the block rate instead of multiplying by it, which is not a unit
// conversion at all - it yields seconds squared per block. It went unnoticed because it is correct
// at exactly one block per second, which is what the first four block versions target and, because
// the block version defaults to 1 until blocks arrive, what DefaultConfig always saw. The current
// versions target 200ms, so the intended 60-second mempool lifetime was being applied as 60 blocks -
// twelve seconds - and had the block version been read correctly it would have been 12 blocks, or
// under three seconds. Transactions that were not mined almost immediately were dropped from every
// mempool on the network, taking their dependants with them.
func (c *Config) secondsToDAAScore(seconds uint64) uint64 {
	score := uint64(float64(seconds) * c.blocksPerSecond())
	if score == 0 {
		// Never collapse to zero: a zero interval expires everything on the first scan.
		return 1
	}
	return score
}

func (c *Config) transactionExpireIntervalDAAScore() uint64 {
	return c.secondsToDAAScore(c.TransactionExpireIntervalSeconds)
}

func (c *Config) transactionExpireScanIntervalDAAScore() uint64 {
	return c.secondsToDAAScore(c.TransactionExpireScanIntervalSeconds)
}

func (c *Config) orphanExpireIntervalDAAScore() uint64 {
	return c.secondsToDAAScore(c.OrphanExpireIntervalSeconds)
}

func (c *Config) orphanExpireScanIntervalDAAScore() uint64 {
	return c.secondsToDAAScore(c.OrphanExpireScanIntervalSeconds)
}
