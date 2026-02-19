package coinbasemanager

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
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
		nil)
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
		nil)
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
	len := len(subsidyTable)
	t.Logf("Length: %d", len)
}
