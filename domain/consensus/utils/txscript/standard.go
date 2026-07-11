// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package txscript

import (
	"fmt"
	"strconv"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
	"github.com/Hoosat-Oy/HTND/util"
)

// ScriptClass is an enumeration for the list of standard types of script.
type ScriptClass byte

// Classes of script payment known about in the blockDAG.
const (
	NonStandardTy     ScriptClass = iota // None of the recognized forms.
	PubKeyTy                             // Pay to pubkey.
	PubKeyECDSATy                        // Pay to pubkey ECDSA.
	PubKeyHashTy                         // Pay to pubkey hash.
	PubKeyHashECDSATy                    // Pay to pubkey hash ECDSA.
	ScriptHashTy                         // Pay to script hash.
	MultiSigTy                           // Pay to multisig (direct OP_CHECKMULTISIG script).
	MultiSigECDSATy                     // Pay to multisig ECDSA (direct OP_CHECKMULTISIGECDSA script).
	MultiSigPKHTy                        // Pay to P2PKH-style multisig (hash of multisig script).
	MultiSigPKHECDSATy                  // Pay to P2PKH-style multisig ECDSA.
)

// Script public key versions for address types.
const (
	addressPublicKeyScriptPublicKeyVersion          = 0
	addressPublicKeyECDSAScriptPublicKeyVersion     = 0
	addressPublicKeyHashScriptPublicKeyVersion      = 0
	addressPublicKeyHashECDSAScriptPublicKeyVersion = 0
	addressScriptHashScriptPublicKeyVersion         = 0
	addressMultiSigScriptPublicKeyVersion           = 0
)

// scriptClassToName houses the human-readable strings which describe each
// script class.
var scriptClassToName = []string{
	NonStandardTy:     "nonstandard",
	PubKeyTy:          "pubkey",
	PubKeyECDSATy:     "pubkeyecdsa",
	PubKeyHashTy:      "pubkeyhash",
	PubKeyHashECDSATy: "pubkeyhashecdsa",
	ScriptHashTy:      "scripthash",
	MultiSigTy:        "multisig",
	MultiSigECDSATy:   "multisigecdsa",
	MultiSigPKHTy:     "multisigpkh",
	MultiSigPKHECDSATy: "multisigpkhecdsa",
}

// String implements the Stringer interface by returning the name of
// the enum script class. If the enum is invalid then "Invalid" will be
// returned.
func (t ScriptClass) String() string {
	if int(t) >= len(scriptClassToName) || int(t) < 0 {
		return "Invalid"
	}
	return scriptClassToName[t]
}

// isPayToPubkey returns true if the script passed is a pay-to-pubkey
// transaction, false otherwise.
func isPayToPubkey(pops []parsedOpcode) bool {
	return len(pops) == 2 &&
		pops[0].opcode.value == OpData32 &&
		pops[1].opcode.value == OpCheckSig
}

// isPayToPubkeyECDSA returns true if the script passed is an ECDSA pay-to-pubkey
// transaction, false otherwise.
func isPayToPubkeyECDSA(pops []parsedOpcode) bool {
	return len(pops) == 2 &&
		pops[0].opcode.value == OpData33 &&
		pops[1].opcode.value == OpCheckSigECDSA
}

// isPayToPubkeyHash returns true if the script passed is a pay-to-pubkey-hash
// transaction, false otherwise.
//
// Hoosat P2PKH (Schnorr) template:
// OP_DUP OP_BLAKE2B <32-byte hash> OP_EQUALVERIFY OP_CHECKSIG
func isPayToPubkeyHash(pops []parsedOpcode) bool {
	return len(pops) == 5 &&
		pops[0].opcode.value == OpDup &&
		pops[1].opcode.value == OpBlake2b &&
		pops[2].opcode.value == OpData32 &&
		pops[3].opcode.value == OpEqualVerify &&
		pops[4].opcode.value == OpCheckSig
}

