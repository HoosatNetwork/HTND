package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/util"
)

func TestRefusedSendLeavesChangeUnissued(t *testing.T) {
	w := newMLWallet(t, 1, 1) // ML-DSA-44 keys for indexes 0-2
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	destination := w.address(t, ml)
	change := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain, mldsa44: true})
	if err := w.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}
	// 21 ML-DSA-44 inputs do not fit a standard transaction, and the amount needs all 21 coins.
	w.fund(t, same(ml, repeat(1_000_000, 21)...)...)

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := w.send(destination, 20_400_000, false, nil, false, nil)
		if err == nil || !strings.Contains(err.Error(), "standard limit") {
			t.Fatalf("attempt %d: got %v, want a refusal over the standard mass limit", attempt, err)
		}
		if slices.Contains(w.s.addressSet.strings(), change.String()) {
			t.Errorf("attempt %d exposed its change address in the balance query set", attempt)
		}
		assertInternalIndex(t, w, 0)
	}

	// The first issued change address is tracked and saved before the transaction leaves the daemon.
	transactions, err := w.send(destination, 5_000_000, false, nil, false, nil)
	if err != nil || len(transactions) != 1 {
		t.Fatalf("a send of six coins failed: %v", err)
	}
	if !slices.Contains(w.s.addressSet.strings(), change.String()) {
		t.Fatal("issued change address is missing from the balance query set")
	}
	assertInternalIndex(t, w, 1)
	// And a refusal after it leaves that index alone.
	if _, err := w.send(destination, 20_400_000, false, nil, false, nil); err == nil {
		t.Fatalf("the send over the standard mass limit was built")
	}
	assertInternalIndex(t, w, 1)
}

func TestDustChangeLeavesAddressUnissued(t *testing.T) {
	w := newSchnorrWallet(t)
	receive := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	change := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain})
	w.fund(t, same(receive, 1_000_000)...)
	if err := w.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}

	transactions, err := w.send(w.address(t, receive), 989_999, false, nil, false, nil)
	if err != nil {
		t.Fatalf("building send with a dust remainder: %v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
	tx, err := serialization.DeserializePartiallySignedTransaction(transactions[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Tx.Outputs) != 1 {
		t.Errorf("a transaction with a dust remainder has %d outputs, want 1", len(tx.Tx.Outputs))
	}
	if slices.Contains(w.s.addressSet.strings(), change.String()) {
		t.Error("a transaction without change exposed an unused change address")
	}
	assertInternalIndex(t, w, 0)
}

func TestSendSigningErrorLeavesChangeUnissued(t *testing.T) {
	w := newSchnorrWallet(t)
	const password = "test signing password"
	keysFile, err := keys.NewFileFromMnemonic(w.params, w.mnemonics[0], password)
	if err != nil {
		t.Fatal(err)
	}
	if err := keysFile.SetPath(w.params, w.s.keysFile.Path(), true); err != nil {
		t.Fatal(err)
	}
	if err := keysFile.Save(); err != nil {
		t.Fatal(err)
	}
	w.s.keysFile = keysFile
	receive := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	change := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain})
	w.fund(t, same(receive, 1_000_000)...)
	attachSendTestNode(t, w)
	request := &pb.SendRequest{ToAddress: w.address(t, receive).String(), Amount: 500_000, Password: "wrong password"}
	_, err = w.s.Send(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "message authentication failed") {
		t.Fatalf("signing returned %v, want the password error", err)
	}
	if slices.Contains(w.s.addressSet.strings(), change.String()) {
		t.Error("a signing refusal exposed its change address")
	}
	assertInternalIndex(t, w, 0)

	// With a watch-only keys file the signing helper can return an unsigned transaction.
	// The daemon must refuse it locally and still keep the change address unissued.
	w.s.keysFile.EncryptedMnemonics = nil
	request.Password = password
	if _, err := w.s.Send(context.Background(), request); err == nil || !strings.Contains(err.Error(), "missing signature") {
		t.Fatalf("sending with a watch-only file returned %v, want a missing signature error", err)
	}
	if slices.Contains(w.s.addressSet.strings(), change.String()) {
		t.Error("a transaction missing a signature exposed its change address")
	}
	assertInternalIndex(t, w, 0)
}

