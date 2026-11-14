package config

import "testing"

// TestProtocolVersionValidatedAtStartup pins that --protocol-version outside the range the node accepts is rejected
// when the configuration is loaded. Left unchecked, every handshake fails, so the node would run with no peers.
// The test calls the validation LoadConfig applies; LoadConfig itself creates the user's application directory, which
// a test must not do.
func TestProtocolVersionValidatedAtStartup(t *testing.T) {
	tests := []struct {
		version uint32
		valid   bool
	}{
		{version: 0, valid: false},
		{version: MinProtocolVersion - 1, valid: false},
		{version: MinProtocolVersion, valid: true},
		{version: MaxProtocolVersion, valid: true},
		{version: MaxProtocolVersion + 1, valid: false},
		{version: defaultProtocolVersion, valid: true},
	}
	for _, test := range tests {
		err := validateProtocolVersion(test.version)
		if (err == nil) != test.valid {
			t.Errorf("protocol version %d: err = %v, want valid = %t", test.version, err, test.valid)
		}
	}
}
