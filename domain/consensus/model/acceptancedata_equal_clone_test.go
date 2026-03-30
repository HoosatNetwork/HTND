package model_test

import (
	"reflect"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
)

func initTestTransactionAcceptanceDataForClone() []*externalapi.TransactionAcceptanceData {
	tests := []*externalapi.TransactionAcceptanceData{
		{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		},
	}
	return tests
}

type testTransactionAcceptanceDataToCompare struct {
	transactionAcceptanceData *externalapi.TransactionAcceptanceData
	expectedResult            bool
}

type testTransactionAcceptanceDataStruct struct {
	baseTransactionAcceptanceData        *externalapi.TransactionAcceptanceData
	transactionAcceptanceDataToCompareTo []testTransactionAcceptanceDataToCompare
}

func initTransactionAcceptanceDataForEqual() []testTransactionAcceptanceDataStruct {
	testTransactionAcceptanceDataBase := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 1,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         1,
		IsAccepted:                  true,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}

	testTransactionAcceptanceData1 := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 1,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         1,
		IsAccepted:                  true,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}
	// test 2: different transactions
	testTransactionAcceptanceData2 := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 2,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         1,
		IsAccepted:                  true,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}
	// test 3: different Fee
	testTransactionAcceptanceData3 := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 1,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         2,
		IsAccepted:                  true,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}
	// test 4: different isAccepted
	testTransactionAcceptanceData4 := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 1,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         1,
		IsAccepted:                  false,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}

	// test 5: different TransactionInputUTXOEntries
	testTransactionAcceptanceData5 := externalapi.TransactionAcceptanceData{
		Transaction: &externalapi.DomainTransaction{
			Version: 1,
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
				},
				SignatureScript: []byte{1, 2, 3},
				Sequence:        uint64(0xFFFFFFFF),
				SigOpCount:      1,
				UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
			}},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
				},
				{
					Value:           uint64(0xFFFF),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
				},
			},
			LockTime:     1,
			SubnetworkID: externalapi.DomainSubnetworkID{0x01},
			Gas:          1,
			Payload:      []byte{0x01},
			Fee:          0,
			Mass:         1,
			ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
			}),
		},
		Fee:                         1,
		IsAccepted:                  false,
		TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
	}

	tests := []testTransactionAcceptanceDataStruct{
		{
			baseTransactionAcceptanceData: &testTransactionAcceptanceDataBase,
			transactionAcceptanceDataToCompareTo: []testTransactionAcceptanceDataToCompare{
				{
					transactionAcceptanceData: &testTransactionAcceptanceData1,
					expectedResult:            true,
				}, {
					transactionAcceptanceData: &testTransactionAcceptanceData2,
					expectedResult:            false,
				}, {
					transactionAcceptanceData: &testTransactionAcceptanceData3,
					expectedResult:            false,
				}, {
					transactionAcceptanceData: &testTransactionAcceptanceData4,
					expectedResult:            false,
				}, {
					transactionAcceptanceData: &testTransactionAcceptanceData5,
					expectedResult:            false,
				}, {
					transactionAcceptanceData: nil,
					expectedResult:            false,
				},
			},
		}, {
			baseTransactionAcceptanceData: nil,
			transactionAcceptanceDataToCompareTo: []testTransactionAcceptanceDataToCompare{
				{
					transactionAcceptanceData: &testTransactionAcceptanceData1,
					expectedResult:            false,
				}, {
					transactionAcceptanceData: nil,
					expectedResult:            true,
				},
			},
		},
	}
	return tests
}

func TestTransactionAcceptanceData_Equal(t *testing.T) {
	acceptanceData := initTransactionAcceptanceDataForEqual()
	for i, test := range acceptanceData {
		for j, subTest := range test.transactionAcceptanceDataToCompareTo {
			result1 := test.baseTransactionAcceptanceData.Equal(subTest.transactionAcceptanceData)
			if result1 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result1)
			}
			result2 := subTest.transactionAcceptanceData.Equal(test.baseTransactionAcceptanceData)
			if result2 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result2)
			}
		}
	}
}

