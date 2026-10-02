package libhtnwallet_test

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// TestMLDSA44MultiSigKeysAreSeparateFromSingleSig pins that a mnemonic's multisig cosigner keys are
// not its single-sig keys, so using one mnemonic both ways never reuses an ML-DSA-44 key.
func TestMLDSA44MultiSigKeysAreSeparateFromSingleSig(t *testing.T) {
	singleSig, err := libhtnwallet.MLDSA44PublicKeyHashes(mldsa44TestMnemonic, libhtnwallet.ExternalKeychain, 0, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	multiSig, err := libhtnwallet.MLDSA44PublicKeyHashes(mldsa44TestMnemonic, libhtnwallet.ExternalKeychain, 0, 1, true)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	if bytes.Equal(singleSig[0], multiSig[0]) {
		t.Fatalf("the multisig and single-sig ML-DSA-44 keys at m/0/0 are the same key")
	}
}

type mldsa44Cosigner struct {
	mnemonic          string
	extendedPublicKey string
}

func newMLDSA44Cosigners(t *testing.T, params *dagconfig.Params, count int) []mldsa44Cosigner {
	t.Helper()
	cosigners := make([]mldsa44Cosigner, count)
	for i := range cosigners {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, true)
		if err != nil {
			t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
		}
		cosigners[i] = mldsa44Cosigner{mnemonic: mnemonic, extendedPublicKey: extendedPublicKey}
	}
	return cosigners
}

// TestMLDSA44MultiSigWalletSpendEndToEnd walks a 2-of-3 ML-DSA-44 multisig coin through the whole
// wallet path - each cosigner signing the unsigned transaction in turn, as it is passed around - and
// into mempool admission and consensus acceptance.
func TestMLDSA44MultiSigWalletSpendEndToEnd(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		if consensusConfig.Name != dagconfig.MainnetParams.Name {
			return
		}
		consensusConfig.BlockCoinbaseMaturity = 0
		consensusConfig.HardForkGates.MLDSA44SignaturesBlockVersion = 1
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMLDSA44MultiSigWalletSpendEndToEnd")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)
		params := &consensusConfig.Params

		const minimumSignatures = 2
		cosigners := newMLDSA44Cosigners(t, params, 3)
		path := libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1)

		// What each daemon builds from its own and its imported key pools.
		cosignerPublicKeyHashes := make(map[string][]byte, len(cosigners))
		extendedPublicKeys := make([]string, len(cosigners))
		for i, cosigner := range cosigners {
			hashes, err := libhtnwallet.MLDSA44PublicKeyHashes(cosigner.mnemonic, libhtnwallet.ExternalKeychain, 1, 1, true)
			if err != nil {
				t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
			}
			cosignerPublicKeyHashes[cosigner.extendedPublicKey] = hashes[0]
			extendedPublicKeys[i] = cosigner.extendedPublicKey
		}
		redeemScript, err := libhtnwallet.MLDSA44MultiSigRedeemScript(cosignerPublicKeyHashes, minimumSignatures)
		if err != nil {
			t.Fatalf("MLDSA44MultiSigRedeemScript: %+v", err)
		}
		address, err := libhtnwallet.MLDSA44MultiSigAddress(params, redeemScript)
		if err != nil {
			t.Fatalf("MLDSA44MultiSigAddress: %+v", err)
		}
		script, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatalf("PayToAddrScript: %+v", err)
		}

		fundingBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: script}, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		block1Hash, _, err := tc.AddBlock([]*externalapi.DomainHash{fundingBlockHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		block1, _, err := tc.GetBlock(block1Hash)
		if err != nil {
			t.Fatalf("GetBlock: %+v", err)
		}
		block1TxOut := block1.Transactions[0].Outputs[0]

		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(extendedPublicKeys, minimumSignatures,
			[]*libhtnwallet.Payment{{Address: address, Amount: block1TxOut.Value - 100_000}},
			[]*libhtnwallet.UTXO{{
				Outpoint: &externalapi.DomainOutpoint{
					TransactionID: *consensushashing.TransactionID(block1.Transactions[0]),
					Index:         0,
				},
				UTXOEntry:      utxo.NewUTXOEntry(block1TxOut.Value, block1TxOut.ScriptPublicKey, true, 0),
				DerivationPath: path,
				RedeemScript:   redeemScript,
			}}, nil)
		if err != nil {
			t.Fatalf("CreateUnsignedTransaction: %+v", err)
		}

		signedOnce, err := libhtnwallet.Sign(params, []string{cosigners[2].mnemonic}, unsignedTransaction, false)
		if err != nil {
			t.Fatalf("Sign by the third cosigner: %+v", err)
		}
		if fullySigned, err := libhtnwallet.IsTransactionFullySigned(signedOnce); err != nil || fullySigned {
			t.Fatalf("one signature of two reported fully signed (err %v)", err)
		}
		if _, err := libhtnwallet.ExtractTransaction(signedOnce, false); err == nil {
			t.Fatalf("extracted a 2-of-3 spend with one signature")
		}

		signedTwice, err := libhtnwallet.Sign(params, []string{cosigners[0].mnemonic}, signedOnce, false)
		if err != nil {
			t.Fatalf("Sign by the first cosigner: %+v", err)
		}
		tx, err := libhtnwallet.ExtractTransaction(signedTwice, false)
		if err != nil {
			t.Fatalf("ExtractTransaction: %+v", err)
		}
		if tx.Inputs[0].SigOpCount != 3 {
			t.Fatalf("SigOpCount is %d, want 3 (one per key in the redeem script)", tx.Inputs[0].SigOpCount)
		}
		t.Logf("2-of-3 ML-DSA-44 signature script: %d bytes", len(tx.Inputs[0].SignatureScript))

		if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(tx, false, false, true); err != nil {
			t.Fatalf("mempool rejected the ML-DSA-44 multisig spend: %+v", err)
		}
		_, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{block1Hash}, nil, []*externalapi.DomainTransaction{tx})
		if err != nil {
			t.Fatalf("AddBlock with the ML-DSA-44 multisig spend: %+v", err)
		}
		added := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(tx), Index: 0}
		if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(added) {
			t.Fatalf("the ML-DSA-44 multisig spend wasn't accepted in the DAG")
		}
	})
}