// isPayToPubkeyHashECDSA returns true if the script passed is an ECDSA
// pay-to-pubkey-hash transaction, false otherwise.
//
// Hoosat P2PKH (ECDSA) template:
// OP_DUP OP_BLAKE2B <32-byte hash> OP_EQUALVERIFY OP_CHECKSIGECDSA
func isPayToPubkeyHashECDSA(pops []parsedOpcode) bool {
	return len(pops) == 5 &&
		pops[0].opcode.value == OpDup &&
		pops[1].opcode.value == OpBlake2b &&
		pops[2].opcode.value == OpData32 &&
		pops[3].opcode.value == OpEqualVerify &&
		pops[4].opcode.value == OpCheckSigECDSA
}

// isMultiSig returns true if the script passed is a direct multisig transaction,
// false otherwise.
//
// Multisig template:
// <m> <pub1> <pub2> ... <pubN> <n> OP_CHECKMULTISIG
//
// Note: 1-of-1 multisig is considered nonstandard as it's equivalent to P2PK.
func isMultiSig(pops []parsedOpcode) bool {
	if len(pops) < 4 {
		return false
	}
	
	// Last opcode must be OP_CHECKMULTISIG
	if pops[len(pops)-1].opcode.value != OpCheckMultiSig {
		return false
	}
	
	// Second to last must be an integer (n - total number of public keys)
	var n int
	if isSmallInt(pops[len(pops)-2].opcode) {
		n = int(pops[len(pops)-2].opcode.value - Op1 + 1)
	} else if pops[len(pops)-2].data != nil {
		m, err := makeScriptNum(pops[len(pops)-2].data, 4)
		if err != nil {
			return false
		}
		n = int(m)
	} else {
		return false
	}
	
	// First opcode must be an integer (m - required signatures)
	var m int
	if isSmallInt(pops[0].opcode) {
		m = int(pops[0].opcode.value - Op1 + 1)
	} else if pops[0].data != nil {
		mVal, err := makeScriptNum(pops[0].data, 4)
		if err != nil {
			return false
		}
		m = int(mVal)
	} else {
		return false
	}
	
	// 1-of-1 multisig is nonstandard (equivalent to P2PK)
	if m == 1 && n == 1 {
		return false
	}
	
	// Validate the number of public keys matches n
	// Expected: 1 (m) + n (pubkeys) + 1 (n) + 1 (CHECKMULTISIG) = n + 3
	if len(pops) != n+3 {
		return false
	}
	
	// All opcodes in between should be valid 32-byte Schnorr pubkeys
	for i := 1; i < len(pops)-2; i++ {
		if pops[i].data == nil || len(pops[i].data) != 32 {
			return false
		}
	}
	
	return true
}

// isMultiSigECDSA returns true if the script passed is a direct ECDSA multisig transaction,
// false otherwise.
//
// Multisig ECDSA template:
// <m> <pub1> <pub2> ... <pubN> <n> OP_CHECKMULTISIGECDSA
//
// Note: 1-of-1 multisig ECDSA is considered nonstandard as it's equivalent to P2PK ECDSA.
func isMultiSigECDSA(pops []parsedOpcode) bool {
	if len(pops) < 4 {
		return false
	}
	
	// Last opcode must be OP_CHECKMULTISIGECDSA
	if pops[len(pops)-1].opcode.value != OpCheckMultiSigECDSA {
		return false
	}
	
	// Second to last must be an integer (n - total number of public keys)
	var n int
	if isSmallInt(pops[len(pops)-2].opcode) {
		n = int(pops[len(pops)-2].opcode.value - Op1 + 1)
	} else if pops[len(pops)-2].data != nil {
		m, err := makeScriptNum(pops[len(pops)-2].data, 4)
		if err != nil {
			return false
		}
		n = int(m)
	} else {
		return false
	}
	
	// First opcode must be an integer (m - required signatures)
	var m int
	if isSmallInt(pops[0].opcode) {
		m = int(pops[0].opcode.value - Op1 + 1)
	} else if pops[0].data != nil {
		mVal, err := makeScriptNum(pops[0].data, 4)
		if err != nil {
			return false
		}
		m = int(mVal)
	} else {
		return false
	}
	
	// 1-of-1 multisig is nonstandard (equivalent to P2PK)
	if m == 1 && n == 1 {
		return false
	}
	
	// Validate the number of public keys matches n
	// Expected: 1 (m) + n (pubkeys) + 1 (n) + 1 (CHECKMULTISIGECDSA) = n + 3
	if len(pops) != n+3 {
		return false
	}
	
	// All opcodes in between should be valid 33-byte ECDSA pubkeys
	for i := 1; i < len(pops)-2; i++ {
		if pops[i].data == nil || len(pops[i].data) != 33 {
			return false
		}
	}
	
	return true
}

