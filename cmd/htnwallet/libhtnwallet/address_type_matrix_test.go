package libhtnwallet_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensusreference"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
)

// TestAddressTypeMatrix walks every source x destination single-sig address type combination through
// the full path a compound or send takes - CreateUnsignedTransaction, Sign, ExtractTransaction,
// mempool admission, and consensus acceptance - to pin that no combination is special.
//
// Written while investigating auto-compound failing to deliver to P2PKH and P2SH wallets. It records
// the negative result: every combination builds, signs, and is accepted, so the transaction layer is
// not where that bug lives. It turned out to be in the compounder, which broadcasts only
// UnsignedTransactions[0] and drops the rest - and only P2PK inputs are small enough for an
// 88-input compound to stay under MaximumStandardTransactionMass and avoid being split in the first
// place (P2PK 98890, P2PKH 101794, P2SH 105138). Keeping this so the layer it clears stays cleared.
func TestAddressTypeMatrix(t *testing.T) {
	types := []struct {
		name string
		typ  libhtnwallet.SingleSigAddressType
	}{
		{"P2PK", libhtnwallet.SingleSigAddressTypeP2PK},
		{"P2PKH", libhtnwallet.SingleSigAddressTypeP2PKH},
		{"P2SH", libhtnwallet.SingleSigAddressTypeP2SH},
	}

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// One network is enough: this exercises address encodings and script templates, which do not
		// vary by network, and the matrix is nine full consensus setups per network.
		if consensusConfig.Name != dagconfig.MainnetParams.Name {
			return
		}
		params := &consensusConfig.Params
		for _, src := range types {
			for _, dst := range types {
				t.Run("from-"+src.name+"_"+dst.name, func(t *testing.T) {
					const ecdsa = false
					consensusConfig.BlockCoinbaseMaturity = 0
					tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMatrix"+src.name+dst.name)
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
					const minimumSignatures = 1
					const path = "m/1/2/3"

					// Source: P2PK, as an auto-compounding wallet holds.
					sourceAddress, err := libhtnwallet.AddressWithSingleSigAddressType(
						params, publicKeys, minimumSignatures, path, ecdsa, src.typ)
					if err != nil {
						t.Fatalf("source Address: %+v", err)
					}
					// Destination: the type under test.
					destinationAddress, err := libhtnwallet.AddressWithSingleSigAddressType(
						params, publicKeys, minimumSignatures, path, ecdsa, dst.typ)
					if err != nil {
						t.Fatalf("destination Address: %+v", err)
					}
					t.Logf("source=%s destination=%s", sourceAddress.EncodeAddress(), destinationAddress.EncodeAddress())

					sourceScript, err := txscript.PayToAddrScript(sourceAddress)
					if err != nil {
						t.Fatalf("PayToAddrScript(source): %+v", err)
					}

					fundingBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash},
						&externalapi.DomainCoinbaseData{ScriptPublicKey: sourceScript}, nil)
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
					selectedUTXOs := []*libhtnwallet.UTXO{{
						Outpoint: &externalapi.DomainOutpoint{
							TransactionID: *consensushashing.TransactionID(block1.Transactions[0]),
							Index:         0,
						},
						UTXOEntry:      utxo.NewUTXOEntry(block1TxOut.Value, block1TxOut.ScriptPublicKey, true, 0),
						DerivationPath: path,
					}}

					unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(publicKeys, minimumSignatures,
						[]*libhtnwallet.Payment{{Address: destinationAddress, Amount: block1TxOut.Value - 100_000}}, selectedUTXOs, nil)
					if err != nil {
						t.Fatalf("CreateUnsignedTransaction: %+v", err)
					}

					signedTx, err := libhtnwallet.Sign(params, []string{mnemonic}, unsignedTransaction, ecdsa)
					if err != nil {
						t.Fatalf("Sign: %+v", err)
					}
					tx, err := libhtnwallet.ExtractTransaction(signedTx, ecdsa)
					if err != nil {
						t.Fatalf("ExtractTransaction: %+v", err)
					}

					// The auto-compounder broadcasts through the node's mempool, which AddBlock bypasses.
					tcAsConsensus := tc.(externalapi.Consensus)
					tcAsConsensusPointer := &tcAsConsensus
					mp := mempool.New(mempool.DefaultConfig(tc.DAGParams()),
						consensusreference.NewConsensusReference(&tcAsConsensusPointer))
					if _, err := mp.ValidateAndInsertTransaction(tx, false, false, true); err != nil {
						t.Fatalf("MEMPOOL REJECTED %s->%s: %+v", src.name, dst.name, err)
					}
					t.Logf("%s->%s accepted", src.name, dst.name)

					_, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{block1Hash}, nil,
						[]*externalapi.DomainTransaction{tx})
					if err != nil {
						t.Fatalf("AddBlock with the spending transaction: %+v", err)
					}
					added := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(tx), Index: 0}
					if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(added) {
						t.Fatalf("Transaction wasn't accepted in the DAG")
					}
				})
			}
		}
	})
}
