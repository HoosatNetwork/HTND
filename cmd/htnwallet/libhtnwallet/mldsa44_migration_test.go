package libhtnwallet_test

import (
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

// mldsa44FormAddress returns the address of the ML-DSA-44 key at mnemonic's index in form, and the
// redeem script a spender of it carries (nil unless form is P2SH).
func mldsa44FormAddress(t *testing.T, params *dagconfig.Params, mnemonic string, index uint32,
	form libhtnwallet.MLDSA44AddressForm,
) (util.Address, []byte) {
	t.Helper()
	publicKeyHashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.ExternalKeychain, index, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	publicKeyHash := publicKeyHashes[0]

	var address util.Address
	var redeemScript []byte
	switch form {
	case libhtnwallet.MLDSA44AddressFormP2PKH:
		address, err = libhtnwallet.MLDSA44Address(params, publicKeyHash)
	case libhtnwallet.MLDSA44AddressFormP2SH:
		address, err = libhtnwallet.MLDSA44ScriptHashAddress(params, publicKeyHash)
		if err == nil {
			redeemScript, err = libhtnwallet.MLDSA44SingleSigRedeemScript(publicKeyHash)
		}
	}
	if err != nil {
		t.Fatalf("ML-DSA-44 %s address: %+v", form, err)
	}
	return address, redeemScript
}

// TestMigrateToMLDSA44 pins that a coin held by any Schnorr or ECDSA single-sig address type can be
// sent to an ML-DSA-44 address of every form - the way a wallet moves its funds to quantum-safe keys -
// and that the coin it lands as is then spendable with the wallet's ML-DSA-44 key. Both hops go
// through the wallet path (CreateUnsignedTransaction, Sign, ExtractTransaction), mempool admission and
// consensus.
func TestMigrateToMLDSA44(t *testing.T) {
	sourceTypes := []struct {
		name string
		typ  libhtnwallet.SingleSigAddressType
	}{
		{"P2PK", libhtnwallet.SingleSigAddressTypeP2PK},
		{"P2PKH", libhtnwallet.SingleSigAddressTypeP2PKH},
		{"P2SH", libhtnwallet.SingleSigAddressTypeP2SH},
	}
	forms := []libhtnwallet.MLDSA44AddressForm{
		libhtnwallet.MLDSA44AddressFormP2PKH,
		libhtnwallet.MLDSA44AddressFormP2SH,
	}
	wantScriptClass := map[libhtnwallet.MLDSA44AddressForm]txscript.ScriptClass{
		libhtnwallet.MLDSA44AddressFormP2PKH: txscript.PubKeyHashMLDSA44Ty,
		libhtnwallet.MLDSA44AddressFormP2SH:  txscript.ScriptHashTy,
	}

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		if consensusConfig.Name != dagconfig.MainnetParams.Name {
			return
		}
		params := &consensusConfig.Params
		for _, ecdsa := range []bool{false, true} {
			signatureName := "Schnorr"
			if ecdsa {
				signatureName = "ECDSA"
			}
			for _, source := range sourceTypes {
				for _, form := range forms {
					name := signatureName + "-" + source.name + "-to-MLDSA44-" + form.String()
					t.Run(name, func(t *testing.T) {
						consensusConfig.BlockCoinbaseMaturity = 0
						consensusConfig.HardForkGates.MLDSA44SignaturesBlockVersion = 1
						tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMigrateToMLDSA44"+name)
						if err != nil {
							t.Fatalf("Error setting up tc: %+v", err)
						}
						defer teardown(false)

						mnemonic, err := libhtnwallet.CreateMnemonic()
						if err != nil {
							t.Fatalf("CreateMnemonic: %+v", err)
						}
						publicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
						if err != nil {
							t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
						}
						publicKeys := []string{publicKey}
						const legacyPath = "m/0/1"
						legacyAddress, err := libhtnwallet.AddressWithSingleSigAddressType(params, publicKeys, 1,
							legacyPath, ecdsa, source.typ)
						if err != nil {
							t.Fatalf("AddressWithSingleSigAddressType: %+v", err)
						}
						legacyScript, err := txscript.PayToAddrScript(legacyAddress)
						if err != nil {
							t.Fatalf("PayToAddrScript: %+v", err)
						}

						const mldsa44Index = 2
						mldsa44Address, redeemScript := mldsa44FormAddress(t, params, mnemonic, mldsa44Index, form)

						fundingBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash},
							&externalapi.DomainCoinbaseData{ScriptPublicKey: legacyScript}, nil)
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
						coinbase := block1.Transactions[0]

						// Hop 1: the legacy coin to the ML-DSA-44 address.
						migration := signAndExtract(t, params, mnemonic, publicKeys, ecdsa, mldsa44Address, &libhtnwallet.UTXO{
							Outpoint:       &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(coinbase), Index: 0},
							UTXOEntry:      utxo.NewUTXOEntry(coinbase.Outputs[0].Value, coinbase.Outputs[0].ScriptPublicKey, true, 0),
							DerivationPath: legacyPath,
						})
						if got := txscript.GetScriptClass(migration.Outputs[0].ScriptPublicKey.Script); got != wantScriptClass[form] {
							t.Fatalf("the migration pays a %s output, want %s", got, wantScriptClass[form])
						}
						if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(migration, false, false, true); err != nil {
							t.Fatalf("mempool rejected the send to ML-DSA-44 %s: %+v", form, err)
						}
						block2Hash, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{block1Hash}, nil,
							[]*externalapi.DomainTransaction{migration})
						if err != nil {
							t.Fatalf("AddBlock with the migration: %+v", err)
						}
						migrated := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(migration), Index: 0}
						if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(migrated) {
							t.Fatalf("the migration wasn't accepted in the DAG")
						}

						// Hop 2: the migrated coin, now spent with the ML-DSA-44 key.
						spend := signAndExtract(t, params, mnemonic, publicKeys, ecdsa, legacyAddress, &libhtnwallet.UTXO{
							Outpoint:       migrated,
							UTXOEntry:      utxo.NewUTXOEntry(migration.Outputs[0].Value, migration.Outputs[0].ScriptPublicKey, false, 0),
							DerivationPath: libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, mldsa44Index),
							RedeemScript:   redeemScript,
						})
						if spend.Inputs[0].SigOpCount != 1 {
							t.Fatalf("SigOpCount of the ML-DSA-44 %s spend is %d, want 1", form, spend.Inputs[0].SigOpCount)
						}
						if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(spend, false, false, true); err != nil {
							t.Fatalf("mempool rejected spending the ML-DSA-44 %s coin: %+v", form, err)
						}
						_, virtualChangeSet, err = tc.AddBlock([]*externalapi.DomainHash{block2Hash}, nil,
							[]*externalapi.DomainTransaction{spend})
						if err != nil {
							t.Fatalf("AddBlock spending the ML-DSA-44 %s coin: %+v", form, err)
						}
						spent := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(spend), Index: 0}
						if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(spent) {
							t.Fatalf("spending the ML-DSA-44 %s coin wasn't accepted in the DAG", form)
						}
					})
				}
			}
		}
	})
}

