package libhtnwallet_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	miningmanagermodel "github.com/HoosatNetwork/HTND/v2/domain/miningmanager/model"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// A fixed BIP39 test mnemonic (the "abandon ... art" vector), so derived keys can be pinned.
const mldsa44TestMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"

// TestMLDSA44KeyDerivationIsPinned pins the mnemonic -> ML-DSA-44 key derivation. Changing it would
// leave every coin already sent to an ML-DSA-44 address of an existing wallet unspendable from a
// restored one, so a failure here is a compatibility break, not a stale fixture.
func TestMLDSA44KeyDerivationIsPinned(t *testing.T) {
	hashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mldsa44TestMnemonic, libhtnwallet.ExternalKeychain, 0, 2, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	want := []string{
		"8ae134e769262d58538728630c3f1ea1273c7df4f69dca7ef145807338d5a27e",
		"336f88af3294e500677e1caee2ee39c52ca85cb12219b47831cb190d2bca051c",
	}
	for i, hash := range hashes {
		if got := hex.EncodeToString(hash); got != want[i] {
			t.Errorf("ML-DSA-44 public key hash at m/0/%d: got %s, want %s", i, got, want[i])
		}
	}

	// The pool and the signing path must agree on which key sits at a path.
	publicKey, _, err := libhtnwallet.MLDSA44KeyFromMnemonic(mldsa44TestMnemonic, libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1), false)
	if err != nil {
		t.Fatalf("MLDSA44KeyFromMnemonic: %+v", err)
	}
	if got := hex.EncodeToString(util.HashBlake2b(publicKey.Bytes())); got != hex.EncodeToString(hashes[1]) {
		t.Fatalf("MLDSA44KeyFromMnemonic and MLDSA44PublicKeyHashes disagree on m/0/1")
	}

	// The internal chain is a different key, not the external one reused.
	internal, err := libhtnwallet.MLDSA44PublicKeyHashes(mldsa44TestMnemonic, libhtnwallet.InternalKeychain, 1, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	if hex.EncodeToString(internal[0]) == hex.EncodeToString(hashes[1]) {
		t.Fatalf("m/1/1 and m/0/1 derived the same ML-DSA-44 key")
	}
}

// mldsa44WalletSpend funds the ML-DSA-44 address at m/0/1 of mnemonic with a coinbase, then builds,
// signs and extracts a transaction spending it to the ML-DSA-44 address at m/1/1, exactly as the
// wallet daemon and the signer do.
func mldsa44WalletSpend(t *testing.T, tc testapi.TestConsensus, consensusConfig *consensus.Config, mnemonic string) (
	*externalapi.DomainTransaction, *externalapi.DomainHash,
) {
	t.Helper()
	params := &consensusConfig.Params
	sourcePath := libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1)
	sourceHashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.ExternalKeychain, 1, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	sourceAddress, err := libhtnwallet.MLDSA44Address(params, sourceHashes[0])
	if err != nil {
		t.Fatalf("MLDSA44Address: %+v", err)
	}
	changeHashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.InternalKeychain, 1, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	destinationAddress, err := libhtnwallet.MLDSA44Address(params, changeHashes[0])
	if err != nil {
		t.Fatalf("MLDSA44Address: %+v", err)
	}
	sourceScript, err := txscript.PayToAddrScript(sourceAddress)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
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

	publicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction([]string{publicKey}, 1,
		[]*libhtnwallet.Payment{{Address: destinationAddress, Amount: block1TxOut.Value - 100_000}},
		[]*libhtnwallet.UTXO{{
			Outpoint: &externalapi.DomainOutpoint{
				TransactionID: *consensushashing.TransactionID(block1.Transactions[0]),
				Index:         0,
			},
			UTXOEntry:      utxo.NewUTXOEntry(block1TxOut.Value, block1TxOut.ScriptPublicKey, true, 0),
			DerivationPath: sourcePath,
		}}, nil)
	if err != nil {
		t.Fatalf("CreateUnsignedTransaction: %+v", err)
	}

	signedTransaction, err := libhtnwallet.Sign(params, []string{mnemonic}, unsignedTransaction, false)
	if err != nil {
		t.Fatalf("Sign: %+v", err)
	}
	tx, err := libhtnwallet.ExtractTransaction(signedTransaction, false)
	if err != nil {
		t.Fatalf("ExtractTransaction: %+v", err)
	}
	return tx, block1Hash
}

func newMLDSA44TestMempool(tc testapi.TestConsensus) miningmanagermodel.Mempool {
	tcAsConsensus := tc.(externalapi.Consensus)
	tcAsConsensusPointer := &tcAsConsensus
	// These tests pin ML-DSA-44 script admission, not coin age: the coins they spend are a few DAA
	// old, so the input minimum-age policy is switched off here.
	mempoolConfig := mempool.DefaultConfig(tc.DAGParams())
	mempoolConfig.InputMinAgeDAAScore = 0
	return mempool.New(mempoolConfig, consensusreference.NewConsensusReference(&tcAsConsensusPointer))
}