// isPayToMultiSigPKH returns true if the script passed is a P2PKH-style multisig transaction,
// false otherwise.
//
// P2PKH-style multisig template:
// OP_DUP OP_BLAKE2B <multisig-script-hash> OP_EQUALVERIFY OP_CHECKSIG
func isPayToMultiSigPKH(pops []parsedOpcode) bool {
	return len(pops) == 5 &&
		pops[0].opcode.value == OpDup &&
		pops[1].opcode.value == OpBlake2b &&
		pops[2].opcode.value == OpData32 &&
		pops[3].opcode.value == OpEqualVerify &&
		pops[4].opcode.value == OpCheckSig
}

// isPayToMultiSigPKHECDSA returns true if the script passed is an ECDSA P2PKH-style multisig transaction,
// false otherwise.
//
// P2PKH-style multisig ECDSA template:
// OP_DUP OP_BLAKE2B <multisig-script-hash> OP_EQUALVERIFY OP_CHECKSIGECDSA
func isPayToMultiSigPKHECDSA(pops []parsedOpcode) bool {
	return len(pops) == 5 &&
		pops[0].opcode.value == OpDup &&
		pops[1].opcode.value == OpBlake2b &&
		pops[2].opcode.value == OpData32 &&
		pops[3].opcode.value == OpEqualVerify &&
		pops[4].opcode.value == OpCheckSigECDSA
}

// scriptType returns the type of the script being inspected from the known
// standard types.
func typeOfScript(pops []parsedOpcode) ScriptClass {
	switch {
	case isPayToPubkey(pops):
		return PubKeyTy
	case isPayToPubkeyECDSA(pops):
		return PubKeyECDSATy
	case isPayToPubkeyHash(pops):
		return PubKeyHashTy
	case isPayToPubkeyHashECDSA(pops):
		return PubKeyHashECDSATy
	case isScriptHash(pops):
		return ScriptHashTy
	case isMultiSig(pops):
		return MultiSigTy
	case isMultiSigECDSA(pops):
		return MultiSigECDSATy
	case isPayToMultiSigPKH(pops):
		return MultiSigPKHTy
	case isPayToMultiSigPKHECDSA(pops):
		return MultiSigPKHECDSATy
	}
	return NonStandardTy
}

// GetScriptClass returns the class of the script passed.
//
// NonStandardTy will be returned when the script does not parse.
func GetScriptClass(script []byte) ScriptClass {
	pops, err := ParseScript(script)
	if err != nil {
		return NonStandardTy
	}
	return GetScriptClassFromParsedScript(pops)
}

func GetScriptClassFromParsedScript(pops []parsedOpcode) ScriptClass {
	return typeOfScript(pops)
}

// expectedInputs returns the number of arguments required by a script.
// If the script is of unknown type such that the number can not be determined
// then -1 is returned. We are an internal function and thus assume that class
// is the real class of pops (and we can thus assume things that were determined
// while finding out the type).
func expectedInputs(pops []parsedOpcode, class ScriptClass) int {
	switch class {

	case PubKeyTy:
		return 1

	case PubKeyHashTy:
		// P2PKH requires <sig> <pubkey> on the stack.
		return 2

	case PubKeyHashECDSATy:
		// P2PKH requires <sig> <pubkey> on the stack.
		return 2

	case ScriptHashTy:
		// Not including script. That is handled by the caller.
		return 1

	case MultiSigTy:
		// Direct multisig requires m signatures, where m is the first opcode
		if len(pops) > 0 {
			if isSmallInt(pops[0].opcode) {
				return int(pops[0].opcode.value - Op1 + 1)
			} else if pops[0].data != nil {
				m, err := makeScriptNum(pops[0].data, 4)
				if err == nil {
					return int(m)
				}
			}
		}
		return -1

	case MultiSigECDSATy:
		// Direct multisig ECDSA requires m signatures, where m is the first opcode
		if len(pops) > 0 {
			if isSmallInt(pops[0].opcode) {
				return int(pops[0].opcode.value - Op1 + 1)
			} else if pops[0].data != nil {
				m, err := makeScriptNum(pops[0].data, 4)
				if err == nil {
					return int(m)
				}
			}
		}
		return -1

	default:
		return -1
	}
}

