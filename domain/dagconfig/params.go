// Copyright (c) 2014-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"math/big"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/util/network"

	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/util"
)

// These variables are the DAG proof-of-work limit parameters for each default
// network.
var (
	// bigOne is 1 represented as a big.Int. It is defined here to avoid
	// the overhead of creating it multiple times.
	bigOne = big.NewInt(1)

	// mainPowMax is the highest proof of work value a Hoosat block can
	// have for the main network. It is the value 2^255 - 1.
	mainPowMax = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 255), bigOne)

	// testnetPowMax is the highest proof of work value a Hoosat block
	// can have for the test network. It is the value 2^255 - 1.
	testnetPowMax = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 255), bigOne)

	// simnetPowMax is the highest proof of work value a Hoosat block
	// can have for the simulation test network. It is the value 2^255 - 1.
	simnetPowMax = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 255), bigOne)

	// devnetPowMax is the highest proof of work value a Hoosat block
	// can have for the development network. It is the value
	// 2^255 - 1.
	devnetPowMax = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 255), bigOne)
)

// KType defines the size of GHOSTDAG consensus algorithm K parameter.
type KType uint8

// Params defines a Hoosat network by its parameters. These parameters may be
// used by Hoosat applications to differentiate networks as well as addresses
// and keys for one network from those intended for use on another network.
type Params struct {
	// K defines the K parameter for GHOSTDAG consensus algorithm.
	// See ghostdag.go for further details.
	K []externalapi.KType

	// Name defines a human-readable identifier for the network.
	Name string

	// Net defines the magic bytes used to identify the network.
	Net appmessage.HoosatNet

	// RPCPort defines the rpc server port
	RPCPort string

	// DefaultPort defines the default peer-to-peer port for the network.
	DefaultPort string

	// DNSSeeds defines a list of DNS seeds for the network that are used
	// as one method to discover peers.
	DNSSeeds []string

	// GRPCSeeds defines a list of GRPC seeds for the network that are used
	// as one method to discover peers.
	GRPCSeeds []string

	// GenesisBlock defines the first block of the DAG.
	GenesisBlock *externalapi.DomainBlock

	// GenesisHash is the starting block hash.
	GenesisHash *externalapi.DomainHash

	// PowMax defines the highest allowed proof of work value for a block
	// as a uint256.
	PowMax *big.Int

	// BlockCoinbaseMaturity is the number of blocks required before newly mined
	// coins can be spent.
	BlockCoinbaseMaturity uint64

	// SubsidyGenesisReward SubsidyMergeSetRewardMultiplier, and
	// SubsidyPastRewardMultiplier are part of the block subsidy equation.
	// Further details: https://hashdag.medium.com/hoosat-launch-plan-9a63f4d754a6
	SubsidyGenesisReward            uint64
	PreDeflationaryPhaseBaseSubsidy uint64
	DeflationaryPhaseBaseSubsidy    uint64
	DeflationaryPhaseCurveFactor    float64

	// TargetTimePerBlock is the desired amount of time to generate each
	// block.
	TargetTimePerBlock []time.Duration

	// FinalityDuration is the duration of the finality window.
	FinalityDuration []time.Duration

	PruningMultiplier []uint64

	// TimestampDeviationTolerance is the maximum offset a block timestamp
	// is allowed to be in the future before it gets delayed
	TimestampDeviationTolerance int

	// DifficultyAdjustmentWindowSize is the size of window that is inspected
	// to calculate the required difficulty of each block.
	DifficultyAdjustmentWindowSize []int

	// These fields are related to voting on consensus rule changes as
	// defined by BIP0009.
	//
	// RuleChangeActivationThreshold is the number of blocks in a threshold
	// state retarget window for which a positive vote for a rule change
	// must be cast in order to lock in a rule change. It should typically
	// be 95% for the main network and 75% for test networks.
	//
	// MinerConfirmationWindow is the number of blocks in each threshold
	// state retarget window.
	//
	// Deployments define the specific consensus rule changes to be voted
	// on.
	RuleChangeActivationThreshold uint64
	MinerConfirmationWindow       uint64

	// Mempool parameters
	RelayNonStdTxs bool

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable bool

	// Human-readable prefix for Bech32 encoded addresses
	Prefix util.Bech32Prefix

	// Address encoding magics
	PrivateKeyID byte // First byte of a WIF private key

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks bool

	// DisableDifficultyAdjustment determine whether to use difficulty
	DisableDifficultyAdjustment bool

	// SkipProofOfWork indicates whether proof of work should be checked.
	SkipProofOfWork bool

	// MaxCoinbasePayloadLength is the maximum length in bytes allowed for a block's coinbase's payload
	MaxCoinbasePayloadLength uint64

	// MaxBlockMass is the maximum mass a block is allowed
	MaxBlockMass []uint64

	// MaxBlockParents is the maximum number of blocks a block is allowed to point to
	MaxBlockParents []externalapi.KType

	// MassPerTxByte is the number of grams that any byte
	// adds to a transaction.
	MassPerTxByte uint64

	// MassPerScriptPubKeyByte is the number of grams that any
	// scriptPubKey byte adds to a transaction.
	MassPerScriptPubKeyByte uint64

	// MassPerSigOp is the number of grams that any
	// signature operation adds to a transaction.
	MassPerSigOp uint64

	// MergeSetSizeLimit is the maximum number of blocks in a block's merge set
	MergeSetSizeLimit uint64

	// CoinbasePayloadScriptPublicKeyMaxLength is the maximum allowed script public key in the coinbase's payload
	CoinbasePayloadScriptPublicKeyMaxLength uint8

	// PruningProofM is the 'm' constant in the pruning proof. For more details see: https://github.com/hoosatnet/research/issues/3
	PruningProofM uint64

	// DeflationaryPhaseDaaScore is the DAA score after which the monetary policy switches
	// to its deflationary phase
	DeflationaryPhaseDaaScore uint64

	DisallowDirectBlocksOnTopOfGenesis bool

	// MaxBlockLevel is the maximum possible block level.
	MaxBlockLevel int

	MergeDepth []uint64

	POWScores []uint64
}