func TestTransactionAcceptanceData_Clone(t *testing.T) {
	testTransactionAcceptanceData := initTestTransactionAcceptanceDataForClone()
	for i, transactionAcceptanceData := range testTransactionAcceptanceData {
		transactionAcceptanceDataClone := transactionAcceptanceData.Clone()
		if !transactionAcceptanceDataClone.Equal(transactionAcceptanceData) {
			t.Fatalf("Test #%d:[Equal] clone should be equal to the original", i)
		}
		if !reflect.DeepEqual(transactionAcceptanceData, transactionAcceptanceDataClone) {
			t.Fatalf("Test #%d:[DeepEqual] clone should be equal to the original", i)
		}
	}
}

func initTestBlockAcceptanceDataForClone() []*externalapi.BlockAcceptanceData {
	tests := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				{
					Transaction: &externalapi.DomainTransaction{
						Version: 1,
						Inputs: []*externalapi.DomainTransactionInput{
							{
								PreviousOutpoint: externalapi.DomainOutpoint{
									TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}),
									Index:         0xFFFF,
								},
								SignatureScript: []byte{1, 2, 3},
								Sequence:        uint64(0xFFFFFFFF),
								SigOpCount:      1,
								UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
							},
						},
						Outputs: []*externalapi.DomainTransactionOutput{
							{
								Value:           uint64(0xFFFF),
								ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
							},
							{
								Value:           uint64(0xFFFF),
								ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
							},
						},
						LockTime:     1,
						SubnetworkID: externalapi.DomainSubnetworkID{0x01},
						Gas:          1,
						Payload:      []byte{0x01},
						Fee:          0,
						Mass:         1,
						ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
						}),
					},
					Fee:                         1,
					IsAccepted:                  true,
					TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
				},
			},
		},
	}
	return tests
}

type testBlockAcceptanceDataToCompare struct {
	blockAcceptanceData *externalapi.BlockAcceptanceData
	expectedResult      bool
}

type testBlockAcceptanceDataStruct struct {
	baseBlockAcceptanceData        *externalapi.BlockAcceptanceData
	blockAcceptanceDataToCompareTo []testBlockAcceptanceDataToCompare
}

func iniBlockAcceptanceDataForEqual() []testBlockAcceptanceDataStruct {
	testBlockAcceptanceDataBase := externalapi.BlockAcceptanceData{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		}},
	}
	// test 1: structs are equal
	testBlockAcceptanceData1 := externalapi.BlockAcceptanceData{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		}},
	}
	// test 2: different size
	testBlockAcceptanceData2 := externalapi.BlockAcceptanceData{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		}, {}},
	}
	// test 3: different transactions, same size
	testBlockAcceptanceData3 := externalapi.BlockAcceptanceData{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  false,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		}},
	}

	// test 4 - different block hash
	testBlockAcceptanceData4 := externalapi.BlockAcceptanceData{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction: &externalapi.DomainTransaction{
				Version: 1,
				Inputs: []*externalapi.DomainTransactionInput{{
					PreviousOutpoint: externalapi.DomainOutpoint{
						TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
					},
					SignatureScript: []byte{1, 2, 3},
					Sequence:        uint64(0xFFFFFFFF),
					SigOpCount:      1,
					UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
				}},
				Outputs: []*externalapi.DomainTransactionOutput{
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
					},
					{
						Value:           uint64(0xFFFF),
						ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
					},
				},
				LockTime:     1,
				SubnetworkID: externalapi.DomainSubnetworkID{0x01},
				Gas:          1,
				Payload:      []byte{0x01},
				Fee:          0,
				Mass:         1,
				ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
				}),
			},
			Fee:                         1,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
		}},
	}

	tests := []testBlockAcceptanceDataStruct{
		{
			baseBlockAcceptanceData: &testBlockAcceptanceDataBase,
			blockAcceptanceDataToCompareTo: []testBlockAcceptanceDataToCompare{
				{
					blockAcceptanceData: &testBlockAcceptanceData1,
					expectedResult:      true,
				},
				{
					blockAcceptanceData: &testBlockAcceptanceData2,
					expectedResult:      false,
				},
				{
					blockAcceptanceData: &testBlockAcceptanceData3,
					expectedResult:      false,
				},
				{
					blockAcceptanceData: nil,
					expectedResult:      false,
				},
				{
					blockAcceptanceData: &testBlockAcceptanceData4,
					expectedResult:      false,
				},
			},
		}, {
			baseBlockAcceptanceData: nil,
			blockAcceptanceDataToCompareTo: []testBlockAcceptanceDataToCompare{
				{
					blockAcceptanceData: &testBlockAcceptanceData1,
					expectedResult:      false,
				}, {
					blockAcceptanceData: nil,
					expectedResult:      true,
				},
			},
		},
	}
	return tests
}

