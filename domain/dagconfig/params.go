// Copyright (c) 2014-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"math/big"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/util/network"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/util"
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

	// PruningPointCheckpoint is a permanent, release-pinned stamp: a block, with its blue score, DAA score
	// and UTXO commitment, that every pruning point imported by IBD must be at or descended from. nil
	// disables the check. It is trusted, not derived; once shipped it must never be moved or removed.
	// Foztor - validated as present on mainnet 5 October 27 and x referened to var|HTN
	PruningPointCheckpoint *Checkpoint

	// UnpricedTransactionFeeAllowance is how much, per merge-set transaction whose fee this node cannot
	// compute (accepted with missing inputs, or not accepted here), a coinbase may exceed the expected
	// coinbase on a node with an offset UTXO baseline, from the block version at which
	// HardForkGates.OffsetModeValueChecksVersion activates the offset-mode value checks. It bounds the
	// fee a miner with a more complete UTXO set may legitimately claim for transactions this node
	// cannot fully price. Blocks below that version are unaffected by it.
	UnpricedTransactionFeeAllowance uint64

	// HardForkGates is the block version at which each version-gated consensus rule activates on
	// this network.
	HardForkGates HardForkGates
}

// MLDSA44SignaturesActive reports whether ML-DSA-44 signatures are valid in a block of blockVersion.
func (p *Params) MLDSA44SignaturesActive(blockVersion uint16) bool {
	gate := p.HardForkGates.MLDSA44SignaturesBlockVersion
	return gate != 0 && blockVersion >= gate
}

