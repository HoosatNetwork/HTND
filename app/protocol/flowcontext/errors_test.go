package flowcontext

import (
	"errors"
	"testing"
)

func TestIsWireFormatError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "proto wire-format error",
			err:      errors.New("proto: cannot parse invalid wire-format data"),
			expected: true,
		},
		{
			name:     "invalid wire-format error",
			err:      errors.New("invalid wire-format data received"),
			expected: true,
		},
		{
			name:     "proto with wire-format",
			err:      errors.New("proto: error in wire-format parsing"),
			expected: true,
		},
		{
			name:     "protobuf parse error",
			err:      errors.New("protobuf parse failed"),
			expected: true,
		},
		{
			name:     "regular error",
			err:      errors.New("some other error"),
			expected: false,
		},
		{
			name:     "database error",
			err:      errors.New("database not found"),
			expected: false,
		},
		{
			name:     "network error",
			err:      errors.New("connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isWireFormatError(tt.err)
			if result != tt.expected {
				t.Errorf("isWireFormatError(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}
