package main

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestWebWalletSecretFromExport pins which secret of a web wallet export is stored: the mnemonic when it
// produces the export's master key, otherwise the master key, which the web wallet's addresses come from.
func TestWebWalletSecretFromExport(t *testing.T) {
	params := &dagconfig.MainnetParams
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	const masterKey = "xprv9s21ZrQH143K3GJpoapnV8SFfukcVBSfeCficPSGfubmSFDxo1kuHnLisriDvSnRRuL2Qrg5ggqHKNVpxR86QEC8w35uxmGoggxtQTPvfUu"
	otherMnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}

	tests := []struct {
		name   string
		export libhtnwallet.WebWalletExport
		want   string
	}{
		{"matching", libhtnwallet.WebWalletExport{PrivateKey: masterKey, SeedPhrase: mnemonic}, mnemonic},
		{"mismatching", libhtnwallet.WebWalletExport{PrivateKey: masterKey, SeedPhrase: otherMnemonic}, masterKey},
		{"master key only", libhtnwallet.WebWalletExport{PrivateKey: masterKey}, masterKey},
		{"mnemonic only", libhtnwallet.WebWalletExport{SeedPhrase: mnemonic}, mnemonic},
	}
	for _, test := range tests {
		got, err := webWalletSecretFromExport(params, &test.export)
		if err != nil {
			t.Fatalf("%s: %+v", test.name, err)
		}
		if got != test.want {
			t.Fatalf("%s: got %q, want %q", test.name, got, test.want)
		}
	}

	for _, export := range []libhtnwallet.WebWalletExport{{}, {SeedPhrase: "not a mnemonic"}, {PrivateKey: "not a key", SeedPhrase: mnemonic}} {
		if _, err := webWalletSecretFromExport(params, &export); err == nil {
			t.Fatalf("webWalletSecretFromExport(%+v) succeeded", export)
		}
	}
}
