package txscript

import (
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
)

// ML-DSA-44 multisig is a pay-to-script-hash redeem script built from OP_CHECKSIGMLDSA44 and existing
// opcodes; it needs no consensus rule beyond ScriptEnableMLDSA44. For each of the n keys, in order:
//
//	OP_IF OP_DUP OP_BLAKE2B <32-byte key hash> OP_EQUALVERIFY OP_CHECKSIGMLDSA44 OP_ELSE OP_0 OP_ENDIF OP_TOALTSTACK
//
// then the n results are summed back from the alt stack and compared with m:
//
//	OP_FROMALTSTACK (OP_FROMALTSTACK OP_ADD)*(n-1) <m> OP_GREATERTHANOREQUAL
//
// A signer i supplies <sig||hashtype> <pubkey> OP_1 and a non-signer OP_0. The script commits to key
// hashes rather than keys, so non-signers' 1312-byte keys never appear on chain.
//
// The limits come from consensus sizes, not from policy:
//   - The redeem script is pushed as one element, so it must fit MaxScriptElementSize (520): 44n+1
//     bytes allows n <= 11.
//   - The whole signature script must fit MaxScriptSize (10000), and each signer adds 3740 bytes, so
//     m <= 2. Raising either would be a further consensus change.
const (
	// MaxMLDSA44MultiSigKeys is the largest n an ML-DSA-44 multisig redeem script can hold.
	MaxMLDSA44MultiSigKeys = 11

	// MaxMLDSA44MultiSigSignatures is the largest m an ML-DSA-44 multisig can require.
	MaxMLDSA44MultiSigSignatures = 2
)

// MultiSigMLDSA44RedeemScript returns the m-of-n ML-DSA-44 multisig redeem script over the given
// BLAKE2b-256 public key hashes, in the order given.
func MultiSigMLDSA44RedeemScript(publicKeyHashes [][]byte, requiredSignatures int) ([]byte, error) {
	keyCount := len(publicKeyHashes)
	if keyCount < 2 || keyCount > MaxMLDSA44MultiSigKeys {
		return nil, errors.Errorf("an ML-DSA-44 multisig needs 2 to %d keys, got %d", MaxMLDSA44MultiSigKeys, keyCount)
	}
	if requiredSignatures < 1 || requiredSignatures > MaxMLDSA44MultiSigSignatures || requiredSignatures > keyCount {
		return nil, errors.Errorf("an ML-DSA-44 multisig can require 1 to %d signatures, and at most one per key; got %d of %d",
			MaxMLDSA44MultiSigSignatures, requiredSignatures, keyCount)
	}

	builder := NewScriptBuilder()
	for i, publicKeyHash := range publicKeyHashes {
		if len(publicKeyHash) != blake2b.Size256 {
			return nil, errors.Errorf("public key hash #%d is %d bytes, expected %d", i, len(publicKeyHash), blake2b.Size256)
		}
		builder.AddOp(OpIf).
			AddOp(OpDup).
			AddOp(OpBlake2b).
			AddData(publicKeyHash).
			AddOp(OpEqualVerify).
			AddOp(OpCheckSigMLDSA44).
			AddOp(OpElse).
			AddOp(Op0).
			AddOp(OpEndIf).
			AddOp(OpToAltStack)
	}
	builder.AddOp(OpFromAltStack)
	for i := 1; i < keyCount; i++ {
		builder.AddOp(OpFromAltStack).AddOp(OpAdd)
	}
	builder.AddInt64(int64(requiredSignatures)).AddOp(OpGreaterThanOrEqual)
	return builder.Script()
}

// ExtractMultiSigMLDSA44RedeemScript returns the required signature count and the public key hashes
// of an ML-DSA-44 multisig redeem script, or an error if script is not exactly one that
// MultiSigMLDSA44RedeemScript builds.
func ExtractMultiSigMLDSA44RedeemScript(script []byte) (requiredSignatures int, publicKeyHashes [][]byte, err error) {
	pops, err := ParseScript(script)
	if err != nil {
		return 0, nil, err
	}

	// n key blocks of 10 opcodes, then n FROMALTSTACK and n-1 ADD, then <m> and the comparison.
	keyCount := (len(pops) - 1) / 12
	if keyCount < 2 || keyCount > MaxMLDSA44MultiSigKeys || len(pops) != keyCount*12+1 {
		return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
	}

	keyBlock := []byte{OpIf, OpDup, OpBlake2b, OpData32, OpEqualVerify, OpCheckSigMLDSA44, OpElse, Op0, OpEndIf, OpToAltStack}
	publicKeyHashes = make([][]byte, keyCount)
	for i := range keyCount {
		for j, want := range keyBlock {
			if pops[i*len(keyBlock)+j].opcode.value != want {
				return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
			}
		}
		publicKeyHashes[i] = pops[i*len(keyBlock)+3].data
	}

	tail := pops[keyCount*len(keyBlock):]
	if tail[0].opcode.value != OpFromAltStack {
		return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
	}
	for i := 1; i < keyCount; i++ {
		if tail[2*i-1].opcode.value != OpFromAltStack || tail[2*i].opcode.value != OpAdd {
			return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
		}
	}
	requiredPop := tail[len(tail)-2]
	if !isSmallInt(requiredPop.opcode) || tail[len(tail)-1].opcode.value != OpGreaterThanOrEqual {
		return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
	}
	requiredSignatures = asSmallInt(requiredPop.opcode)
	if requiredSignatures < 1 || requiredSignatures > MaxMLDSA44MultiSigSignatures || requiredSignatures > keyCount {
		return 0, nil, errors.New("not an ML-DSA-44 multisig redeem script")
	}
	return requiredSignatures, publicKeyHashes, nil
}

// IsMultiSigMLDSA44RedeemScript reports whether script is an ML-DSA-44 multisig redeem script.
func IsMultiSigMLDSA44RedeemScript(script []byte) bool {
	_, _, err := ExtractMultiSigMLDSA44RedeemScript(script)
	return err == nil
}

// MultiSigMLDSA44SignatureScript returns the P2SH signature script spending redeemScript.
// signatures[i] and publicKeys[i] belong to the i-th key of the redeem script; a key that does not
// sign has a nil signature. Each signature carries its sighash type byte.
func MultiSigMLDSA44SignatureScript(redeemScript []byte, signatures [][]byte, publicKeys [][]byte) ([]byte, error) {
	requiredSignatures, publicKeyHashes, err := ExtractMultiSigMLDSA44RedeemScript(redeemScript)
	if err != nil {
		return nil, err
	}
	if len(signatures) != len(publicKeyHashes) || len(publicKeys) != len(publicKeyHashes) {
		return nil, errors.Errorf("got %d signatures and %d public keys for %d keys",
			len(signatures), len(publicKeys), len(publicKeyHashes))
	}

	signatureCount := 0
	for _, signature := range signatures {
		if signature != nil {
			signatureCount++
		}
	}
	if signatureCount < requiredSignatures {
		return nil, errors.Errorf("missing %d signatures", requiredSignatures-signatureCount)
	}

	// The first key's items must end up on top of the stack, so push the keys in reverse.
	builder := NewScriptBuilder()
	for i := len(publicKeyHashes) - 1; i >= 0; i-- {
		if signatures[i] == nil {
			builder.AddOp(Op0)
			continue
		}
		builder.AddFullData(signatures[i]).AddFullData(publicKeys[i]).AddOp(Op1)
	}
	builder.AddData(redeemScript)
	return builder.Script()
}