// ScriptInfo houses information about a script pair that is determined by
// CalcScriptInfo.
type ScriptInfo struct {
	// ScriptPubKeyClass is the class of the public key script and is equivalent
	// to calling GetScriptClass on it.
	ScriptPubKeyClass ScriptClass

	// NumInputs is the number of inputs provided by the public key script.
	NumInputs int

	// ExpectedInputs is the number of outputs required by the signature
	// script and any pay-to-script-hash scripts. The number will be -1 if
	// unknown.
	ExpectedInputs int

	// SigOps is the number of signature operations in the script pair.
	SigOps int
}

// CalcScriptInfo returns a structure providing data about the provided script
// pair. It will error if the pair is in someway invalid such that they can not
// be analysed, i.e. if they do not parse or the scriptPubKey is not a push-only
// script
func CalcScriptInfo(sigScript, scriptPubKey []byte, isP2SH bool) (*ScriptInfo, error) {
	sigPops, err := ParseScript(sigScript)
	if err != nil {
		return nil, err
	}

	scriptPubKeyPops, err := ParseScript(scriptPubKey)
	if err != nil {
		return nil, err
	}

	// Push only sigScript makes little sense.
	si := new(ScriptInfo)
	si.ScriptPubKeyClass = typeOfScript(scriptPubKeyPops)

	// Can't have a signature script that doesn't just push data.
	if !isPushOnly(sigPops) {
		return nil, scriptError(ErrNotPushOnly,
			"signature script is not push only")
	}

	si.ExpectedInputs = expectedInputs(scriptPubKeyPops, si.ScriptPubKeyClass)

	// All entries pushed to stack (or are OP_RESERVED and exec will fail).
	si.NumInputs = len(sigPops)

	if si.ScriptPubKeyClass == ScriptHashTy && isP2SH {
		// The pay-to-hash-script is the final data push of the
		// signature script.
		script := sigPops[len(sigPops)-1].data
		shPops, err := ParseScript(script)
		if err != nil {
			return nil, err
		}

		shInputs := expectedInputs(shPops, typeOfScript(shPops))
		if shInputs == -1 {
			si.ExpectedInputs = -1
		} else {
			si.ExpectedInputs += shInputs
		}
		si.SigOps = getSigOpCount(shPops, true)
	} else {
		si.SigOps = getSigOpCount(scriptPubKeyPops, true)
	}

	return si, nil
}

// payToPubKeyScript creates a new script to pay a transaction
// output to a 32-byte pubkey.
func payToPubKeyScript(pubKey []byte) ([]byte, error) {
	return NewScriptBuilder().
		AddData(pubKey).
		AddOp(OpCheckSig).
		Script()
}

// payToPubKeyScript creates a new script to pay a transaction
// output to a 33-byte pubkey.
func payToPubKeyScriptECDSA(pubKey []byte) ([]byte, error) {
	return NewScriptBuilder().
		AddData(pubKey).
		AddOp(OpCheckSigECDSA).
		Script()
}

// payToPubKeyHashScript creates a new script to pay a transaction output to a
// 32-byte pubkey hash.
func payToPubKeyHashScript(pubKeyHash []byte) ([]byte, error) {
	return NewScriptBuilder().
		AddOp(OpDup).
		AddOp(OpBlake2b).
		AddData(pubKeyHash).
		AddOp(OpEqualVerify).
		AddOp(OpCheckSig).
		Script()
}