// HardForkGates holds the block versions at which each version-gated consensus rule activates on a
// network, and the parameters those rules take. Each network declares its own set (see
// mainnetHardForkGates and testnetHardForkGates), so a rule can be scheduled on testnet without
// touching mainnet. A block version only exists once the network's POWScores has an entry reaching
// it, so a gate at a version POWScores does not define never fires.
//
// Each consensus copies the gates from its Config when it is built and shares that copy with every
// process that reads one. Production code never assigns to the gates outside these declarations
// (build_and_test.sh enforces that), except that a custom network's JSON config may set
// MLDSA44SignaturesBlockVersion. Tests activate a rule at a version their blocks reach through
// TestConsensus.HardForkGates.
type HardForkGates struct {
	// StrictUTXOCommitmentVersion activates HTN-002/HTN-004: from this block version,
	// verifyAndBuildUTXO stops swallowing RuleErrors from the UTXO commitment, accepted-ID merkle
	// root, coinbase and body-vs-past-UTXO checks on a node running an inherited-offset baseline.
	StrictUTXOCommitmentVersion uint16

	// StrictMinersViewFieldsVersion ends the miner's-view toleration: from this block version, a
	// block whose only failures are its UTXO commitment or accepted-ID merkle root is disqualified.
	// Those two fields report the miner's UTXO history and move no value, and mainnet mining nodes do
	// not share one history, so a node enforcing them alone disqualifies the chain the network builds
	// on. It is separate from StrictUTXOCommitmentVersion so the value-moving checks can be enforced
	// without it, and can only activate once every mining node commits the same multiset.
	StrictMinersViewFieldsVersion uint16

	// StrictCoinbaseVersion ends the offset-baseline coinbase allowance: from this block version,
	// the coinbase must exactly match the value this node computes, including when some merge-set
	// transaction fees cannot be priced locally.
	StrictCoinbaseVersion uint16

	// RefuseMismatchedImportVersion activates HTN-005: from this block version, a local pruning-point
	// advancement or imported pruning-point UTXO set whose MuHash disagrees with the commitment is
	// refused rather than accepted-and-repaired, and this node refuses to serve such a set onward.
	// This is separate from the operator flag --enable-sanity-check-pruning-utxo, which is unchanged.
	RefuseMismatchedImportVersion uint16

	// ValidateHeaderBitsVersion activates HTN-007: from this block version, a header's bits must
	// equal the difficulty this node computes for it.
	ValidateHeaderBitsVersion uint16

	// ValidateIBDPruningPointVersion activates the first half of HTN-006: from this block version, an
	// imported pruning point is checked with IsValidPruningPoint, which requires it to be on the
	// selected chain of the headers selected tip, at least pruning depth below it.
	ValidateIBDPruningPointVersion uint16

	// ValidateIBDPruningListVersion activates the second half of HTN-006: from this block version,
	// the newest end of the pruning point list stored with an imported pruning point is checked with
	// ArePruningPointsInValidChain against the pruning point headers' commitments: the current pruning
	// point and the one previous pruning point its header commits to, not the whole list. Those commitments
	// are trusted only from HeaderPruningPointVersion on, so this must activate no earlier than that.
	ValidateIBDPruningListVersion uint16

	// OffsetModeValueChecksVersion activates the offset-mode value checks: from this block version,
	// on a node whose UTXO baseline is offset, (1) a transaction accepted despite missing inputs must
	// pass every check its found inputs can decide and its outputs may not exceed its found inputs,
	// and (2) ErrBadCoinbaseTransaction is only tolerated for a coinbase of the expected shape
	// exceeding the expected amounts by at most UnpricedTransactionFeeAllowance per transaction this
	// node could not price. See consensusstatemanager/offset_value_checks.go.
	OffsetModeValueChecksVersion uint16

	// MLDSA44SignaturesBlockVersion is the block version from which post-quantum ML-DSA-44
	// (FIPS 204) signatures are consensus-valid: opcode 0xa6 executes as OP_CHECKSIGMLDSA44
	// instead of failing as an unknown opcode, counts as one signature operation, and the ML-DSA-44
	// public key and signature pushes are exempted from MaxScriptElementSize.
	//
	// The version has to be derived from the DAA score of the block being validated (see
	// constants.BlockVersionForDAAScore), never from the process-global version or a header field.
	// It only takes effect once POWScores has an entry that reaches it; until then no block can have
	// this version and the rule stays off.
	MLDSA44SignaturesBlockVersion uint16

	// The gates below re-enable header and structural checks that were commented out with no
	// version gate (see blockvalidator). Each was off long enough that the existing chain may
	// violate it, so each applies only from its own block version onward. Like every other gate,
	// the version is derived from the selected parent's DAA score as this node computed it, and
	// blocks with trusted data are exempt, as they were upstream.

	// ParentsIncestVersion activates checkParentsIncest: no direct parent may be an ancestor of
	// another.
	ParentsIncestVersion uint16

	// MergeSetSizeLimitVersion activates checkMergeSizeLimit: a block's merge set may not exceed
	// MergeSetSizeLimit.
	MergeSetSizeLimitVersion uint16

	// HeaderDAAScoreVersion activates checkDAAScore (HTN-006): a header's DAA score must equal the
	// one this node computes for it.
	HeaderDAAScoreVersion uint16

	// HeaderBlueWorkVersion activates checkBlueWork (HTN-006): a header's blue work must equal the
	// blue work GHOSTDAG computes for it.
	HeaderBlueWorkVersion uint16

	// HeaderBlueScoreVersion activates checkHeaderBlueScore (HTN-006): a header's blue score must
	// equal the blue score GHOSTDAG computes for it.
	HeaderBlueScoreVersion uint16

	// HeaderPruningPointVersion activates validateHeaderPruningPoint (HTN-001): a header's pruning
	// point must equal the one this node expects from its selected parent.
	HeaderPruningPointVersion uint16

	// IndirectParentsVersion activates checkIndirectParents: a header's parents at every level above
	// 0 must equal the ones this node builds from its direct parents.
	IndirectParentsVersion uint16
}

// unscheduledHardForkGate is a gate no block version can reach.
const unscheduledHardForkGate = ^uint16(0)

// mainnetHardForkGates schedules the gated rules on mainnet. A change here is a mainnet hard fork:
// it needs a coordinated activation with enough lead time for every node operator and miner.
var mainnetHardForkGates = HardForkGates{
	StrictUTXOCommitmentVersion:    10, // Let's stop the rot now, and not wait for V11 HF
	StrictMinersViewFieldsVersion:  11, // Foztor - Octover 8th, let's do this.
	StrictCoinbaseVersion:          11, // Not seen in practice, activate and enforce at block version 11
	RefuseMismatchedImportVersion:  10, // Similar, stop the rot at v10
	ValidateHeaderBitsVersion:      10, // Stop the rot at v10
	ValidateIBDPruningPointVersion: 10, // We start this today to do the checkpoints during IBD
	ValidateIBDPruningListVersion:  10, // We start this today to do the checkpoints during IBD
	OffsetModeValueChecksVersion:   10, // Already implemented
	MLDSA44SignaturesBlockVersion:  15, // This is postponed until some later point.  Foztor. 5/Oct/27
	ParentsIncestVersion:           10,
	MergeSetSizeLimitVersion:       10,
	HeaderDAAScoreVersion:          10,
	HeaderBlueWorkVersion:          10,
	HeaderBlueScoreVersion:         10,
	HeaderPruningPointVersion:      10,
	IndirectParentsVersion:         10,
}