func TestBlockAcceptanceData_Equal(t *testing.T) {
	blockAcceptances := iniBlockAcceptanceDataForEqual()
	for i, test := range blockAcceptances {
		for j, subTest := range test.blockAcceptanceDataToCompareTo {
			result1 := test.baseBlockAcceptanceData.Equal(subTest.blockAcceptanceData)
			if result1 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result1)
			}
			result2 := subTest.blockAcceptanceData.Equal(test.baseBlockAcceptanceData)
			if result2 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result2)
			}
		}
	}
}

func TestBlockAcceptanceData_Clone(t *testing.T) {
	testBlockAcceptanceData := initTestBlockAcceptanceDataForClone()
	for i, blockAcceptanceData := range testBlockAcceptanceData {
		blockAcceptanceDataClone := blockAcceptanceData.Clone()
		if !blockAcceptanceDataClone.Equal(blockAcceptanceData) {
			t.Fatalf("Test #%d:[Equal] clone should be equal to the original", i)
		}
		if !reflect.DeepEqual(blockAcceptanceData, blockAcceptanceDataClone) {
			t.Fatalf("Test #%d:[DeepEqual] clone should be equal to the original", i)
		}
	}
}

func initTestAcceptanceDataForClone() []externalapi.AcceptanceData {
	test1 := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				{
					Transaction: &externalapi.DomainTransaction{
						Version: 1,
						Inputs: []*externalapi.DomainTransactionInput{{
							PreviousOutpoint: externalapi.DomainOutpoint{
								TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
							},
							SignatureScript: []byte{1, 2, 3},
							Sequence:        uint64(0xFFFFFFFF),
							SigOpCount:      1,
							UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
						}},
						Outputs: []*externalapi.DomainTransactionOutput{
							{
								Value:           uint64(0xFFFF),
								ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
							},
							{
								Value:           uint64(0xFFFF),
								ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
							},
						},
						LockTime:     1,
						SubnetworkID: externalapi.DomainSubnetworkID{0x01},
						Gas:          1,
						Payload:      []byte{0x01},
						Fee:          0,
						Mass:         1,
						ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
							0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
						}),
					},
					Fee:                         1,
					IsAccepted:                  true,
					TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
				},
			},
		},
	}
	tests := []externalapi.AcceptanceData{test1, test1}
	return tests
}

type testAcceptanceDataToCompare struct {
	acceptanceData externalapi.AcceptanceData
	expectedResult bool
}

type testAcceptanceDataStruct struct {
	baseAcceptanceData        externalapi.AcceptanceData
	acceptanceDataToCompareTo []testAcceptanceDataToCompare
}

