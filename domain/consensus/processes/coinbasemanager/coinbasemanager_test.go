package coinbasemanager

import (
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestCalcDeflationaryPeriodBlockSubsidy(t *testing.T) {
	const secondsPerMonth = 2629800
	const secondsPerHalving = secondsPerMonth * 12
	const deflationaryPhaseDaaScore = secondsPerMonth * 6
	const deflationaryPhaseBaseSubsidy = 100 * constants.SompiPerHoosat
	deflationaryPhaseCurveFactor := dagconfig.MainnetParams.DeflationaryPhaseCurveFactor
	coinbaseManagerInterface := New(
		nil,
		0,
		0,
		0,
		&externalapi.DomainHash{},
		deflationaryPhaseDaaScore,
		deflationaryPhaseBaseSubsidy,
		deflationaryPhaseCurveFactor,
		dagconfig.MainnetParams.TargetTimePerBlock,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, nil)
	coinbaseManagerInstance := coinbaseManagerInterface.(*coinbaseManager)

	tests := []struct {
		name                 string
		blockDaaScore        uint64
		expectedBlockSubsidy uint64
		blockVersion         uint16
	}{
		{
			name:                 "start of deflationary phase",
			blockDaaScore:        deflationaryPhaseDaaScore,
			expectedBlockSubsidy: 8164965809,
			blockVersion:         1,
		},
		{
			name:                 "after one halving",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving,
			expectedBlockSubsidy: 6666666666,
			blockVersion:         2,
		},
		{
			name:                 "after two halvings",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving*2,
			expectedBlockSubsidy: 5443310539,
			blockVersion:         2,
		},
		{
			name:                 "after five halvings",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving*5,
			expectedBlockSubsidy: 2962962962,
			blockVersion:         2,
		},
		{
			name:                 "after 32 halvings",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving*32,
			expectedBlockSubsidy: 12430661,
			blockVersion:         2,
		},
		{
			name:                 "just before subsidy depleted",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving*35,
			expectedBlockSubsidy: 6766394,
			blockVersion:         2,
		},
		{
			name:                 "after subsidy depleted",
			blockDaaScore:        deflationaryPhaseDaaScore + secondsPerHalving*36,
			expectedBlockSubsidy: 5524738,
			blockVersion:         2,
		},
	}

	for _, test := range tests {
		blockSubsidy := coinbaseManagerInstance.calcDeflationaryPeriodBlockSubsidy(test.blockDaaScore, test.blockVersion)
		if blockSubsidy != test.expectedBlockSubsidy {
			t.Errorf("TestCalcDeflationaryPeriodBlockSubsidy: test '%s' failed. Want: %d, got: %d",
				test.name, test.expectedBlockSubsidy, blockSubsidy)
		}
	}
}

func TestAcceptedFeeFallsBackToRecordedFeeWhenInputEntriesAreMissing(t *testing.T) {
	transaction := &externalapi.DomainTransaction{
		Inputs:  []*externalapi.DomainTransactionInput{{}},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 1}},
	}
	acceptance := &externalapi.TransactionAcceptanceData{
		Transaction: transaction,
		Fee:         884000,
		IsAccepted:  true,
	}

	if got := acceptedFee(acceptance); got != acceptance.Fee {
		t.Fatalf("acceptedFee() = %d, want recorded fee %d", got, acceptance.Fee)
	}
}

func TestAcceptedFeeRecomputesWhenInputEntriesAreComplete(t *testing.T) {
	transaction := &externalapi.DomainTransaction{
		Inputs:  []*externalapi.DomainTransactionInput{{}},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 1}},
	}
	acceptance := &externalapi.TransactionAcceptanceData{
		Transaction: transaction,
		Fee:         999999,
		IsAccepted:  true,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{
			utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{}, false, 0),
		},
	}

	if got := acceptedFee(acceptance); got != 0 {
		t.Fatalf("acceptedFee() = %d, want recomputed fee 0", got)
	}
}

func TestBuildSubsidyTable(t *testing.T) {
	deflationaryPhaseBaseSubsidy := dagconfig.MainnetParams.DeflationaryPhaseBaseSubsidy
	deflationaryPhaseCurveFactor := dagconfig.MainnetParams.DeflationaryPhaseCurveFactor
	if deflationaryPhaseBaseSubsidy != 100*constants.SompiPerHoosat {
		t.Errorf("TestBuildSubsidyTable: table generation function was not updated to reflect "+
			"the new base subsidy %d. Please fix the constant above and replace subsidyByDeflationaryMonthTable "+
			"in coinbasemanager.go with the printed table", deflationaryPhaseBaseSubsidy)
	}
	coinbaseManagerInterface := New(
		nil,
		0,
		0,
		0,
		&externalapi.DomainHash{},
		0,
		deflationaryPhaseBaseSubsidy,
		deflationaryPhaseCurveFactor,
		dagconfig.MainnetParams.TargetTimePerBlock,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, nil)
	coinbaseManagerInstance := coinbaseManagerInterface.(*coinbaseManager)

	var subsidyTable []uint64
	for M := uint64(0); ; M++ {
		subsidy := coinbaseManagerInstance.calcDeflationaryPeriodBlockSubsidyFloatCalc(M)
		subsidyTable = append(subsidyTable, subsidy)
		if subsidy == 0 {
			break
		}
	}

	var tableStr strings.Builder
	tableStr.WriteString("\n{\t")
	for i := 0; i < len(subsidyTable); i++ {
		tableStr.WriteString(strconv.FormatUint(subsidyTable[i], 10) + ", ")
		if (i+1)%25 == 0 {
			tableStr.WriteString("\n\t")
		}
	}
	tableStr.WriteString("\n}")
	t.Logf("%s", tableStr.String())
	tableLen := len(subsidyTable)
	t.Logf("Length: %d", tableLen)
}

