package transactionvalidator

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/subnetworks"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionhelper"
	"github.com/pkg/errors"
)

// ValidateTransactionInIsolation validates the parts of the transaction that can be validated context-free
func (v *transactionValidator) ValidateTransactionInIsolation(tx *externalapi.DomainTransaction, povDAAScore uint64) error {
	err := v.checkTransactionInputCount(tx)
	if err != nil {
		return err
	}
	err = v.checkTransactionAmountRanges(tx)
	if err != nil {
		return err
	}
	err = v.checkDuplicateTransactionInputs(tx)
	if err != nil {
		return err
	}
	err = v.checkCoinbaseInIsolation(tx)
	if err != nil {
		return err
	}
	err = v.checkGasInBuiltInOrNativeTransactions(tx)
	if err != nil {
		return err
	}
	err = v.checkSubnetworkRegistryTransaction(tx)
	if err != nil {
		return err
	}

	err = v.checkDataTransactionPayload(tx, povDAAScore)
	if err != nil {
		return err
	}

	err = v.checkNativeTransactionPayload(tx)
	if err != nil {
		return err
	}

	err = v.checkTransactionSubnetwork(tx, nil, povDAAScore)
	if err != nil {
		return err
	}

	if tx.Version > constants.MaxTransactionVersion {
		return errors.Wrapf(ruleerrors.ErrTransactionVersionIsUnknown, "validation failed: unknown transaction version. ")
	}

	return nil
}

func (v *transactionValidator) checkTransactionInputCount(tx *externalapi.DomainTransaction) error {
	// A non-coinbase transaction must have at least one input.
	if !transactionhelper.IsCoinBase(tx) && len(tx.Inputs) == 0 {
		return errors.Wrapf(ruleerrors.ErrNoTxInputs, "transaction has no inputs")
	}
	return nil
}

func (v *transactionValidator) checkTransactionAmountRanges(tx *externalapi.DomainTransaction) error {
	// Ensure the transaction amounts are in range. Each transaction
	// output must not be negative or more than the max allowed per
	// transaction. Also, the total of all outputs must abide by the same
	// restrictions. All amounts in a transaction are in a unit value known
	// as a sompi. One hoosat is a quantity of sompi as defined by the
	// sompiPerHoosat constant.
	var totalSompi uint64
	for _, txOut := range tx.Outputs {
		sompi := txOut.Value
		if sompi == 0 {
			return errors.Wrap(ruleerrors.ErrTxOutValueZero, "zero value outputs are forbidden")
		}

		if sompi > constants.MaxSompi {
			return errors.Wrapf(ruleerrors.ErrBadTxOutValue, "transaction output value of %d is "+
				"higher than max allowed value of %d", sompi, constants.MaxSompi)
		}

		// Binary arithmetic guarantees that any overflow is detected and reported.
		// This is impossible for Hoosat, but perhaps possible if an alt increases
		// the total money supply.
		newTotalSompi := totalSompi + sompi
		if newTotalSompi < totalSompi {
			return errors.Wrapf(ruleerrors.ErrBadTxOutValue, "total value of all transaction "+
				"outputs exceeds max allowed value of %d",
				constants.MaxSompi)
		}
		totalSompi = newTotalSompi
		if totalSompi > constants.MaxSompi {
			return errors.Wrapf(ruleerrors.ErrBadTxOutValue, "total value of all transaction "+
				"outputs is %d which is higher than max "+
				"allowed value of %d", totalSompi,
				constants.MaxSompi)
		}
	}

	return nil
}

func (v *transactionValidator) checkDuplicateTransactionInputs(tx *externalapi.DomainTransaction) error {
	existingTxOut := make(map[externalapi.DomainOutpoint]struct{})
	for _, txIn := range tx.Inputs {
		if _, exists := existingTxOut[txIn.PreviousOutpoint]; exists {
			return errors.Wrapf(ruleerrors.ErrDuplicateTxInputs, "transaction "+
				"contains duplicate inputs")
		}
		existingTxOut[txIn.PreviousOutpoint] = struct{}{}
	}
	return nil
}