// Foztor October 2027.   Something to anchor onto in the absence of a sensible way to walk back to genesis
// Checkpoint pins one block of a network's history.
type Checkpoint struct {
	Hash           *externalapi.DomainHash
	BlueScore      uint64
	DAAScore       uint64
	UTXOCommitment *externalapi.DomainHash
}

func mustHash(hashString string) *externalapi.DomainHash {
	hash, err := externalapi.NewDomainHashFromString(hashString)
	if err != nil {
		panic(err)
	}
	return hash
}

// mainnetPruningPointCheckpoint is the stamp agreed by the node operators: a block on the mainnet selected
// chain that was a pruning point. It stands in for genesis, which the pruning point list cannot be walked back to.
// Setting all historical blocks to ver 10 is not an appropraite way to fake a walk back to genesis

var mainnetPruningPointCheckpoint = &Checkpoint{
	Hash:           mustHash("27c1163f701f881ed90560e63031156c29d99100acc40ad019e0fadc61fb43b5"),
	BlueScore:      221022005,
	DAAScore:       233742961,
	UTXOCommitment: mustHash("f5072e6ddf17067bb05a5a99ee095fac922d285c0d0318f42275989b55b9ffff"),
}

// testnetHardForkGates schedules the gated rules on testnet and the other test networks. It may run
// ahead of mainnetHardForkGates to exercise a rule before it is scheduled on mainnet.
//
// ValidateIBDPruningListVersion is unscheduled: the old ArePruningPointsInValidChain failed every
// headers-proof IBD, so it could not ride version 12 with ValidateIBDPruningPointVersion. Schedule
// it no earlier than HeaderPruningPointVersion.
var testnetHardForkGates = HardForkGates{
	StrictUTXOCommitmentVersion:    10,
	StrictMinersViewFieldsVersion:  10,
	StrictCoinbaseVersion:          10,
	RefuseMismatchedImportVersion:  12,
	ValidateHeaderBitsVersion:      12,
	ValidateIBDPruningPointVersion: 12,
	ValidateIBDPruningListVersion:  unscheduledHardForkGate,
	OffsetModeValueChecksVersion:   10,
	MLDSA44SignaturesBlockVersion:  15, // This is postponed until some later point. Foztor 5/Oct/27

	ParentsIncestVersion:      10,
	MergeSetSizeLimitVersion:  10,
	HeaderDAAScoreVersion:     10,
	HeaderBlueWorkVersion:     10,
	HeaderBlueScoreVersion:    10,
	HeaderPruningPointVersion: 10,
	IndirectParentsVersion:    10,
}

// HardForkActive reports whether the rule gated at activationVersion applies to a block of
// blockVersion.
//
// blockVersion must be a version this node derived itself from a DAA score it computed - via
// constants.BlockVersionForDAAScore, blockversion.OfSelectedParent, or an equivalent - and never the
// version field of a peer-supplied header. Every gated rule adds strictness, so keying one on an
// attacker-chosen field would let any miner opt out of it by claiming an older version.
func HardForkActive(activationVersion, blockVersion uint16) bool {
	return blockVersion >= activationVersion
}

// defaultUnpricedTransactionFeeAllowance is 0.1 HTN per unpriced transaction - about ten times the fee
// htnwallet pays for a full 88-input compound (10,000 sompi per input).
const defaultUnpricedTransactionFeeAllowance = 10_000_000

// NormalizeRPCServerAddress returns addr with the current network default
// port appended if there is not already a port specified.
func (p *Params) NormalizeRPCServerAddress(addr string) (string, error) {
	return network.NormalizeAddress(addr, p.RPCPort)
}

func currentBlockVersionIndexForSlice(length int) int {
	return blockVersionIndexForSlice(length, constants.GetBlockVersion())
}

func blockVersionIndexForSlice(length int, blockVersion uint16) int {
	if length <= 0 {
		panic("dagconfig: attempted to index empty per-version parameter slice")
	}

	index := max(int(blockVersion)-1, 0)
	if index >= length {
		index = length - 1
	}
	return index
}

func (p *Params) targetTimePerBlockForCurrentVersion() time.Duration {
	return p.TargetTimePerBlock[currentBlockVersionIndexForSlice(len(p.TargetTimePerBlock))]
}

