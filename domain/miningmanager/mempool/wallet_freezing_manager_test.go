package mempool

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
)

type testUTXOEntry struct {
	scriptPublicKey *externalapi.ScriptPublicKey
}

func (tue *testUTXOEntry) Amount() uint64 { return 0 }
func (tue *testUTXOEntry) ScriptPublicKey() *externalapi.ScriptPublicKey {
	return tue.scriptPublicKey
}
func (tue *testUTXOEntry) BlockDAAScore() uint64 { return 0 }
func (tue *testUTXOEntry) IsCoinbase() bool      { return false }
func (tue *testUTXOEntry) Equal(other externalapi.UTXOEntry) bool {
	_, ok := other.(*testUTXOEntry)
	return ok
}

func TestWalletFreezingManagerExtractAddresses_NoPanicOnNilExtractedAddress(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestWalletFreezingManagerExtractAddresses_NoPanicOnNilExtractedAddress")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		config := DefaultConfig(tc.DAGParams())
		wfm := newWalletFreezingManager(config)

		tx := &externalapi.DomainTransaction{
			Version: constants.MaxTransactionVersion,
			Inputs: []*externalapi.DomainTransactionInput{
				{
					UTXOEntry: &testUTXOEntry{scriptPublicKey: &externalapi.ScriptPublicKey{
						Script:  []byte{txscript.OpTrue},
						Version: constants.MaxScriptPublicKeyVersion + 1,
					}},
				},
			},
			Outputs: []*externalapi.DomainTransactionOutput{
				{
					Value: 0,
					ScriptPublicKey: &externalapi.ScriptPublicKey{
						Script:  []byte{txscript.OpTrue},
						Version: constants.MaxScriptPublicKeyVersion + 1,
					},
				},
				{
					Value: 0,
					ScriptPublicKey: &externalapi.ScriptPublicKey{
						Script:  []byte{txscript.OpTrue},
						Version: 0,
					},
				},
			},
		}

		addresses := wfm.extractAddressesFromTransaction(tx)
		if len(addresses) != 0 {
			t.Fatalf("expected no extracted addresses, got %v", addresses)
		}

		isFrozen, frozenAddresses := wfm.isWalletFrozen(tx)
		if isFrozen {
			t.Fatalf("expected transaction to not be frozen, got frozen addresses %v", frozenAddresses)
		}
	})
}