func (v *transactionValidator) checkCoinbaseInIsolation(tx *externalapi.DomainTransaction) error {
	if !transactionhelper.IsCoinBase(tx) {
		return nil
	}

	// Coinbase payload length must not exceed the max length.
	payloadLen := len(tx.Payload)
	if uint64(payloadLen) > v.maxCoinbasePayloadLength {
		return errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen, "coinbase transaction payload length "+
			"of %d is out of range (max: %d)",
			payloadLen, v.maxCoinbasePayloadLength)
	}

	if len(tx.Inputs) != 0 {
		return errors.Wrap(ruleerrors.ErrCoinbaseWithInputs, "coinbase has inputs")
	}

	outputsLimit := v.MergeSetSizeLimit + 2
	// Make the outputsLimits twice the size because of developer fee in outputs with coinbase outputs.
	outputsLimit *= 2
	if uint64(len(tx.Outputs)) > outputsLimit {
		return errors.Wrapf(ruleerrors.ErrCoinbaseTooManyOutputs, "coinbase has too many outputs: got %d where the limit is %d", len(tx.Outputs), outputsLimit)
	}

	for i, output := range tx.Outputs {
		if len(output.ScriptPublicKey.Script) > int(v.coinbasePayloadScriptPublicKeyMaxLength) {
			return errors.Wrapf(ruleerrors.ErrCoinbaseTooLongScriptPublicKey, "coinbase output %d has a too long script public key", i)

		}
	}

	return nil
}

func (v *transactionValidator) checkGasInBuiltInOrNativeTransactions(tx *externalapi.DomainTransaction) error {
	// Transactions in native, registry and coinbase subnetworks must have Gas = 0
	if subnetworks.IsBuiltInOrNative(tx.SubnetworkID) && tx.Gas > 0 {
		return errors.Wrapf(ruleerrors.ErrInvalidGas, "transaction in the native or "+
			"registry subnetworks has gas > 0 ")
	}
	return nil
}

func (v *transactionValidator) checkSubnetworkRegistryTransaction(tx *externalapi.DomainTransaction) error {
	if tx.SubnetworkID != subnetworks.SubnetworkIDRegistry {
		return nil
	}

	if len(tx.Payload) != 8 {
		return errors.Wrapf(ruleerrors.ErrSubnetworkRegistry, "validation failed: subnetwork registry "+
			"tx has an invalid payload")
	}
	return nil
}

func (v *transactionValidator) checkNativeTransactionPayload(tx *externalapi.DomainTransaction) error {
	if tx.SubnetworkID == subnetworks.SubnetworkIDNative && len(tx.Payload) > 0 {
		return errors.Wrapf(ruleerrors.ErrInvalidPayload, "transaction in the native subnetwork "+
			"includes a payload")
	}
	return nil
}

func IsValidJSONObject(data []byte, DAAScore uint64) (bool, error) {
	if len(data) == 0 {
		return false, fmt.Errorf("empty input data")
	}

	// Quick pre-validation: check for suspicious binary signatures in the raw data
	if containsSuspiciousBinarySignatures(data) {
		return false, fmt.Errorf("contains suspicious binary file signatures")
	}

	// Parse JSON with streaming decoder for better performance on large inputs
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Preserve number precision and avoid float64 conversion

	var obj map[string]any
	err := decoder.Decode(&obj)
	if err != nil {
		return false, fmt.Errorf("invalid JSON: %v", err)
	}

	if obj == nil {
		return false, fmt.Errorf("input is not a JSON object")
	}

	// Optimized binary/file detection with early termination
	err = hasEncodedFileContent(obj, 0, DAAScore)
	if err != nil {
		return false, fmt.Errorf("contains encoded file or binary data %v", err)
	}

	return true, nil
}