// TargetTimePerBlockForCurrentVersion is the exported form, for callers outside this package that
// need the block rate the network is actually running at. Indexing TargetTimePerBlock directly with
// constants.GetBlockVersion()-1 is not the same thing: the version is a process-global that starts
// at 1 and is only raised as blocks arrive, so anything reading it during startup gets version 1's
// parameters and keeps them. Callers should therefore call this at the point of use rather than
// caching its result.
func (p *Params) TargetTimePerBlockForCurrentVersion() time.Duration {
	return p.targetTimePerBlockForCurrentVersion()
}

// DifficultyAdjustmentWindowSizeForCurrentVersion returns the difficulty-adjustment window size for
// the process-global block version, safely clamped to the last entry if the table is shorter than
// the current version (see blockVersionIndexForSlice). Callers must use this instead of indexing
// DifficultyAdjustmentWindowSize directly with constants.GetBlockVersion()-1, which panics the
// instant the version reaches one past the table's length - exactly what happens at every future
// hard fork's activation unless every per-version table is extended in lockstep with POWScores.
func (p *Params) DifficultyAdjustmentWindowSizeForCurrentVersion() int {
	return p.DifficultyAdjustmentWindowSize[currentBlockVersionIndexForSlice(len(p.DifficultyAdjustmentWindowSize))]
}

// MaxBlockMassForCurrentVersion returns the max block mass for the process-global block version,
// safely clamped the same way as DifficultyAdjustmentWindowSizeForCurrentVersion - see its comment.
func (p *Params) MaxBlockMassForCurrentVersion() uint64 {
	return p.MaxBlockMass[currentBlockVersionIndexForSlice(len(p.MaxBlockMass))]
}