// payToPubKeyHashScriptECDSA creates a new script to pay a transaction output to a
// 32-byte pubkey hash, spending via ECDSA.
func payToPubKeyHashScriptECDSA(pubKeyHash []byte) ([]byte, error) {
	return NewScriptBuilder().
		AddOp(OpDup).
		AddOp(OpBlake2b).
		AddData(pubKeyHash).
		AddOp(OpEqualVerify).
		AddOp(OpCheckSigECDSA).
		Script()
}

// payToScriptHashScript creates a new script to pay a transaction output to a
// script hash. It is expected that the input is a valid hash.
func payToScriptHashScript(scriptHash []byte) ([]byte, error) {
	return NewScriptBuilder().AddOp(OpBlake2b).AddData(scriptHash).
		AddOp(OpEqual).Script()
}

// PayToAddrScript creates a new script to pay a transaction output to a the
// specified address.
func PayToAddrScript(addr util.Address) (*externalapi.ScriptPublicKey, error) {
	const nilAddrErrStr = "unable to generate payment script for nil address"
	switch addr := addr.(type) {
	case *util.AddressPublicKey:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		script, err := payToPubKeyScript(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}

		return &externalapi.ScriptPublicKey{Script: script, Version: addressPublicKeyScriptPublicKeyVersion}, err

	case *util.AddressPublicKeyECDSA:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		script, err := payToPubKeyScriptECDSA(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}

		return &externalapi.ScriptPublicKey{Script: script, Version: addressPublicKeyECDSAScriptPublicKeyVersion}, err

	case *util.AddressPublicKeyHash:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		script, err := payToPubKeyHashScript(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}
		return &externalapi.ScriptPublicKey{Script: script, Version: addressPublicKeyHashScriptPublicKeyVersion}, err

	case *util.AddressPublicKeyHashECDSA:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		script, err := payToPubKeyHashScriptECDSA(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}
		return &externalapi.ScriptPublicKey{Script: script, Version: addressPublicKeyHashECDSAScriptPublicKeyVersion}, err

	case *util.AddressScriptHash:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		script, err := payToScriptHashScript(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}

		return &externalapi.ScriptPublicKey{Script: script, Version: addressScriptHashScriptPublicKeyVersion}, err

	case *util.AddressMultiSig:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		// For multisig addresses, the script is already the full scriptPubKey
		return &externalapi.ScriptPublicKey{Script: addr.ScriptAddress(), Version: addressMultiSigScriptPublicKeyVersion}, nil

	case *util.AddressMultiSigPKH:
		if addr == nil {
			return nil, scriptError(ErrUnsupportedAddress,
				nilAddrErrStr)
		}
		// For P2PKH-style multisig addresses, create a P2PKH-like script with the multisig script hash
		script, err := payToMultiSigPKHScript(addr.ScriptAddress())
		if err != nil {
			return nil, err
		}
		return &externalapi.ScriptPublicKey{Script: script, Version: addressPublicKeyHashScriptPublicKeyVersion}, err
	}

	str := fmt.Sprintf("unable to generate payment script for unsupported "+
		"address type %T", addr)
	return nil, scriptError(ErrUnsupportedAddress, str)
}

// PayToScriptHashScript takes a script and returns an equivalent pay-to-script-hash script
func PayToScriptHashScript(redeemScript []byte) ([]byte, error) {
	redeemScriptHash := util.HashBlake2b(redeemScript)
	script, err := NewScriptBuilder().
		AddOp(OpBlake2b).AddData(redeemScriptHash).
		AddOp(OpEqual).Script()
	if err != nil {
		return nil, err
	}
	return script, nil
}

// PayToScriptHashSignatureScript generates a signature script that fits a pay-to-script-hash script
func PayToScriptHashSignatureScript(redeemScript []byte, signature []byte) ([]byte, error) {
	redeemScriptAsData, err := NewScriptBuilder().AddData(redeemScript).Script()
	if err != nil {
		return nil, err
	}
	signatureScript := make([]byte, len(signature)+len(redeemScriptAsData))
	copy(signatureScript, signature)
	copy(signatureScript[len(signature):], redeemScriptAsData)
	return signatureScript, nil
}

