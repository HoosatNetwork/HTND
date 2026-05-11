package mempool

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
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

func TestWalletFreezingManagerExtractAddresses_IncludesP2SHAndRedeemScriptAddressForInputs(t *testing.T) {
	pubKeyHash := make([]byte, 32)
	for i := range pubKeyHash {
		pubKeyHash[i] = 0x11
	}

	redeemScript, err := txscript.NewScriptBuilder().
		AddOp(txscript.OpDup).
		AddOp(txscript.OpBlake2b).
		AddData(pubKeyHash).
		AddOp(txscript.OpEqualVerify).
		AddOp(txscript.OpCheckSig).
		Script()
	if err != nil {
		t.Fatalf("unexpected redeemScript builder error: %v", err)
	}

	p2shScript, err := txscript.PayToScriptHashScript(redeemScript)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %v", err)
	}

	signatureScript, err := txscript.PayToScriptHashSignatureScript(redeemScript, nil)
	if err != nil {
		t.Fatalf("PayToScriptHashSignatureScript: %v", err)
	}

	_, p2shAddr, err := txscript.ExtractScriptPubKeyAddress(&externalapi.ScriptPublicKey{Script: p2shScript, Version: 0}, &dagconfig.MainnetParams)
	if err != nil {
		t.Fatalf("ExtractScriptPubKeyAddress(p2sh): %v", err)
	}
	if p2shAddr == nil {
		t.Fatalf("expected non-nil p2sh addr")
	}

	innerClass, innerAddr, err := txscript.ExtractScriptPubKeyAddress(&externalapi.ScriptPublicKey{Script: redeemScript, Version: 0}, &dagconfig.MainnetParams)
	if err != nil {
		t.Fatalf("ExtractScriptPubKeyAddress(inner): %v", err)
	}
	if innerAddr == nil || innerClass != txscript.PubKeyHashTy {
		t.Fatalf("unexpected inner extraction: class=%v addr=%v", innerClass, innerAddr)
	}

	config := DefaultConfig(&dagconfig.MainnetParams)
	config.WalletFreezingEnabled = true
	config.FrozenAddresses = []string{innerAddr.EncodeAddress()}
	wfm := newWalletFreezingManager(config)

	tx := &externalapi.DomainTransaction{
		Version: constants.MaxTransactionVersion,
		Inputs: []*externalapi.DomainTransactionInput{
			{
				SignatureScript: signatureScript,
				UTXOEntry:       &testUTXOEntry{scriptPublicKey: &externalapi.ScriptPublicKey{Script: p2shScript, Version: 0}},
			},
		},
		Outputs: []*externalapi.DomainTransactionOutput{},
	}

	addresses := wfm.extractAddressesFromTransaction(tx)
	foundP2SH := false
	foundInner := false
	for _, a := range addresses {
		if a == p2shAddr.EncodeAddress() {
			foundP2SH = true
		}
		if a == innerAddr.EncodeAddress() {
			foundInner = true
		}
	}
	if !foundP2SH || !foundInner {
		t.Fatalf("expected extracted addresses to include both p2sh=%q and inner=%q; got %v", p2shAddr.EncodeAddress(), innerAddr.EncodeAddress(), addresses)
	}

	isFrozen, frozenAddresses := wfm.isWalletFrozen(tx)
	if !isFrozen {
		t.Fatalf("expected tx to be frozen by inner address; extracted=%v frozen=%v", addresses, frozenAddresses)
	}
}