// NormalizeRPCServerAddress returns addr with the current network default
// port appended if there is not already a port specified.
func (p *Params) NormalizeRPCServerAddress(addr string) (string, error) {
	return network.NormalizeAddress(addr, p.RPCPort)
}

func currentBlockVersionIndexForSlice(length int) int {
	if length <= 0 {
		panic("dagconfig: attempted to index empty per-version parameter slice")
	}

	index := int(constants.GetBlockVersion()) - 1
	if index < 0 {
		index = 0
	}
	if index >= length {
		index = length - 1
	}
	return index
}

func (p *Params) targetTimePerBlockForCurrentVersion() time.Duration {
	return p.TargetTimePerBlock[currentBlockVersionIndexForSlice(len(p.TargetTimePerBlock))]
}

func (p *Params) finalityDurationForCurrentVersion() time.Duration {
	return p.FinalityDuration[currentBlockVersionIndexForSlice(len(p.FinalityDuration))]
}

func (p *Params) ghostdagKForCurrentVersion() externalapi.KType {
	return p.K[currentBlockVersionIndexForSlice(len(p.K))]
}

func (p *Params) pruningMultiplierForCurrentVersion() uint64 {
	return p.PruningMultiplier[currentBlockVersionIndexForSlice(len(p.PruningMultiplier))]
}

/*
	Block version index must be -1 because blockVersions start at 1 and index from 0.
	blockVersion = index
	1 = 0
	2 = 1
	3 = 2
	4 = 3
	5 = 4
*/
// FinalityDepth returns the finality duration represented in blocks
func (p *Params) FinalityDepth() uint64 {
	finalityDuration := p.finalityDurationForCurrentVersion()
	targetTimePerBlock := p.targetTimePerBlockForCurrentVersion()
	if constants.GetBlockVersion() < 5 {
		return uint64(finalityDuration / targetTimePerBlock)
	}
	return uint64(finalityDuration.Seconds() / targetTimePerBlock.Seconds())
}

