package externalapi

import (
	"math/big"
	"reflect"
	"testing"
)

func initTestBlockInfoStructsForClone() []*BlockInfo {
	tests := []*BlockInfo{
		{
			Exists:         true,
			BlockStatus:    BlockStatus(0x01),
			BlueScore:      0,
			BlueWork:       big.NewInt(0),
			DynamicK:       0,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:         true,
			BlockStatus:    BlockStatus(0x02),
			BlueScore:      0,
			BlueWork:       big.NewInt(0),
			DynamicK:       0,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:         true,
			BlockStatus:    1,
			BlueScore:      1,
			BlueWork:       big.NewInt(0),
			DynamicK:       1,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:         true,
			BlockStatus:    255,
			BlueScore:      2,
			BlueWork:       big.NewInt(0),
			DynamicK:       2,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:         true,
			BlockStatus:    0,
			BlueScore:      3,
			BlueWork:       big.NewInt(0),
			DynamicK:       3,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:         true,
			BlockStatus:    BlockStatus(0x01),
			BlueScore:      0,
			BlueWork:       big.NewInt(1),
			DynamicK:       4,
			SelectedParent: nil,
			MergeSetBlues:  []*DomainHash{},
			MergeSetReds:   []*DomainHash{},
		}, {
			Exists:      false,
			BlockStatus: BlockStatus(0x01),
			BlueScore:   0,
			BlueWork:    big.NewInt(1),
			DynamicK:    5,
			SelectedParent: NewDomainHashFromByteArray(&[DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			}),
			MergeSetBlues: []*DomainHash{
				NewDomainHashFromByteArray(&[DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
				NewDomainHashFromByteArray(&[DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
				}),
			},
			MergeSetReds: []*DomainHash{
				NewDomainHashFromByteArray(&[DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04,
				}),
				NewDomainHashFromByteArray(&[DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05,
				}),
			},
		},
	}
	return tests
}

func TestBlockInfo_Clone(t *testing.T) {
	blockInfos := initTestBlockInfoStructsForClone()
	for i, blockInfo := range blockInfos {
		blockInfoClone := blockInfo.Clone()
		if !reflect.DeepEqual(blockInfo, blockInfoClone) {
			t.Fatalf("Test #%d:[DeepEqual] clone should be equal to the original", i)
		}
	}
}