// TestMLDSA44MultiSigSignRefusesSwappedRedeemScript pins that a signer checks the redeem script it
// is handed against the output being spent, instead of trusting whatever key hashes it lists.
func TestMLDSA44MultiSigSignRefusesSwappedRedeemScript(t *testing.T) {
	params := &dagconfig.MainnetParams
	cosigners := newMLDSA44Cosigners(t, params, 2)
	path := libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1)

	redeemScriptFor := func(m uint32) []byte {
		hashes := make(map[string][]byte, len(cosigners))
		for _, cosigner := range cosigners {
			cosignerHashes, err := libhtnwallet.MLDSA44PublicKeyHashes(cosigner.mnemonic, libhtnwallet.ExternalKeychain, 1, 1, true)
			if err != nil {
				t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
			}
			hashes[cosigner.extendedPublicKey] = cosignerHashes[0]
		}
		redeemScript, err := libhtnwallet.MLDSA44MultiSigRedeemScript(hashes, m)
		if err != nil {
			t.Fatalf("MLDSA44MultiSigRedeemScript: %+v", err)
		}
		return redeemScript
	}
	oneOfTwo := redeemScriptFor(1)
	twoOfTwo := redeemScriptFor(2)

	p2sh, err := txscript.PayToScriptHashScript(twoOfTwo)
	if err != nil {
		t.Fatalf("PayToScriptHashScript: %+v", err)
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(
		[]string{cosigners[0].extendedPublicKey, cosigners[1].extendedPublicKey}, 1,
		[]*libhtnwallet.Payment{{Address: mustMLDSA44MultiSigAddress(t, params, twoOfTwo), Amount: 1000}},
		[]*libhtnwallet.UTXO{{
			Outpoint:       &externalapi.DomainOutpoint{Index: 0},
			UTXOEntry:      utxo.NewUTXOEntry(100_000, &externalapi.ScriptPublicKey{Script: p2sh}, false, 0),
			DerivationPath: path,
			RedeemScript:   oneOfTwo, // not the script the output commits to
		}}, nil)
	if err != nil {
		t.Fatalf("CreateUnsignedTransaction: %+v", err)
	}
	if _, err := libhtnwallet.Sign(params, []string{cosigners[0].mnemonic}, unsignedTransaction, false); err == nil {
		t.Fatalf("signed an input whose redeem script does not match its output")
	}
}

func mustMLDSA44MultiSigAddress(t *testing.T, params *dagconfig.Params, redeemScript []byte) util.Address {
	t.Helper()
	address, err := libhtnwallet.MLDSA44MultiSigAddress(params, redeemScript)
	if err != nil {
		t.Fatalf("MLDSA44MultiSigAddress: %+v", err)
	}
	return address
}