// PruningDepth returns the pruning duration represented in blocks
func (p *Params) PruningDepth() uint64 {
	k := uint64(p.ghostdagKForCurrentVersion())
	if constants.GetBlockVersion() < 5 {
		return 2*p.FinalityDepth() + 4*p.MergeSetSizeLimit*k + 2*k + 2
	}
	return 2*p.FinalityDepth()*p.pruningMultiplierForCurrentVersion() + 4*p.MergeSetSizeLimit*k + 2*k + 2
}

// MainnetParams defines the network parameters for the main Hoosat network.
var MainnetParams = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-mainnet",
	Net:         appmessage.Mainnet,
	RPCPort:     "42420",
	DefaultPort: "42421",
	DNSSeeds: []string{
		// This DNS seeder is run by Toni Lukkaroinen
		"mainnet-dnsseed.hoosat.fi",
		// These DNS seeders are run by Cryptonoob
		"mainnet-node-1.hoosat.org",
		"mainnet-node-2.hoosat.org",
		"mainnet-node-3.hoosat.org",
		"mainnet-node-4.hoosat.org",
		//These DNS seeders are ran by Evern00b
		"hoosat.seed-fi.evern00b.com",
		"hoosat.seed-de.evern00b.com",
		"hoosat.seed-in.evern00b.com",
	},

	// DAG parameters
	GenesisBlock:                    &genesisBlock,
	GenesisHash:                     genesisHash,
	PowMax:                          mainPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseCurveFactor:    defaultDeflationaryPhaseCurveFactor,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 200 * time.Millisecond, 200 * time.Millisecond},
	FinalityDuration:                []time.Duration{defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, 10800 * time.Second, 10800 * time.Second},
	DifficultyAdjustmentWindowSize:  []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, 2640, 2640},
	TimestampDeviationTolerance:     defaultTimestampDeviationTolerance,
	POWScores:                       []uint64{17500000, 21821800, 29335426, 43334184},
	PruningMultiplier:               []uint64{0, 0, 0, 0, 1, 1},
	MaxBlockMass:                    []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, 1_000_000, 1_000_000},

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1916, // 95% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016, //

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosat,

	// Address encoding magics
	PrivateKeyID: 0x80, // starts with 5 (uncompressed) or K (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600},
}

// TestnetParams defines the network parameters for the test Hoosat network.
var TestnetParams = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-testnet",
	Net:         appmessage.Testnet,
	RPCPort:     "42422",
	DefaultPort: "42423",
	DNSSeeds: []string{
		// This DNS seeder is run by Toni Lukkaroinen
		"mainnet-dnsseed.hoosat.fi",
		// These DNS seeders are run by Cryptonoob
		"mainnet-node-1.hoosat.org",
		"mainnet-node-2.hoosat.org",
		"mainnet-node-3.hoosat.org",
		"mainnet-node-4.hoosat.org",
	},

	// DAG parameters
	GenesisBlock:                    &genesisBlock,
	GenesisHash:                     genesisHash,
	PowMax:                          mainPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseCurveFactor:    defaultDeflationaryPhaseCurveFactor,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 200 * time.Millisecond, 200 * time.Millisecond},
	FinalityDuration:                []time.Duration{defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, 10800 * time.Second, 10800 * time.Second},
	DifficultyAdjustmentWindowSize:  []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, 2640, 2640},
	TimestampDeviationTolerance:     defaultTimestampDeviationTolerance,
	POWScores:                       []uint64{1, 2, 3, 4},
	PruningMultiplier:               []uint64{0, 0, 0, 0, 1, 1},
	MaxBlockMass:                    []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, 1_000_000, 1_000_000},

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1916, // 95% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016, //

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosatTest,

	// Address encoding magics
	PrivateKeyID: 0x80, // starts with 5 (uncompressed) or K (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600},
}