// KForCurrentVersion returns GHOSTDAG's K for the process-global block version, clamped the same way
// as DifficultyAdjustmentWindowSizeForCurrentVersion - see its comment.
func (p *Params) KForCurrentVersion() externalapi.KType {
	return p.K[currentBlockVersionIndexForSlice(len(p.K))]
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
// FinalityDepth returns the finality duration represented in blocks, for the process-global block version. Consensus
// code must use FinalityDepthForBlockVersion with the chain's current version instead (see blockversion.Current).
func (p *Params) FinalityDepth() uint64 {
	return p.FinalityDepthForBlockVersion(constants.GetBlockVersion())
}

// FinalityDepthForBlockVersion returns the finality duration represented in blocks for the given block version.
func (p *Params) FinalityDepthForBlockVersion(blockVersion uint16) uint64 {
	finalityDuration := p.FinalityDuration[blockVersionIndexForSlice(len(p.FinalityDuration), blockVersion)]
	targetTimePerBlock := p.TargetTimePerBlock[blockVersionIndexForSlice(len(p.TargetTimePerBlock), blockVersion)]
	if blockVersion < 5 {
		val := finalityDuration / targetTimePerBlock
		if val < 0 {
			panic("finalityDuration / targetTimePerBlock is negative, cannot convert to uint64")
		}
		finalityDepth, err := strconv.ParseUint(strconv.FormatInt(int64(val), 10), 10, 64)
		if err != nil {
			panic(err)
		}
		return finalityDepth
	}
	return uint64(finalityDuration.Seconds() / targetTimePerBlock.Seconds())
}

// PruningDepth returns the pruning duration represented in blocks, for the process-global block version. Consensus
// code must use PruningDepthForBlockVersion with the chain's current version instead (see blockversion.Current).
func (p *Params) PruningDepth() uint64 {
	return p.PruningDepthForBlockVersion(constants.GetBlockVersion())
}

// PruningDepthForBlockVersion returns the pruning duration represented in blocks for the given block version.
func (p *Params) PruningDepthForBlockVersion(blockVersion uint16) uint64 {
	k := uint64(p.K[blockVersionIndexForSlice(len(p.K), blockVersion)])
	finalityDepth := p.FinalityDepthForBlockVersion(blockVersion)
	if blockVersion < 5 {
		return 2*finalityDepth + 4*p.MergeSetSizeLimit*k + 2*k + 2
	}
	pruningMultiplier := p.PruningMultiplier[blockVersionIndexForSlice(len(p.PruningMultiplier), blockVersion)]
	return 2*finalityDepth*pruningMultiplier + 4*p.MergeSetSizeLimit*k + 2*k + 2
}

// MainnetParams defines the network parameters for the main Hoosat network.
var MainnetParams = Params{
	K: []externalapi.KType{
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
	},
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
		// These DNS seeders are ran by Evern00b
		"hoosat.seed-fi.evern00b.com",
		"hoosat.seed-de.evern00b.com",
		"hoosat.seed-in.evern00b.com",
		// Seeder run by Foztor in the UK
		"htn-mainnet-seed.htn.foztor.net",
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
	TargetTimePerBlock: []time.Duration{
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
	},
	FinalityDuration: []time.Duration{
		defaultFinalityDuration,
		defaultFinalityDuration,
		defaultFinalityDuration,
		defaultFinalityDuration,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
	},
	DifficultyAdjustmentWindowSize: []int{
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		2640,
		2640,
		2640,
		2640,
		2640,
		2640,
		2640,
		2640,
	},
	TimestampDeviationTolerance: defaultTimestampDeviationTolerance,
	// DAGKnight HF todo: Add Hard Fork DAA score to increase block version.
	// The 8th entry (block version 9) is the CoinbaseTimestampEntropyActivationVersion hard fork -
	// see domain/consensus/processes/coinbasemanager/payload.go.
	// The 9th entry (block version 10) activates the dev-fee integer-split formula
	// (calcDevFeeQuantity) and HTN-216's merge-set-reward fix (calcMergedBlockReward paying every
	// merge set block regardless of the difficulty-adjustment window sample) - both gated on
	// mergeSetRewardIgnoresDAAWindowVersion in coinbasemanager.go. User-chosen activation DAA score,
	// 2026-09-19. Briefly reverted the same day, then re-applied on the user's explicit instruction:
	// see HTN-216/ISSUES.md for the incident. This node's tip reaching 227679830 does make it reject
	// version-9 blocks relayed by peers that haven't upgraded (expected, uncoordinated-rollout
	// behavior, not itself a bug) - the separate, actual bug that was blocking this node's own block
	// template generation regardless of that (RepairBlockStatuses leaving a UTXO-valid virtual parent
	// with no stored multiset) is fixed by RepairMissingMultisets, see consensus.go.
	POWScores: []uint64{
		17500000,
		21821800,
		29335426,
		43334184,
		192792190,
		213340776,
		217137983,
		218735007,
		227679830,
		236878000, // v11 activation - Foztor 6/Oct/26, retargeted for ~Mon 12 Oct 2026 08:00 GMT
		^uint64(0),
	},
	PruningPointCheckpoint: mainnetPruningPointCheckpoint,

	PruningMultiplier: []uint64{
		0,
		0,
		0,
		0,
		1,
		1,
		1,
		1,
		1,
		1,
		1,
		1,
	},
	MaxBlockMass: []uint64{
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
	},

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

	MaxCoinbasePayloadLength: defaultMaxCoinbasePayloadLength,
	MaxBlockParents: []externalapi.KType{
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		12,
		12,
		12,
		12,
		12,
		12,
		12,
		12,
	},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	HardForkGates: mainnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth: []uint64{
		defaultMergeDepth,
		defaultMergeDepth,
		defaultMergeDepth,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
	},
}

// TestnetParams defines the network parameters for the test Hoosat network.
var TestnetParams = Params{
	K: []externalapi.KType{
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		defaultGHOSTDAGK,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
		40,
	},
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
	GenesisBlock:                    &testnetGenesisBlock,
	GenesisHash:                     testnetGenesisHash,
	PowMax:                          testnetPowMax,
	BlockCoinbaseMaturity:           100,
	SubsidyGenesisReward:            defaultSubsidyGenesisReward,
	PreDeflationaryPhaseBaseSubsidy: defaultPreDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseBaseSubsidy:    defaultDeflationaryPhaseBaseSubsidy,
	DeflationaryPhaseCurveFactor:    defaultDeflationaryPhaseCurveFactor,
	TargetTimePerBlock: []time.Duration{
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		defaultTargetTimePerBlock,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
		200 * time.Millisecond,
	},
	FinalityDuration: []time.Duration{
		defaultFinalityDuration,
		defaultFinalityDuration,
		defaultFinalityDuration,
		defaultFinalityDuration,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
		10800 * time.Second,
	},
	DifficultyAdjustmentWindowSize: []int{
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		defaultDifficultyAdjustmentWindowSize,
		2651,
		2641,
		2641,
		2641,
		2641,
		2641,
		2641,
		2641,
		2641,
		2641,
		2641,
	},
	TimestampDeviationTolerance: defaultTimestampDeviationTolerance,
	// TODO: set the real activation DAA score for block version 8 (coinbase
	// entropy hard fork) as the 7th entry. ^uint64(0) is a placeholder that
	// never triggers, so version 8 stays inactive until this is set.
	// The 8th entry (block version 9) is the CoinbaseTimestampEntropyActivationVersion hard fork -
	// see domain/consensus/processes/coinbasemanager/payload.go. Testnet can use a real,
	// soon-reachable value freely (low stakes); this just needs to stay comfortably above the
	// 7th entry so the two hard forks exercise as distinct transitions during testing.
	POWScores: []uint64{
		1,
		50,
		100,
		150,
		200,
		250,
		300,
		350,
		400,
		450,
		1534673,
		// Block version 13 activates the header and structural checks gated at 13 in
		// testnetHardForkGates. ^uint64(0) is a placeholder that never triggers: set it to the
		// testnet DAA score the fork should activate at.
		^uint64(0),
	},
	PruningMultiplier: []uint64{
		0,
		0,
		0,
		0,
		1,
		1,
		1,
		1,
		1,
		1,
		1,
		1,
		1,
	},
	MaxBlockMass: []uint64{
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		defaultMaxBlockMass,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
		1_000_000,
	},

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

	MaxCoinbasePayloadLength: defaultMaxCoinbasePayloadLength,
	MaxBlockParents: []externalapi.KType{
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		defaultMaxBlockParents,
		12,
		12,
		12,
		12,
		12,
		12,
		12,
		12,
		12,
	},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	HardForkGates: testnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth: []uint64{
		defaultMergeDepth,
		defaultMergeDepth,
		defaultMergeDepth,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
		3600,
	},
}

var TestnetParamsB5 = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40, 40},
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
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit * 5,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,

	HardForkGates: testnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600, 3600},
}