// PayToMultiSigScript creates a direct multisig script (P2PK-style) for m-of-n signatures.
// This creates a script of the form:
// <m> <pub1> <pub2> ... <pubN> <n> OP_CHECKMULTISIG
func PayToMultiSigScript(pubKeys [][]byte, requiredSigs int, ecdsa bool) ([]byte, error) {
	if requiredSigs <= 0 || requiredSigs > len(pubKeys) {
		return nil, errors.Errorf("invalid required signatures: %d for %d public keys", requiredSigs, len(pubKeys))
	}
	if len(pubKeys) == 0 {
		return nil, errors.New("at least one public key is required")
	}
	
	builder := NewScriptBuilder()
	builder.AddInt64(int64(requiredSigs))
	
	for _, pubKey := range pubKeys {
		builder.AddData(pubKey)
	}
	
	builder.AddInt64(int64(len(pubKeys)))
	
	if ecdsa {
		builder.AddOp(OpCheckMultiSigECDSA)
	} else {
		builder.AddOp(OpCheckMultiSig)
	}
	
	return builder.Script()
}

// payToMultiSigPKHScript creates a P2PKH-style script for multisig.
// This creates a script of the form:
// OP_DUP OP_BLAKE2B <multisig-script-hash> OP_EQUALVERIFY OP_CHECKSIG
// However, this is unconventional and may not work for all multisig cases.
func payToMultiSigPKHScript(scriptHash []byte) ([]byte, error) {
	return NewScriptBuilder().
		AddOp(OpDup).
		AddOp(OpBlake2b).
		AddData(scriptHash).
		AddOp(OpEqualVerify).
		AddOp(OpCheckSig).
		Script()
}

// ExtractMultiSigScriptInfo extracts the required signatures and public keys from a multisig script.
// Returns (requiredSigs, pubKeys, ecdsa, error)
func ExtractMultiSigScriptInfo(script []byte) (int, [][]byte, bool, error) {
	pops, err := ParseScript(script)
	if err != nil {
		return 0, nil, false, err
	}
	
	scriptClass := typeOfScript(pops)
	
	// Handle both Schnorr and ECDSA multisig
	if scriptClass == MultiSigTy {
		return extractMultiSigInfo(pops, false)
	} else if scriptClass == MultiSigECDSATy {
		return extractMultiSigInfo(pops, true)
	}
	
	return 0, nil, false, errors.Errorf("script is not a multisig script (class: %s)", scriptClass)
}

// extractMultiSigInfo is the internal function to extract multisig info from parsed opcodes
func extractMultiSigInfo(pops []parsedOpcode, ecdsa bool) (int, [][]byte, bool, error) {
	if len(pops) < 4 {
		return 0, nil, false, errors.New("multisig script too short")
	}
	
	// Extract required signatures (first opcode)
	var requiredSigs int
	if isSmallInt(pops[0].opcode) {
		requiredSigs = int(pops[0].opcode.value - Op1 + 1)
	} else if pops[0].data != nil {
		m, err := makeScriptNum(pops[0].data, 4)
		if err != nil {
			return 0, nil, false, errors.Wrap(err, "invalid required signatures")
		}
		requiredSigs = int(m)
	} else {
		return 0, nil, false, errors.New("invalid required signatures format")
	}
	
	// Extract total public keys (second to last opcode)
	var totalPubKeys int
	if isSmallInt(pops[len(pops)-2].opcode) {
		totalPubKeys = int(pops[len(pops)-2].opcode.value - Op1 + 1)
	} else if pops[len(pops)-2].data != nil {
		n, err := makeScriptNum(pops[len(pops)-2].data, 4)
		if err != nil {
			return 0, nil, false, errors.Wrap(err, "invalid total public keys")
		}
		totalPubKeys = int(n)
	} else {
		return 0, nil, false, errors.New("invalid total public keys format")
	}
	
	// Validate the number of public keys
	// Expected: 1 (requiredSigs) + N (pubkeys) + 1 (totalPubKeys) + 1 (CHECKMULTISIG) = N + 3
	expectedOps := totalPubKeys + 3
	if len(pops) != expectedOps {
		return 0, nil, false, errors.Errorf("expected %d total opcodes (including %d pubkeys), found %d", expectedOps, totalPubKeys, len(pops))
	}
	
	// Extract public keys (all opcodes between first and last two)
	pubKeys := make([][]byte, totalPubKeys)
	for i := 0; i < totalPubKeys; i++ {
		pop := pops[i+1] // +1 to skip the requiredSigs opcode
		if pop.data == nil {
			return 0, nil, false, errors.Errorf("public key %d is not data", i+1)
		}
		pubKeys[i] = pop.data
	}
	
	return requiredSigs, pubKeys, ecdsa, nil
}