func assertInternalIndex(t *testing.T, w *feeWallet, want uint32) {
	t.Helper()
	if got := w.s.keysFile.LastUsedInternalIndex(); got != want {
		t.Errorf("internal index in memory is %d, want %d", got, want)
	}
	reloaded, err := keys.ReadKeysFile(w.params, w.s.keysFile.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.LastUsedInternalIndex(); got != want {
		t.Errorf("internal index on disk is %d, want %d", got, want)
	}
}

func TestDustSelfPaymentReservesDestination(t *testing.T) {
	for _, name := range []string{"same address", "same script with different encoding"} {
		t.Run(name, func(t *testing.T) {
			w, receive, firstChange, _ := freshSendFixture(t, "")
			secondChange := w.address(t, &walletAddress{index: 2, keyChain: libhtnwallet.InternalKeychain})
			paymentScript, err := txscript.PayToAddrScript(firstChange)
			if err != nil {
				t.Fatal(err)
			}
			destination := firstChange
			if name == "same script with different encoding" {
				destination, err = util.NewAddressMultiSig(paymentScript.Script, w.params.Prefix)
				if err != nil {
					t.Fatal(err)
				}
			}

			send := func(to util.Address, amount uint64) *serialization.PartiallySignedTransaction {
				t.Helper()
				transactions, err := w.send(to, amount, false, nil, false, nil)
				if err != nil || len(transactions) != 1 {
					t.Fatalf("got %d transactions, error %v, want one transaction", len(transactions), err)
				}
				tx, err := serialization.DeserializePartiallySignedTransaction(transactions[0])
				if err != nil {
					t.Fatal(err)
				}
				return tx
			}
			first := send(destination, 989_999)
			if len(first.Tx.Outputs) != 1 || !first.Tx.Outputs[0].ScriptPublicKey.Equal(paymentScript) {
				t.Fatal("want one output paying the next internal address")
			}
			assertInternalIndex(t, w, 1)
			assertChangeTracked(t, w, firstChange, true)

			second := send(receive, 500_000)
			nextScript, err := txscript.PayToAddrScript(secondChange)
			if err != nil {
				t.Fatal(err)
			}
			if len(second.Tx.Outputs) != 2 || !second.Tx.Outputs[1].ScriptPublicKey.Equal(nextScript) {
				t.Error("next transaction did not use a fresh change address")
			}
			assertInternalIndex(t, w, 2)
		})
	}
}

func TestSendDirectoryOpenFailureDoesNotConsumeChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions require Unix")
	}
	w := newMLWallet(t, 1, 1) // ML-DSA-44 keys for indexes 0-2
	receive := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	destination := w.address(t, receive)
	change := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain, mldsa44: true})
	w.fund(t, same(receive, 1_000_000)...)
	if err := w.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(w.s.keysFile.Path())
	if err := os.Chmod(dir, 0300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	if handle, err := os.Open(dir); err == nil {
		handle.Close()
		t.Skip("process can open an unreadable directory")
	}
	for range 2 {
		if transactions, err := w.send(destination, 500_000, false, nil, false, nil); err == nil || transactions != nil {
			t.Fatalf("send returned %d transactions and error %v, want a save refusal", len(transactions), err)
		}
		assertInternalIndex(t, w, 0)
		if _, tracked := w.s.addressSet[change.String()]; tracked {
			t.Error("the refused change address is visible to balance queries")
		}
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.send(destination, 500_000, false, nil, false, nil); err != nil {
		t.Fatalf("send after directory permissions were restored: %v", err)
	}
	assertInternalIndex(t, w, 1)
}