// singleBlockStore is a model.BlockStore fake that serves exactly one block for calcMergedBlockReward
// (the only method it needs), and panics if any other method is exercised.
type singleBlockStore struct {
	hash  externalapi.DomainHash
	block *externalapi.DomainBlock
}

func (s *singleBlockStore) Stage(*model.StagingArea, *externalapi.DomainHash, *externalapi.DomainBlock) {
	panic("not implemented")
}
func (s *singleBlockStore) IsStaged(*model.StagingArea) bool { panic("not implemented") }
func (s *singleBlockStore) UnstageAll(*model.StagingArea)    {}
func (s *singleBlockStore) Delete(*model.StagingArea, *externalapi.DomainHash) {
	panic("not implemented")
}
func (s *singleBlockStore) Count(*model.StagingArea) uint64 { panic("not implemented") }
func (s *singleBlockStore) AllBlockHashesIterator(model.DBReader) (model.BlockIterator, error) {
	panic("not implemented")
}
func (s *singleBlockStore) CacheLen() int { panic("not implemented") }
func (s *singleBlockStore) HasBlock(model.DBReader, *model.StagingArea, *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}
func (s *singleBlockStore) Blocks(model.DBReader, *model.StagingArea, []*externalapi.DomainHash) ([]*externalapi.DomainBlock, error) {
	panic("not implemented")
}
func (s *singleBlockStore) Block(_ model.DBReader, _ *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainBlock, error) {
	if !blockHash.Equal(&s.hash) {
		panic("unexpected block hash")
	}
	return s.block, nil
}

// TestCalcMergedBlockRewardPaysRegardlessOfDAAWindowFromActivation is HTN-216's regression test.
//
// mergingBlockDAAAddedBlocksSet is the subset of a merging block's own merge set that landed inside
// the (size-bounded, blue-work-ranked) difficulty-adjustment window sample - a sampling artifact, not
// a statement about whether a merge set block was legitimately merged. Before HTN-216,
// calcMergedBlockReward silently paid a merge set block nothing whenever it fell outside that
// sample, for every block version. This pins both sides of the fix: below
// mergeSetRewardIgnoresDAAWindowVersion the historical (already-mined) behavior is preserved exactly,
// and from it onward every merge set block with valid acceptance data is paid regardless.
func TestCalcMergedBlockRewardPaysRegardlessOfDAAWindowFromActivation(t *testing.T) {
	const subsidy = uint64(1000000)
	const blockVersion = mergeSetRewardIgnoresDAAWindowVersion

	coinbaseManagerInterface := New(
		nil, 0, 0, 255, &externalapi.DomainHash{}, 0, 0, 1,
		dagconfig.MainnetParams.TargetTimePerBlock,
		nil, nil, nil, nil, nil, nil, nil, nil)
	cbm := coinbaseManagerInterface.(*coinbaseManager)

	blockHash := *externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	payload, err := cbm.serializeCoinbasePayload(1, &externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0xAB}, Version: 0},
	}, subsidy, [lengthOfEntropy]byte{}, blockVersion)
	if err != nil {
		t.Fatalf("serializeCoinbasePayload: %+v", err)
	}
	block := &externalapi.DomainBlock{
		Header: blockheader.NewImmutableBlockHeader(
			blockVersion, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			0, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{}),
		Transactions: []*externalapi.DomainTransaction{{Payload: payload}},
	}
	cbm.blockStore = &singleBlockStore{hash: blockHash, block: block}

	blockAcceptanceData := &externalapi.BlockAcceptanceData{BlockHash: &blockHash}
	emptyDAAAddedBlocksSet := hashset.New()

	reward, err := cbm.calcMergedBlockReward(model.NewStagingArea(), &blockHash, blockAcceptanceData,
		emptyDAAAddedBlocksSet, false)
	if err != nil {
		t.Fatalf("calcMergedBlockReward (legacy path): %+v", err)
	}
	if reward != 0 {
		t.Fatalf("calcMergedBlockReward with payRegardlessOfDAAWindow=false and an empty DAA added "+
			"blocks set: want 0 (preserving already-mined-block behavior), got %d", reward)
	}

	reward, err = cbm.calcMergedBlockReward(model.NewStagingArea(), &blockHash, blockAcceptanceData,
		emptyDAAAddedBlocksSet, true)
	if err != nil {
		t.Fatalf("calcMergedBlockReward (fixed path): %+v", err)
	}
	if reward != subsidy {
		t.Fatalf("calcMergedBlockReward with payRegardlessOfDAAWindow=true and an empty DAA added "+
			"blocks set: want the block's own subsidy %d paid despite being outside the DAA window, got %d",
			subsidy, reward)
	}
}