// containsSuspiciousBinarySignatures checks for common file signatures in raw data
func containsSuspiciousBinarySignatures(data []byte) bool {
	if len(data) < 4 {
		return false
	}

	// Common file signatures to detect
	signatures := [][]byte{
		{0xFF, 0xD8, 0xFF},       // JPEG
		{0x89, 0x50, 0x4E, 0x47}, // PNG
		{0x47, 0x49, 0x46, 0x38}, // GIF
		{0x42, 0x4D},             // BMP
		{0x50, 0x4B, 0x03, 0x04}, // ZIP/JAR/APK
		{0x50, 0x4B, 0x05, 0x06}, // ZIP (empty)
		{0x50, 0x4B, 0x07, 0x08}, // ZIP (spanned)
		{0x52, 0x61, 0x72, 0x21}, // RAR
		{0x7F, 0x45, 0x4C, 0x46}, // ELF executable
		{0x4D, 0x5A},             // PE executable
		{0xCA, 0xFE, 0xBA, 0xBE}, // Mach-O (32-bit)
		{0xFE, 0xED, 0xFA, 0xCE}, // Mach-O (32-bit reverse)
		{0xFE, 0xED, 0xFA, 0xCF}, // Mach-O (64-bit reverse)
		{0xCF, 0xFA, 0xED, 0xFE}, // Mach-O (64-bit)
		{0x25, 0x50, 0x44, 0x46}, // PDF
		{0xD0, 0xCF, 0x11, 0xE0}, // MS Office
		{0x4F, 0x67, 0x67, 0x53}, // Ogg
		{0x49, 0x44, 0x33},       // MP3
		{0xFF, 0xFB},             // MP3
		{0x66, 0x74, 0x79, 0x70}, // MP4/MOV
		{0x00, 0x00, 0x01, 0x00}, // ICO
		{0x52, 0x49, 0x46, 0x46}, // RIFF (WAV/AVI)
	}

	for _, sig := range signatures {
		if len(data) >= len(sig) && bytes.HasPrefix(data, sig) {
			return true
		}
	}
	return false
}

// hasEncodedFileContent recursively checks JSON object for encoded binary content with depth limit
func hasEncodedFileContent(data any, depth int, DAAScore uint64) error {
	// Prevent deep recursion that could cause stack overflow
	const maxDepth = 10
	if depth > maxDepth {
		return errors.New("suspicious deeply nested structure") // Suspicious deeply nested structure
	}

	switch v := data.(type) {
	case map[string]any:
		// Limit the number of keys we check for performance
		const maxKeys = 100
		keyCount := 0
		for key, val := range v {
			keyCount++
			if keyCount > maxKeys {
				return errors.New("too many keys in object") // Too many keys, suspicious
			}

			// Check for suspicious key names
			if isSuspiciousKey(key) {
				return errors.New("suspicious key name found")
			}

			if err := hasEncodedFileContent(val, depth+1, DAAScore); err != nil {
				return err
			}
		}
	case []any:
		// Limit array size for performance
		const maxArraySize = 100
		if len(v) > maxArraySize {
			return errors.New("suspicious large array")
		}

		for _, val := range v {
			if err := hasEncodedFileContent(val, depth+1, DAAScore); err != nil {
				return err
			}
		}
	case string:
		return isEncodedBinaryString(v, DAAScore)
	case []byte:
		return errors.New("direct byte slices are binary")
	}
	return nil
}

// isSuspiciousKey checks for key names that commonly indicate file content
func isSuspiciousKey(key string) bool {
	suspiciousKeys := []string{
		"image", "img", "photo", "picture", "file", "attachment", "binary",
		"data", "content", "payload", "buffer", "blob", "executable", "exe",
		"zip", "archive", "compressed", "encoded", "base64", "hex",
	}

	lowerKey := strings.ToLower(key)
	for _, suspicious := range suspiciousKeys {
		if strings.Contains(lowerKey, suspicious) {
			return true
		}
	}
	return false
}

// isEncodedBinaryString optimized check for encoded binary content
func isEncodedBinaryString(s string, DAAScore uint64) error {
	// Quick length checks for performance
	if len(s) == 0 {
		return nil
	}

	// Strings over a certain size are more likely to be encoded files
	const suspiciousLength = 64
	if len(s) > suspiciousLength {
		// Check entropy - high entropy suggests encoded binary
		if hasHighEntropy(s, DAAScore) {
			return errors.New("high entropy string detected")
		}
	}

	// Fast base64 detection
	if isLikelyBase64(s) {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err == nil && len(decoded) > suspiciousLength { // Only check larger decoded content
			if hasFileSignature(decoded) || isLikelyImage(decoded) {
				return errors.New("encoded file or binary data detected")
			}
		}

		// Try URL encoding
		decoded, err = base64.URLEncoding.DecodeString(s)
		if err == nil && len(decoded) > suspiciousLength {
			if hasFileSignature(decoded) || isLikelyImage(decoded) {
				return errors.New("encoded file or binary data detected")
			}
		}
	}

	// Optimized hex string check
	if isLikelyHexString(s) && len(s) > suspiciousLength { // Only check longer hex strings
		decoded, err := hex.DecodeString(strings.ToLower(s))
		if err == nil {
			if hasFileSignature(decoded) || isLikelyImage(decoded) {
				return errors.New("encoded file or binary data detected")
			}
		}
	}

	return nil
}

