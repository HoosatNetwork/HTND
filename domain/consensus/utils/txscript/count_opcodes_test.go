package txscript

import (
	"bytes"
	"testing"
)

// TestCountScriptOpcodesMatchesParse pins that countScriptOpcodes predicts exactly how many opcodes
// parseScriptTemplate returns, for well-formed and malformed scripts alike, so the parse allocates once
// and never grows its slice. It used to reserve one entry per script byte - ~118 KB per ML-DSA-44
// signature script, which is two or three opcodes.
func TestCountScriptOpcodesMatchesParse(t *testing.T) {
	// OP_PUSHDATA2 of an ML-DSA-44-sized (2,420-byte) signature, then one opcode. Built by hand: the
	// script builder refuses pushes over MaxScriptElementSize, which ML-DSA is exempted from only in
	// consensus.
	mldsaSigScript := append([]byte{OpPushData2, 0x74, 0x09}, bytes.Repeat([]byte{0xab}, 2420)...)
	mldsaSigScript = append(mldsaSigScript, OpTrue)

	scripts := map[string][]byte{
		"empty":                {},
		"one-byte opcodes":     {OpTrue, OpDup, OpDrop, OpTrue},
		"ml-dsa sized push":    mldsaSigScript,
		"OP_DATA_2":            {OpData2, 1, 2, OpTrue},
		"PUSHDATA1":            {OpPushData1, 3, 1, 2, 3, OpTrue},
		"PUSHDATA4":            {OpPushData4, 2, 0, 0, 0, 7, 8},
		"truncated OP_DATA":    {OpTrue, OpData5, 1, 2},
		"truncated length":     {OpTrue, OpPushData2, 1},
		"length past the end":  {OpTrue, OpPushData1, 9, 1, 2},
		"push then truncation": {OpData1, 1, OpPushData4, 0xff, 0xff, 0xff, 0x7f},
	}
	for name, script := range scripts {
		parsed, _ := parseScriptTemplate(script, &opcodeArray)
		count := countScriptOpcodes(script, &opcodeArray)
		if count < len(parsed) {
			t.Errorf("%s: counted %d opcodes but parsing produced %d", name, count, len(parsed))
		}
		if cap(parsed) != count {
			t.Errorf("%s: parse reserved %d entries for %d counted opcodes", name, cap(parsed), count)
		}
	}
	if count := countScriptOpcodes(mldsaSigScript, &opcodeArray); count != 2 {
		t.Errorf("ML-DSA-sized signature script counted as %d opcodes, want 2", count)
	}
}
