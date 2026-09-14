package prefix

import "testing"

// TestDeserialize pins that only a single byte holding a known prefix deserializes. An empty value - a corrupted
// or truncated prefix record - used to be indexed without a length check, which panicked at startup instead of
// reporting an invalid prefix.
func TestDeserialize(t *testing.T) {
	tests := []struct {
		name    string
		bytes   []byte
		wantErr bool
	}{
		{name: "empty", bytes: []byte{}, wantErr: true},
		{name: "nil", bytes: nil, wantErr: true},
		{name: "too long", bytes: []byte{0, 1}, wantErr: true},
		{name: "unknown prefix", bytes: []byte{2}, wantErr: true},
		{name: "prefix zero", bytes: []byte{0}, wantErr: false},
		{name: "prefix one", bytes: []byte{1}, wantErr: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix, err := Deserialize(test.bytes)
			if (err != nil) != test.wantErr {
				t.Fatalf("Deserialize(%x) error = %v, want error %t", test.bytes, err, test.wantErr)
			}
			if err == nil && prefix.Serialize()[0] != test.bytes[0] {
				t.Fatalf("Deserialize(%x) round-tripped to %x", test.bytes, prefix.Serialize())
			}
		})
	}
}