func initAcceptanceDataForEqual() []testAcceptanceDataStruct {
	testAcceptanceDataBase := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
				Transaction: &externalapi.DomainTransaction{
					Version: 1,
					Inputs: []*externalapi.DomainTransactionInput{{
						PreviousOutpoint: externalapi.DomainOutpoint{
							TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
						},
						SignatureScript: []byte{1, 2, 3},
						Sequence:        uint64(0xFFFFFFFF),
						SigOpCount:      1,
						UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
					}},
					Outputs: []*externalapi.DomainTransactionOutput{
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
						},
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
						},
					},
					LockTime:     1,
					SubnetworkID: externalapi.DomainSubnetworkID{0x01},
					Gas:          1,
					Payload:      []byte{0x01},
					Fee:          0,
					Mass:         1,
					ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
					}),
				},
				Fee:                         1,
				IsAccepted:                  true,
				TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
			}},
		},
	}
	// test 1: structs are equal
	testAcceptanceData1 := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
				Transaction: &externalapi.DomainTransaction{
					Version: 1,
					Inputs: []*externalapi.DomainTransactionInput{{
						PreviousOutpoint: externalapi.DomainOutpoint{
							TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
						},
						SignatureScript: []byte{1, 2, 3},
						Sequence:        uint64(0xFFFFFFFF),
						SigOpCount:      1,
						UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
					}},
					Outputs: []*externalapi.DomainTransactionOutput{
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
						},
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
						},
					},
					LockTime:     1,
					SubnetworkID: externalapi.DomainSubnetworkID{0x01},
					Gas:          1,
					Payload:      []byte{0x01},
					Fee:          0,
					Mass:         1,
					ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
					}),
				},
				Fee:                         1,
				IsAccepted:                  true,
				TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
			}},
		},
	}
	// test 2: different size
	testAcceptanceData2 := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
				Transaction: &externalapi.DomainTransaction{
					Version: 1,
					Inputs: []*externalapi.DomainTransactionInput{{
						PreviousOutpoint: externalapi.DomainOutpoint{
							TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
						},
						SignatureScript: []byte{1, 2, 3},
						Sequence:        uint64(0xFFFFFFFF),
						SigOpCount:      1,
						UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
					}},
					Outputs: []*externalapi.DomainTransactionOutput{
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
						},
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
						},
					},
					LockTime:     1,
					SubnetworkID: externalapi.DomainSubnetworkID{0x01},
					Gas:          1,
					Payload:      []byte{0x01},
					Fee:          0,
					Mass:         1,
					ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
					}),
				},
				Fee:                         1,
				IsAccepted:                  true,
				TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
			}},
		}, {},
	}
	// test 3: different transactions, same size
	testAcceptanceData3 := []*externalapi.BlockAcceptanceData{
		{
			BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
				Transaction: &externalapi.DomainTransaction{
					Version: 2,
					Inputs: []*externalapi.DomainTransactionInput{{
						PreviousOutpoint: externalapi.DomainOutpoint{
							TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{0x01}), Index: 0xFFFF,
						},
						SignatureScript: []byte{1, 2, 3},
						Sequence:        uint64(0xFFFFFFFF),
						SigOpCount:      1,
						UTXOEntry:       utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2),
					}},
					Outputs: []*externalapi.DomainTransactionOutput{
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2}, Version: 0},
						},
						{
							Value:           uint64(0xFFFF),
							ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 3}, Version: 0},
						},
					},
					LockTime:     1,
					SubnetworkID: externalapi.DomainSubnetworkID{0x01},
					Gas:          1,
					Payload:      []byte{0x01},
					Fee:          0,
					Mass:         1,
					ID: externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
						0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
					}),
				},
				Fee:                         1,
				IsAccepted:                  true,
				TransactionInputUTXOEntries: []externalapi.UTXOEntry{utxo.NewUTXOEntry(1, &externalapi.ScriptPublicKey{Script: []byte{0, 1, 2, 3}, Version: 0}, true, 2)},
			}},
		},
	}

	tests := []testAcceptanceDataStruct{
		{
			baseAcceptanceData: testAcceptanceDataBase,
			acceptanceDataToCompareTo: []testAcceptanceDataToCompare{
				{
					acceptanceData: testAcceptanceData1,
					expectedResult: true,
				}, {
					acceptanceData: testAcceptanceData2,
					expectedResult: false,
				}, {
					acceptanceData: testAcceptanceData3,
					expectedResult: false,
				},
			},
		},
	}
	return tests
}

func TestAcceptanceData_Equal(t *testing.T) {
	acceptances := initAcceptanceDataForEqual()
	for i, test := range acceptances {
		for j, subTest := range test.acceptanceDataToCompareTo {
			result1 := test.baseAcceptanceData.Equal(subTest.acceptanceData)
			if result1 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result1)
			}
			result2 := subTest.acceptanceData.Equal(test.baseAcceptanceData)
			if result2 != subTest.expectedResult {
				t.Fatalf("Test #%d:%d: Expected %t but got %t", i, j, subTest.expectedResult, result2)
			}
		}
	}
}

func TestAcceptanceData_Clone(t *testing.T) {
	testAcceptanceData := initTestAcceptanceDataForClone()
	for i, acceptanceData := range testAcceptanceData {
		acceptanceDataClone := acceptanceData.Clone()
		if !acceptanceDataClone.Equal(acceptanceData) {
			t.Fatalf("Test #%d:[Equal] clone should be equal to the original", i)
		}
		if !reflect.DeepEqual(acceptanceData, acceptanceDataClone) {
			t.Fatalf("Test #%d:[DeepEqual] clone should be equal to the original", i)
		}
	}
}