// hasHighEntropy checks if string has high entropy (indicating encoded data)
func hasHighEntropy(s string, DAAScore uint64) bool {
	if len(s) < 16 {
		return false
	}

	// Count character frequency
	freq := make(map[rune]int)
	for _, r := range s {
		freq[r]++
	}

	// Calculate Shannon entropy
	entropy := 0.0
	length := float64(len(s))
	for _, count := range freq {
		p := float64(count) / length
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}

	// High entropy threshold (close to random) - adjusted for more realistic detection
	// TODO: update current DAA Score when doing release.
	if DAAScore >= 110_996_218+12_960_000 {
		return entropy > 4.5
	} else {
		return entropy > 2
	}
}

// isLikelyBase64 fast check for base64 patterns
func isLikelyBase64(s string) bool {
	if len(s) < 4 || len(s)%4 != 0 {
		return false
	}

	// Check for base64 character set and padding
	validChars := 0
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
			validChars++
		}
	}

	// Must be mostly valid base64 chars
	return float64(validChars)/float64(len(s)) > 0.95
}

// hasFileSignature checks if data starts with known file signatures
func hasFileSignature(data []byte) bool {
	return containsSuspiciousBinarySignatures(data)
}

// isLikelyImage optimized image detection
func isLikelyImage(data []byte) bool {
	if len(data) < 10 {
		return false
	}

	// Quick signature checks before expensive image.Decode
	imageSignatures := [][]byte{
		{0xFF, 0xD8, 0xFF},       // JPEG
		{0x89, 0x50, 0x4E, 0x47}, // PNG
		{0x47, 0x49, 0x46, 0x38}, // GIF
		{0x42, 0x4D},             // BMP
	}

	for _, sig := range imageSignatures {
		if bytes.HasPrefix(data, sig) {
			return true
		}
	}

	// Only do expensive decode for potential images
	_, _, err := image.Decode(bytes.NewReader(data))
	return err == nil
}

// isLikelyHexString optimized hex string detection
func isLikelyHexString(s string) bool {
	if len(s) < 8 || len(s)%2 != 0 {
		return false
	}

	// Quick character set check
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func (v *transactionValidator) checkDataTransactionPayload(tx *externalapi.DomainTransaction, DAAScore uint64) error {
	if tx.SubnetworkID != subnetworks.SubnetworkIDData || len(tx.Payload) <= 0 {
		return nil
	}

	if len(tx.Payload) > 10_000 {
		return errors.Wrapf(ruleerrors.ErrTooLargePayload, "data subnetwork transaction payload is too large!")
	}

	if isValid, err := IsValidJSONObject(tx.Payload, DAAScore); !isValid {
		return errors.Wrapf(ruleerrors.ErrInvalidPayload, "data subnetwork transaction payload is not valid JSON: %v", err)
	}

	return nil
}

func (v *transactionValidator) checkTransactionSubnetwork(tx *externalapi.DomainTransaction,
	localNodeSubnetworkID *externalapi.DomainSubnetworkID, DAAScore uint64) error {
	if !v.enableNonNativeSubnetworks &&
		tx.SubnetworkID != subnetworks.SubnetworkIDNative &&
		tx.SubnetworkID != subnetworks.SubnetworkIDCoinbase &&
		tx.SubnetworkID != subnetworks.SubnetworkIDData {
		return errors.Wrapf(ruleerrors.ErrSubnetworksDisabled, "transaction has non native or coinbase "+
			"subnetwork ID")
	}
	if DAAScore <= (89_872_005+2_592_000) && tx.SubnetworkID == subnetworks.SubnetworkIDData {
		return errors.Wrapf(ruleerrors.ErrSubnetworksDisabled, "transaction has non native or coinbase "+
			"subnetwork ID")
	}

	// If we are a partial node, only transactions on built in subnetworks
	// or our own subnetwork may have a payload
	isLocalNodeFull := localNodeSubnetworkID == nil
	shouldTxBeFull := subnetworks.IsBuiltIn(tx.SubnetworkID) || tx.SubnetworkID.Equal(localNodeSubnetworkID)
	if !isLocalNodeFull && !shouldTxBeFull && len(tx.Payload) > 0 {
		return errors.Wrapf(ruleerrors.ErrInvalidPayload,
			"transaction that was expected to be partial has a payload "+
				"with length > 0")
	}
	return nil
}