// PushedData returns an array of byte slices containing any pushed data found
// in the passed script. This includes OP_0, but not OP_1 - OP_16.
func PushedData(script []byte) ([][]byte, error) {
	pops, err := ParseScript(script)
	if err != nil {
		return nil, err
	}

	var data [][]byte
	for _, pop := range pops {
		if pop.data != nil {
			data = append(data, pop.data)
		} else if pop.opcode.value == Op0 {
			data = append(data, nil)
		}
	}
	return data, nil
}

// ExtractScriptPubKeyAddress returns the type of script and its addresses.
// Note that it only works for 'standard' transaction script types. Any data such
// as public keys which are invalid will return a nil address.
func ExtractScriptPubKeyAddress(scriptPubKey *externalapi.ScriptPublicKey, dagParams *dagconfig.Params) (ScriptClass, util.Address, error) {
	if scriptPubKey.Version > constants.MaxScriptPublicKeyVersion {
		return NonStandardTy, nil, nil
	}
	// No valid address if the script doesn't parse.
	pops, err := ParseScript(scriptPubKey.Script)
	if err != nil {
		return NonStandardTy, nil, err
	}

	scriptClass := typeOfScript(pops)
	switch scriptClass {
	case PubKeyTy:
		// A pay-to-pubkey script is of the form:
		// <pubkey> OP_CHECKSIG
		// Therefore the pubkey is the first item on the stack.
		// If the pubkey is invalid for some reason, return a nil address.
		addr, err := util.NewAddressPublicKey(pops[0].data,
			dagParams.Prefix)
		if err != nil {
			return scriptClass, nil, nil
		}
		return scriptClass, addr, nil

	case PubKeyHashTy:
		// A pay-to-pubkey-hash script is of the form:
		// OP_DUP OP_BLAKE2B <pubkeyhash> OP_EQUALVERIFY OP_CHECKSIG
		// Therefore the pubkey hash is the 3rd item.
		addr, err := util.NewAddressPublicKeyHashFromHash(pops[2].data, dagParams.Prefix)
		if err != nil {
			return scriptClass, nil, nil
		}
		return scriptClass, addr, nil

	case PubKeyHashECDSATy:
		// A pay-to-pubkey-hash ECDSA script is of the form:
		// OP_DUP OP_BLAKE2B <pubkeyhash> OP_EQUALVERIFY OP_CHECKSIGECDSA
		// Therefore the pubkey hash is the 3rd item.
		addr, err := util.NewAddressPublicKeyHashECDSAFromHash(pops[2].data, dagParams.Prefix)
		if err != nil {
			return scriptClass, nil, nil
		}
		return scriptClass, addr, nil

	case PubKeyECDSATy:
		// A pay-to-pubkey script is of the form:
		// <pubkey> OP_CHECKSIGECDSA
		// Therefore the pubkey is the first item on the stack.
		// If the pubkey is invalid for some reason, return a nil address.
		addr, err := util.NewAddressPublicKeyECDSA(pops[0].data,
			dagParams.Prefix)
		if err != nil {
			return scriptClass, nil, nil
		}
		return scriptClass, addr, nil

	case ScriptHashTy:
		// A pay-to-script-hash script is of the form:
		//  OP_BLAKE2B <scripthash> OP_EQUAL
		// Therefore the script hash is the 2nd item on the stack.
		// If the script hash ss invalid for some reason, return a nil address.
		addr, err := util.NewAddressScriptHashFromHash(pops[1].data,
			dagParams.Prefix)
		if err != nil {
			return scriptClass, nil, nil
		}
		return scriptClass, addr, nil

	case MultiSigTy:
		// A direct multisig script doesn't have a single address.
		// Return the script class with nil address.
		return scriptClass, nil, nil

	case MultiSigECDSATy:
		// A direct multisig ECDSA script doesn't have a single address.
		// Return the script class with nil address.
		return scriptClass, nil, nil

	case NonStandardTy:
		// Don't attempt to extract addresses or required signatures for
		// nonstandard transactions.
		return NonStandardTy, nil, nil
	}

	return NonStandardTy, nil, errors.Errorf("Cannot handle script class %s", scriptClass)
}

