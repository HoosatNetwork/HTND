package txscript

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/util"
)

func filled(n int, v byte) []byte { return bytes.Repeat([]byte{v}, n) }

// Every presized helper must emit the bytes a default-capacity builder emits, in a buffer with no slack.
func TestSizedBuildersMatchDefaultCapacity(t *testing.T) {
	pk, pkECDSA, hash := filled(32, 7), filled(33, 8), filled(32, 9)

	pkh := func(checkSig byte) ([]byte, error) {
		return NewScriptBuilder().AddOp(OpDup).AddOp(OpBlake2b).AddData(hash).AddOp(OpEqualVerify).AddOp(checkSig).Script()
	}
	tests := []struct {
		name  string
		sized func() ([]byte, error)
		plain func() ([]byte, error)
	}{
		{"p2pk", func() ([]byte, error) { return payToPubKeyScript(pk) },
			func() ([]byte, error) { return NewScriptBuilder().AddData(pk).AddOp(OpCheckSig).Script() }},
		{"p2pk ecdsa", func() ([]byte, error) { return payToPubKeyScriptECDSA(pkECDSA) },
			func() ([]byte, error) { return NewScriptBuilder().AddData(pkECDSA).AddOp(OpCheckSigECDSA).Script() }},
		{"p2pkh", func() ([]byte, error) { return payToPubKeyHashScript(hash) },
			func() ([]byte, error) { return pkh(OpCheckSig) }},
		{"p2pkh ecdsa", func() ([]byte, error) { return payToPubKeyHashScriptECDSA(hash) },
			func() ([]byte, error) { return pkh(OpCheckSigECDSA) }},
		{"p2pkh mldsa44", func() ([]byte, error) { return payToPubKeyHashScriptMLDSA44(hash) },
			func() ([]byte, error) { return pkh(OpCheckSigMLDSA44) }},
		{"p2sh", func() ([]byte, error) { return payToScriptHashScript(hash) },
			func() ([]byte, error) {
				return NewScriptBuilder().AddOp(OpBlake2b).AddData(hash).AddOp(OpEqual).Script()
			}},
		{"p2sh from redeem script", func() ([]byte, error) { return PayToScriptHashScript(pk) },
			func() ([]byte, error) {
				return NewScriptBuilder().AddOp(OpBlake2b).AddData(util.HashBlake2b(pk)).AddOp(OpEqual).Script()
			}},
		{"multisig pkh", func() ([]byte, error) { return payToMultiSigPKHScript(hash) },
			func() ([]byte, error) { return pkh(OpCheckSig) }},
		{"multisig", func() ([]byte, error) { return PayToMultiSigScript([][]byte{pk, pk, pk}, 2, false) },
			func() ([]byte, error) {
				return NewScriptBuilder().AddInt64(2).AddData(pk).AddData(pk).AddData(pk).AddInt64(3).AddOp(OpCheckMultiSig).Script()
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sized, err := test.sized()
			if err != nil {
				t.Fatal(err)
			}
			plain, err := test.plain()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sized, plain) {
				t.Fatalf("sized %x != default %x", sized, plain)
			}
			if cap(sized) != len(sized) {
				t.Fatalf("cap %d != len %d", cap(sized), len(sized))
			}
		})
	}
}

func TestNewScriptBuilderWithCapacity(t *testing.T) {
	if got := cap(NewScriptBuilderWithCapacity(-5).script); got != 0 {
		t.Fatalf("negative capacity gave cap %d", got)
	}
	if got := cap(NewScriptBuilder().script); got != defaultScriptAlloc {
		t.Fatalf("default cap %d", got)
	}
	// Capacity is a hint only: limits still apply and the buffer still grows.
	script, err := NewScriptBuilderWithCapacity(1).AddData(filled(300, 1)).Script()
	if err != nil || len(script) != 303 {
		t.Fatalf("len %d err %v", len(script), err)
	}
	if _, err := NewScriptBuilderWithCapacity(0).AddData(filled(MaxScriptElementSize+1, 1)).Script(); err == nil {
		t.Fatal("expected oversize element error")
	}
}

// The push-length encodings must match the old temporary-buffer ones byte for byte.
func TestAddDataLengthPrefixes(t *testing.T) {
	script, _ := NewScriptBuilder().AddData(filled(300, 5)).Script()
	if !bytes.Equal(script[:3], []byte{OpPushData2, 0x2c, 0x01}) {
		t.Fatalf("pushdata2 prefix %x", script[:3])
	}
	script, _ = NewScriptBuilder().AddFullData(filled(70000, 1)).Script()
	if !bytes.Equal(script[:5], []byte{OpPushData4, 0x70, 0x11, 0x01, 0x00}) {
		t.Fatalf("pushdata4 prefix %x", script[:5])
	}
	script, _ = NewScriptBuilder().AddLockTimeNumber(0x0102030405).Script()
	if !bytes.Equal(script, []byte{0x05, 5, 4, 3, 2, 1}) {
		t.Fatalf("locktime %x", script)
	}
}

func BenchmarkPayToAddrScript(b *testing.B) {
	p := util.Bech32PrefixHoosat
	pk, _ := util.NewAddressPublicKey(filled(32, 7), p)
	pkE, _ := util.NewAddressPublicKeyECDSA(append([]byte{2}, filled(32, 7)...), p)
	pkh, _ := util.NewAddressPublicKeyHashFromHash(filled(32, 9), p)
	pkhE, _ := util.NewAddressPublicKeyHashECDSAFromHash(filled(32, 9), p)
	pkhM, _ := util.NewAddressPublicKeyHashMLDSA44FromHash(filled(32, 9), p)
	sh, _ := util.NewAddressScriptHashFromHash(filled(32, 9), p)
	for _, c := range []struct {
		name string
		addr util.Address
	}{{"pubkey", pk}, {"pubkey-ecdsa", pkE}, {"pubkeyhash", pkh}, {"pubkeyhash-ecdsa", pkhE},
		{"pubkeyhash-mldsa44", pkhM}, {"scripthash", sh}} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := PayToAddrScript(c.addr); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