var TestnetParamsB10 = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40, 40},
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
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 16, 16},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit * 10,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,

	HardForkGates: testnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	MaxBlockLevel: 250,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600, 3600},
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

	HardForkGates: testnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	MaxBlockLevel: 250,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, defaultMergeDepth},
}

// DevnetParams defines the network parameters for the development Hoosat network.
var DevnetParams = Params{
	K:           []externalapi.KType{defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, defaultGHOSTDAGK, 40, 40},
	Name:        "hoosat-devnet",
	Net:         appmessage.Devnet,
	RPCPort:     "42426",
	DefaultPort: "42427",
	DNSSeeds:    []string{}, // NOTE: There must NOT be any seeds.

	// DAG parameters
	GenesisBlock:                    &devnetGenesisBlock,
	GenesisHash:                     devnetGenesisHash,
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
	Prefix: util.Bech32PrefixHoosat,

	// Address encoding magics
	PrivateKeyID: 0x80, // starts with 5 (uncompressed) or K (compressed)

	// EnableNonNativeSubnetworks enables non-native/coinbase transactions
	EnableNonNativeSubnetworks: false,

	DisableDifficultyAdjustment: false,

	MaxCoinbasePayloadLength:                defaultMaxCoinbasePayloadLength,
	MaxBlockParents:                         []externalapi.KType{defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, defaultMaxBlockParents, 12, 12},
	MassPerTxByte:                           defaultMassPerTxByte,
	MassPerScriptPubKeyByte:                 defaultMassPerScriptPubKeyByte,
	MassPerSigOp:                            defaultMassPerSigOp,
	MergeSetSizeLimit:                       defaultMergeSetSizeLimit,
	CoinbasePayloadScriptPublicKeyMaxLength: defaultCoinbasePayloadScriptPublicKeyMaxLength,
	PruningProofM:                           defaultPruningProofM,
	DeflationaryPhaseDaaScore:               defaultDeflationaryPhaseDaaScore,
	DisallowDirectBlocksOnTopOfGenesis:      true,

	HardForkGates: testnetHardForkGates,

	UnpricedTransactionFeeAllowance: defaultUnpricedTransactionFeeAllowance,

	// This is technically 255, but we clamped it at 256 - block level of mainnet genesis
	// This means that any block that has a level lower or equal to genesis will be level 0.
	MaxBlockLevel: 225,
	MergeDepth:    []uint64{defaultMergeDepth, defaultMergeDepth, defaultMergeDepth, 3600, 3600, 3600},
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