// TestMLDSA44SignRefusesOtherKeysForms pins that a signer only signs a P2PKH or P2SH ML-DSA-44 input
// locked to its own key, and refuses a P2SH input whose carried redeem script is not the one the
// output commits to.
func TestMLDSA44SignRefusesOtherKeysForms(t *testing.T) {
	params := &dagconfig.MainnetParams
	owner, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	stranger, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	strangerPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, stranger, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	path := libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1)

	unsigned := func(address util.Address, redeemScript []byte) []byte {
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatalf("PayToAddrScript: %+v", err)
		}
		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction([]string{strangerPublicKey}, 1,
			[]*libhtnwallet.Payment{{Address: address, Amount: 1000}},
			[]*libhtnwallet.UTXO{{
				Outpoint:       &externalapi.DomainOutpoint{Index: 0},
				UTXOEntry:      utxo.NewUTXOEntry(100_000, scriptPublicKey, false, 0),
				DerivationPath: path,
				RedeemScript:   redeemScript,
			}}, nil)
		if err != nil {
			t.Fatalf("CreateUnsignedTransaction: %+v", err)
		}
		return unsignedTransaction
	}

	for _, form := range []libhtnwallet.MLDSA44AddressForm{libhtnwallet.MLDSA44AddressFormP2PKH, libhtnwallet.MLDSA44AddressFormP2SH} {
		address, redeemScript := mldsa44FormAddress(t, params, owner, 1, form)
		if _, err := libhtnwallet.Sign(params, []string{stranger}, unsigned(address, redeemScript), false); err == nil {
			t.Fatalf("another mnemonic signed an ML-DSA-44 %s input", form)
		}
		if _, err := libhtnwallet.Sign(params, []string{owner}, unsigned(address, redeemScript), false); err != nil {
			t.Fatalf("the owner could not sign its ML-DSA-44 %s input: %+v", form, err)
		}
	}

	// A P2SH input carrying the redeem script of another of the owner's keys.
	address, _ := mldsa44FormAddress(t, params, owner, 1, libhtnwallet.MLDSA44AddressFormP2SH)
	_, otherRedeemScript := mldsa44FormAddress(t, params, owner, 2, libhtnwallet.MLDSA44AddressFormP2SH)
	if _, err := libhtnwallet.Sign(params, []string{owner}, unsigned(address, otherRedeemScript), false); err == nil {
		t.Fatalf("signed a P2SH input whose redeem script does not match its output")
	}
}

// signAndExtract pays all of input, less a fee, to destination through the wallet's own
// build-sign-extract path.
func signAndExtract(t *testing.T, params *dagconfig.Params, mnemonic string, publicKeys []string, ecdsa bool,
	destination util.Address, input *libhtnwallet.UTXO,
) *externalapi.DomainTransaction {
	t.Helper()
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(publicKeys, 1,
		[]*libhtnwallet.Payment{{Address: destination, Amount: input.UTXOEntry.Amount() - 100_000}},
		[]*libhtnwallet.UTXO{input}, nil)
	if err != nil {
		t.Fatalf("CreateUnsignedTransaction: %+v", err)
	}
	signedTransaction, err := libhtnwallet.Sign(params, []string{mnemonic}, unsignedTransaction, ecdsa)
	if err != nil {
		t.Fatalf("Sign: %+v", err)
	}
	tx, err := libhtnwallet.ExtractTransaction(signedTransaction, ecdsa)
	if err != nil {
		t.Fatalf("ExtractTransaction: %+v", err)
	}
	return tx
}
