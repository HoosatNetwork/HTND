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

// TestMigrateToMLDSA44 pins that a coin held by any Schnorr or ECDSA single-sig address type can be
// sent to an ML-DSA-44 address - the way a wallet moves its funds to quantum-safe keys - and that the
// coin it lands as is then spendable with the wallet's ML-DSA-44 key. Both hops go through the wallet
// path (CreateUnsignedTransaction, Sign, ExtractTransaction), mempool admission and consensus.
func TestMigrateToMLDSA44(t *testing.T) {
	sourceTypes := []struct {
		name string
		typ  libhtnwallet.SingleSigAddressType
	}{
		{"P2PK", libhtnwallet.SingleSigAddressTypeP2PK},
		{"P2PKH", libhtnwallet.SingleSigAddressTypeP2PKH},
		{"P2SH", libhtnwallet.SingleSigAddressTypeP2SH},
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
				t.Run(signatureName+"-"+source.name, func(t *testing.T) {
					consensusConfig.BlockCoinbaseMaturity = 0
					consensusConfig.MLDSA44SignaturesBlockVersion = 1
					tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
						"TestMigrateToMLDSA44"+signatureName+source.name)
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

					mldsa44Path := libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 2)
					mldsa44Hashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.ExternalKeychain, 2, 1, false)
					if err != nil {
						t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
					}
					mldsa44Address, err := libhtnwallet.MLDSA44Address(params, mldsa44Hashes[0])
					if err != nil {
						t.Fatalf("MLDSA44Address: %+v", err)
					}

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
					if got := txscript.GetScriptClass(migration.Outputs[0].ScriptPublicKey.Script); got != txscript.PubKeyHashMLDSA44Ty {
						t.Fatalf("the migration pays a %s output, want %s", got, txscript.PubKeyHashMLDSA44Ty)
					}
					if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(migration, false, false, true); err != nil {
						t.Fatalf("mempool rejected the %s %s -> ML-DSA-44 send: %+v", signatureName, source.name, err)
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
					backToLegacy := signAndExtract(t, params, mnemonic, publicKeys, ecdsa, legacyAddress, &libhtnwallet.UTXO{
						Outpoint:       migrated,
						UTXOEntry:      utxo.NewUTXOEntry(migration.Outputs[0].Value, migration.Outputs[0].ScriptPublicKey, false, 0),
						DerivationPath: mldsa44Path,
					})
					if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(backToLegacy, false, false, true); err != nil {
						t.Fatalf("mempool rejected spending the migrated ML-DSA-44 coin: %+v", err)
					}
					_, virtualChangeSet, err = tc.AddBlock([]*externalapi.DomainHash{block2Hash}, nil,
						[]*externalapi.DomainTransaction{backToLegacy})
					if err != nil {
						t.Fatalf("AddBlock spending the migrated coin: %+v", err)
					}
					spent := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(backToLegacy), Index: 0}
					if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(spent) {
						t.Fatalf("spending the migrated ML-DSA-44 coin wasn't accepted in the DAG")
					}
				})
			}
		}
	})
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