var TestnetParamsB5 = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-testnet-b5",
	Net:         appmessage.Testnet,
	RPCPort:     "42422",
	DefaultPort: "42423",
	DNSSeeds: []string{
		// This DNS seeder is ran by Toni Lukkaroinen
		"mainnet-dnsseed.hoosat.fi",
		// These DNS seeders are ran by Cryptonoob
		"mainnet-node-1.hoosat.org",
		"mainnet-node-2.hoosat.org",
		"mainnet-node-3.hoosat.org",
		"mainnet-node-4.hoosat.org",
	},

	// DAG parameters
	GenesisBlock:                    &testnetGenesisBlock,
	GenesisHash:                     testnetGenesisHash,
	PowMax:                          testnetPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 200 * time.Millisecond, 200 * time.Millisecond},
	FinalityDuration:                []time.Duration{defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, 10800 * time.Second, 10800 * time.Second},
	DifficultyAdjustmentWindowSize:  []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize},
	TimestampDeviationTolerance:     defaultTimestampDeviationTolerance,
	POWScores:                       []uint64{5, 15, 25, 30},
	PruningMultiplier:               []uint64{0, 0, 0, 0, 1, 1},
	MaxBlockMass:                    []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, 1_000_000, 1_000_000},

	// Consensus rule change deployments.s
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1512, // 75% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016,

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosatTest,

	// Address encoding magics
	PrivateKeyID: 0xef, // starts with 9 (uncompressed) or c (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit * 5,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,

	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600},
}

var TestnetParamsB10 = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-testnet-b10",
	Net:         appmessage.Testnet,
	RPCPort:     "42422",
	DefaultPort: "42423",
	DNSSeeds: []string{
		// This DNS seeder is run by Toni Lukkaroinen
		"mainnet-dnsseed.hoosat.fi",
		// These DNS seeders are run by Cryptonoob
		"mainnet-node-1.hoosat.org",
		"mainnet-node-2.hoosat.org",
		"mainnet-node-3.hoosat.org",
		"mainnet-node-4.hoosat.org",
	},

	// DAG parameters
	GenesisBlock:                    &testnetGenesisBlock,
	GenesisHash:                     testnetGenesisHash,
	PowMax:                          testnetPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 100 * time.Millisecond},
	FinalityDuration:                []time.Duration{defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, 28800 * time.Second},
	DifficultyAdjustmentWindowSize:  []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize},
	TimestampDeviationTolerance:     defaultTimestampDeviationTolerance,
	POWScores:                       []uint64{5, 15, 25, 30},
	PruningMultiplier:               []uint64{0, 0, 0, 0, 3},
	MaxBlockMass:                    []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, 1_000_000, 1_000_000},

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1512, // 75% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016,

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosatTest,

	// Address encoding magics
	PrivateKeyID: 0xef, // starts with 9 (uncompressed) or c (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 16},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit * 10,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,

	MaxBlockLevel: 250,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600},
}

// SimnetParams defines the network parameters for the simulation test Hoosat
// network. This network is similar to the normal test network except it is
// intended for private use within a group of individuals doing simulation
// testing. The functionality is intended to differ in that the only nodes
// which are specifically specified are used to create the network rather than
// following normal discovery rules. This is important as otherwise it would
// just turn into another public testnet.
var SimnetParams = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-simnet",
	Net:         appmessage.Simnet,
	RPCPort:     "42424",
	DefaultPort: "42425",
	DNSSeeds:    []string{}, // NOTE: There must NOT be any seeds.

	// DAG parameters
	GenesisBlock:                    &simnetGenesisBlock,
	GenesisHash:                     simnetGenesisHash,
	PowMax:                          simnetPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 200 * time.Millisecond, 200 * time.Millisecond},
	// Must have at least as many entries as the maximum block version reachable on this network.
	// Simnet transitions to block version 2 after DAA score >= 5 (see POWScores below).
	FinalityDuration:               []time.Duration{defaultFinalityDuration, defaultFinalityDuration},
	DifficultyAdjustmentWindowSize: []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, 264},
	TimestampDeviationTolerance:    defaultTimestampDeviationTolerance,
	POWScores:                      []uint64{5},
	PruningMultiplier:              []uint64{0, 0, 0, 0, 48},
	MaxBlockMass:                   []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass},

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 75, // 75% of MinerConfirmationWindow
	MinerConfirmationWindow:       100,

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	PrivateKeyID: 0x64, // starts with 4 (uncompressed) or F (compressed)
	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosatSim,

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: true,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 40},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,

	MaxBlockLevel: 250,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, defaultMergeDepth},
}