// AtomicSwapDataPushes houses the data pushes found in atomic swap contracts.
type AtomicSwapDataPushes struct {
	RecipientBlake2b [32]byte
	RefundBlake2b    [32]byte
	SecretHash       [32]byte
	SecretSize       int64
	LockTime         uint64
}

// ExtractAtomicSwapDataPushes returns the data pushes from an atomic swap
// contract. If the script is not an atomic swap contract,
// ExtractAtomicSwapDataPushes returns (nil, nil). Non-nil errors are returned
// for unparsable scripts.
//
// NOTE: Atomic swaps are not considered standard script types by the dcrd
// mempool policy and should be used with P2SH. The atomic swap format is also
// expected to change to use a more secure hash function in the future.
//
// This function is only defined in the txscript package due to API limitations
// which prevent callers using txscript to parse nonstandard scripts.
func ExtractAtomicSwapDataPushes(_ uint16, scriptPubKey []byte) (*AtomicSwapDataPushes, error) {
	pops, err := ParseScript(scriptPubKey)
	if err != nil {
		return nil, err
	}

	if len(pops) != 19 {
		return nil, nil
	}
	isAtomicSwap := pops[0].opcode.value == OpIf &&
		pops[1].opcode.value == OpSize &&
		canonicalPush(pops[2]) &&
		pops[3].opcode.value == OpEqualVerify &&
		pops[4].opcode.value == OpSHA256 &&
		pops[5].opcode.value == OpData32 &&
		pops[6].opcode.value == OpEqualVerify &&
		pops[7].opcode.value == OpDup &&
		pops[8].opcode.value == OpBlake2b &&
		pops[9].opcode.value == OpData32 &&
		pops[10].opcode.value == OpElse &&
		canonicalPush(pops[11]) &&
		pops[12].opcode.value == OpCheckLockTimeVerify &&
		pops[13].opcode.value == OpDup &&
		pops[14].opcode.value == OpBlake2b &&
		pops[15].opcode.value == OpData32 &&
		pops[16].opcode.value == OpEndIf &&
		pops[17].opcode.value == OpEqualVerify &&
		pops[18].opcode.value == OpCheckSig
	if !isAtomicSwap {
		return nil, nil
	}

	pushes := new(AtomicSwapDataPushes)
	copy(pushes.SecretHash[:], pops[5].data)
	copy(pushes.RecipientBlake2b[:], pops[9].data)
	copy(pushes.RefundBlake2b[:], pops[15].data)
	if pops[2].data != nil {
		locktime, err := makeScriptNum(pops[2].data, 8)
		if err != nil {
			return nil, nil
		}
		pushes.SecretSize = int64(locktime)
	} else if op := pops[2].opcode; isSmallInt(op) {
		pushes.SecretSize = int64(asSmallInt(op))
	} else {
		return nil, nil
	}
	if pops[11].data != nil {
		locktime, err := makeScriptNum(pops[11].data, 8)
		if err != nil {
			return nil, nil
		}
		if locktime < 0 {
			return nil, nil
		}
		lockTimeUint64, err := strconv.ParseUint(strconv.FormatInt(int64(locktime), 10), 10, 64)
		if err != nil {
			return nil, nil
		}
		pushes.LockTime = lockTimeUint64
	} else if op := pops[11].opcode; isSmallInt(op) {
		lockTimeUint64, err := strconv.ParseUint(strconv.Itoa(asSmallInt(op)), 10, 64)
		if err != nil {
			return nil, nil
		}
		pushes.LockTime = lockTimeUint64
	} else {
		return nil, nil
	}
	return pushes, nil
}