// TestMLDSA44WalletSpendEndToEnd walks an ML-DSA-44 coin through the whole wallet path - key pool
// address, CreateUnsignedTransaction, Sign, ExtractTransaction - and into mempool admission and
// consensus acceptance, on a network where ML-DSA-44 is active from its first block.
func TestMLDSA44WalletSpendEndToEnd(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		if consensusConfig.Name != dagconfig.MainnetParams.Name {
			return
		}
		consensusConfig.BlockCoinbaseMaturity = 0
		consensusConfig.MLDSA44SignaturesBlockVersion = 1
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMLDSA44WalletSpendEndToEnd")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		tx, block1Hash := mldsa44WalletSpend(t, tc, consensusConfig, mldsa44TestMnemonic)

		// The signature script is <PUSHDATA2 sig||hashtype> <PUSHDATA2 pubkey>, which is what the
		// mempool's standard signature script limit was sized for.
		if got := len(tx.Inputs[0].SignatureScript); got != 3739 {
			t.Fatalf("ML-DSA-44 signature script is %d bytes, want 3739", got)
		}
		if tx.Inputs[0].SigOpCount != 1 {
			t.Fatalf("ML-DSA-44 input SigOpCount is %d, want 1", tx.Inputs[0].SigOpCount)
		}

		if _, err := newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(tx, false, false, true); err != nil {
			t.Fatalf("mempool rejected the ML-DSA-44 spend: %+v", err)
		}

		_, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{block1Hash}, nil, []*externalapi.DomainTransaction{tx})
		if err != nil {
			t.Fatalf("AddBlock with the ML-DSA-44 spend: %+v", err)
		}
		added := &externalapi.DomainOutpoint{TransactionID: *consensushashing.TransactionID(tx), Index: 0}
		if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(added) {
			t.Fatalf("the ML-DSA-44 spend wasn't accepted in the DAG")
		}
	})
}

// TestMLDSA44WalletSpendRejectedBeforeActivation is the same spend on a network still below
// MLDSA44SignaturesBlockVersion: the node must refuse it, whatever the wallet does.
func TestMLDSA44WalletSpendRejectedBeforeActivation(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		if consensusConfig.Name != dagconfig.MainnetParams.Name {
			return
		}
		consensusConfig.BlockCoinbaseMaturity = 0
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestMLDSA44WalletSpendRejectedBeforeActivation")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		tx, _ := mldsa44WalletSpend(t, tc, consensusConfig, mldsa44TestMnemonic)
		_, err = newMLDSA44TestMempool(tc).ValidateAndInsertTransaction(tx, false, false, true)
		if err == nil {
			t.Fatalf("the mempool accepted an ML-DSA-44 spend below MLDSA44SignaturesBlockVersion")
		}
		// Before activation 0xa6 counts as 0 sigops, so the SigOpCount of 1 the wallet declares is
		// refused first; the oversized pushes would be refused next.
		if !strings.Contains(err.Error(), "ErrWrongSigOpCount") && !strings.Contains(err.Error(), "exceeds max allowed size") {
			t.Fatalf("expected a pre-activation ML-DSA-44 rejection, got: %+v", err)
		}
	})
}

// TestMLDSA44SignRefusesForeignInput pins that a mnemonic whose key at the input's path does not hash
// to the output's key hash signs nothing, rather than producing a signature consensus would reject.
func TestMLDSA44SignRefusesForeignInput(t *testing.T) {
	otherMnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	params := &dagconfig.MainnetParams
	hashes, err := libhtnwallet.MLDSA44PublicKeyHashes(mldsa44TestMnemonic, libhtnwallet.ExternalKeychain, 1, 1, false)
	if err != nil {
		t.Fatalf("MLDSA44PublicKeyHashes: %+v", err)
	}
	address, err := libhtnwallet.MLDSA44Address(params, hashes[0])
	if err != nil {
		t.Fatalf("MLDSA44Address: %+v", err)
	}
	script, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}
	otherPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, otherMnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction([]string{otherPublicKey}, 1,
		[]*libhtnwallet.Payment{{Address: address, Amount: 1000}},
		[]*libhtnwallet.UTXO{{
			Outpoint:       &externalapi.DomainOutpoint{Index: 0},
			UTXOEntry:      utxo.NewUTXOEntry(100_000, script, false, 0),
			DerivationPath: libhtnwallet.MLDSA44DerivationPath(libhtnwallet.ExternalKeychain, 1),
		}}, nil)
	if err != nil {
		t.Fatalf("CreateUnsignedTransaction: %+v", err)
	}
	if _, err := libhtnwallet.Sign(params, []string{otherMnemonic}, unsignedTransaction, false); err == nil {
		t.Fatalf("a foreign mnemonic signed an ML-DSA-44 input it does not own")
	}
}