// DevnetParams defines the network parameters for the development Hoosat network.
var DevnetParams = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40},
	Name:        "hoosat-devnet",
	Net:         appmessage.Devnet,
	RPCPort:     "42426",
	DefaultPort: "42427",
	DNSSeeds:    []string{}, // NOTE: There must NOT be any seeds.

	// DAG parameters
	GenesisBlock:                    &genesisBlock,
	GenesisHash:                     genesisHash,
	PowMax:                          mainPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseCurveFactor:    defaultDeflationaryPhaseCurveFactor,
	TargetTimePerBlock:              []time.Duration{defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, defaultTargetTimePerBlock, 200 * time.Millisecond, 200 * time.Millisecond},
	FinalityDuration:                []time.Duration{defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, defaultFinalityDuration, 10800 * time.Second, 10800 * time.Second},
	DifficultyAdjustmentWindowSize:  []int{defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, defaultDifficultyAdjustmentWindowSize, 2640, 2640},
	TimestampDeviationTolerance:     defaultTimestampDeviationTolerance,
	POWScores:                       []uint64{17500000, 21821800, 29335426, 43334184},
	PruningMultiplier:               []uint64{0, 0, 0, 0, 1, 1},
	MaxBlockMass:                    []uint64{defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, defaultMaxBlockMass, 1_000_000, 1_000_000},

	// Consensus rule change deployments.
	//
	// The miner confirmation window is defined as:
	//   target proof of work timespan / target proof of work spacing
	RuleChangeActivationThreshold: 1916, // 95% of MinerConfirmationWindow
	MinerConfirmationWindow:       2016, //

	// Mempool parameters
	RelayNonStdTxs: false,

	// AcceptUnroutable specifies whether this network accepts unroutable
	// IP addresses, such as 10.0.0.0/8
	AcceptUnroutable: false,

	// Human-readable part for Bech32 encoded addresses
	Prefix: util.Bech32PrefixHoosat,

	// Address encoding magics
	PrivateKeyID: 0x80, // starts with 5 (uncompressed) or K (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600},
}

// ErrDuplicateNet describes an error where the parameters for a Hoosat
// network could not be set due to the network already being a standard
// network or previously-registered into this package.
var ErrDuplicateNet = errors.New("duplicate Hoosat network")

var registeredNets = make(map[appmessage.HoosatNet]struct{})

// Register registers the network parameters for a Hoosat network. This may
// error with ErrDuplicateNet if the network is already registered (either
// due to a previous Register call, or the network being one of the default
// networks).
//
// Network parameters should be registered into this package by a main package
// as early as possible. Then, library packages may lookup networks or network
// parameters based on inputs and work regardless of the network being standard
// or not.
func Register(params *Params) error {
	if _, ok := registeredNets[params.Net]; ok {
		return ErrDuplicateNet
	}
	registeredNets[params.Net] = struct{}{}

	return nil
}

// mustRegister performs the same function as Register except it panics if there
// is an error. This should only be called from package init functions.
func mustRegister(params *Params) {
	if err := Register(params); err != nil {
		panic("failed to register network: " + err.Error())
	}
}

func init() {
	// Register all default networks when the package is initialized.
	mustRegister(&MainnetParams)
	mustRegister(&TestnetParams)
	mustRegister(&SimnetParams)
	mustRegister(&DevnetParams)
}
