package serialization

import (
	"bytes"
	"testing"
)

func TestWriteElementNegativeInt64(t *testing.T) {
	var buf bytes.Buffer
	err := WriteElement(&buf, int64(-1))
	if err != nil {
		t.Fatalf("WriteElement(int64(-1)) returned error: %s", err)
	}
	expected := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Fatalf("WriteElement(int64(-1)) wrote %x, want %x", buf.Bytes(), expected)
	}
}
