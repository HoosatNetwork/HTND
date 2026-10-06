package transactionvalidator

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestIsValidJSONObjectOptimized(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected bool
		errorMsg string
	}{
		{
			name:     "valid simple JSON",
			input:    []byte(`{"name": "test", "value": 123}`),
			expected: true,
		},
		{
			name:     "empty JSON object",
			input:    []byte(`{}`),
			expected: true,
		},
		{
			name:     "invalid JSON",
			input:    []byte(`{"name": "test",}`),
			expected: false,
			errorMsg: "invalid JSON",
		},
		{
			name:     "empty input",
			input:    []byte(``),
			expected: false,
			errorMsg: "empty input data",
		},
		{
			name:     "JSON with base64 image",
			input:    []byte(`{"image": "` + base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0}) + `test_image_data_here_making_it_long_enough_to_trigger_detection_because_we_need_more_than_100_bytes_for_the_optimization_to_kick_in_and_detect_encoded_binary_content_properly"}`),
			expected: false,
			errorMsg: "encoded file or binary data",
		},
		{
			name:     "JSON with suspicious binary signatures",
			input:    []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x7B, 0x22, 0x74, 0x65, 0x73, 0x74, 0x22, 0x3A, 0x31, 0x7D}, // JPEG signature + JSON
			expected: false,
			errorMsg: "suspicious binary file signatures",
		},
		{
			name:     "deeply nested JSON",
			input:    []byte(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":{"k":"too_deep"}}}}}}}}}}}`),
			expected: false,
			errorMsg: "encoded file or binary data",
		},
		{
			name:     "JSON with suspicious key names",
			input:    []byte(`{"image_data": "some_value", "normal": "ok"}`),
			expected: false,
			errorMsg: "encoded file or binary data",
		},
		{
			name: "valid poll creation JSON",
			input: []byte(`{
  "type": "poll_create",
  "v": 1,
  "title": "Transaction Fee Adjustment Vote",
  "description": "Should we increase the platform fee to 1 HTN?",
  "options": [
    "Yes, increase to 1 HTN",
    "No, keep at 0.0001 HTN",
    "Increase to 0.5 HTN (middle ground)"
  ],
  "startDate": 1760355780000,
  "endDate": 1760788800000,
  "votingType": "single",
  "votingMode": "standard",
  "category": "community",
  "minBalance": 0
}`),
			expected: true,
		},
		{
			name: "Valid poll creation JSON with longer text",
			input: []byte(`{
  "type": "poll_create",
  "v": 1,
  "title": "[HTN] Should Tonto stop addressing FUD? ",
  "description": "We have our HTN voting platform and every person who is spreading FUD can instead come here to create polls to see what HTN community thinks about their opinions. ",
  "options": [
    "Whatever, don't really care.",
    "Yes! Tell them to voice their opinions here and stop addressing the FUD.",
    "Just continue addressing the FUD!",
    "Both! Keep addressing the FUD and tell them to voice their opinions here. "
  ],
  "startDate": 1766154300000,
  "endDate": 1772288700000,
  "votingType": "single",
  "votingMode": "standard",
  "category": "community",
  "minBalance": 100
}`),
			expected: false,
			errorMsg: "high entropy",
		},
		{
			name: "valid vote cast JSON",
			input: []byte(`{
  "type": "vote_cast",
  "v": 1,
  "pollId": "49ac22678311c2e990cde0daaf8d82f8662bd4c0aeeade130c7bb52a9440c9bb",
  "votes": [
    0
  ]
}`),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid, err := IsValidJSONObject(tt.input, 0)
			if isValid != tt.expected {
				t.Errorf("%v", err.Error())
				t.Errorf("%s IsValidJSONObject() = %v, expected %v", tt.name, isValid, tt.expected)
			}
			if !tt.expected && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expected && tt.errorMsg != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			}
		})
	}
}

func TestHasHighEntropy(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "low entropy text",
			input:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			expected: false,
		},
		{
			name:     "medium entropy random",
			input:    "zK8pQm2NvF5jHxW9yB6cE1rT4uI7oA0sD3gL6nM8hP5qRx2vY9wZ1eN4mC7bV0oS3dF6gH8jK1lP5qR7sT9uV2wX",
			expected: true,
		},
		{
			name:     "short string",
			input:    "test",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasHighEntropy(tt.input, 0)
			if result != tt.expected {
				t.Errorf("hasHighEntropy() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestIsLikelyBase64(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid base64",
			input:    "SGVsbG8gV29ybGQ=",
			expected: true,
		},
		{
			name:     "invalid base64 length",
			input:    "SGVsbG8=invalid",
			expected: false,
		},
		{
			name:     "not base64 chars",
			input:    "Hello World!",
			expected: false,
		},
		{
			name:     "short string",
			input:    "Hi",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLikelyBase64(tt.input)
			if result != tt.expected {
				t.Errorf("isLikelyBase64() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestContainsSuspiciousBinarySignatures(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected bool
	}{
		{
			name:     "JPEG signature",
			input:    []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46},
			expected: true,
		},
		{
			name:     "PNG signature",
			input:    []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
			expected: true,
		},
		{
			name:     "normal text",
			input:    []byte("Hello World"),
			expected: false,
		},
		{
			name:     "too short",
			input:    []byte{0xFF},
			expected: false,
		},
		{
			name:     "ZIP signature",
			input:    []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsSuspiciousBinarySignatures(tt.input)
			if result != tt.expected {
				t.Errorf("containsSuspiciousBinarySignatures() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestIsLikelyHexString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid hex string",
			input:    "48656c6c6f20576f726c64",
			expected: true,
		},
		{
			name:     "odd length",
			input:    "48656c6c6",
			expected: false,
		},
		{
			name:     "invalid characters",
			input:    "48656c6g",
			expected: false,
		},
		{
			name:     "too short",
			input:    "48656c",
			expected: false,
		},
		{
			name:     "uppercase hex",
			input:    "48656C6C6F20576F726C64",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLikelyHexString(tt.input)
			if result != tt.expected {
				t.Errorf("isLikelyHexString() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestIsSuspiciousKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "image key",
			input:    "profile_image",
			expected: true,
		},
		{
			name:     "file key",
			input:    "uploaded_file",
			expected: true,
		},
		{
			name:     "normal key",
			input:    "username",
			expected: false,
		},
		{
			name:     "base64 key",
			input:    "encoded_base64_data",
			expected: true,
		},
		{
			name:     "case insensitive",
			input:    "IMAGE_DATA",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSuspiciousKey(tt.input)
			if result != tt.expected {
				t.Errorf("isSuspiciousKey() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

// Benchmark tests to verify performance improvements
func BenchmarkIsValidJSONObjectSimple(b *testing.B) {
	testData := []byte(`{"name": "test", "description": "a simple JSON object", "value": 12345, "active": true}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = IsValidJSONObject(testData, 0)
	}
}

func BenchmarkIsValidJSONObjectWithEncodedData(b *testing.B) {
	// Create JSON with potential base64 encoded content
	hexData := hex.EncodeToString([]byte("This is some test data that could be suspicious"))
	testData := []byte(`{"name": "test", "hex_data": "` + hexData + `", "value": 12345}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = IsValidJSONObject(testData, 0)
	}
}

func BenchmarkContainsSuspiciousBinarySignatures(b *testing.B) {
	testData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x01, 0x00, 0x48}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		containsSuspiciousBinarySignatures(testData)
	}
}

// Test real-world poll and vote scenarios
func TestDataTransactionPayloadValidation(t *testing.T) {
	pollJSON := []byte(`{
  "type": "poll_create",
  "v": 1,
  "title": "Transaction Fee Adjustment Vote",
  "description": "Should we increase the platform fee to 1 HTN?",
  "options": [
    "Yes, increase to 1 HTN",
    "No, keep at 0.0001 HTN",
    "Increase to 0.5 HTN (middle ground)"
  ],
  "startDate": 1760355780000,
  "endDate": 1760788800000,
  "votingType": "single",
  "votingMode": "standard",
  "category": "community",
  "minBalance": 0
}`)

	voteJSON := []byte(`{
  "type": "vote_cast",
  "v": 1,
  "pollId": "49ac22678311c2e990cde0daaf8d82f8662bd4c0aeeade130c7bb52a9440c9bb",
  "votes": [
    0
  ]
}`)

	tests := []struct {
		name     string
		payload  []byte
		expected bool
	}{
		{
			name:     "poll creation should pass",
			payload:  pollJSON,
			expected: true,
		},
		{
			name:     "vote cast should pass",
			payload:  voteJSON,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid, err := IsValidJSONObject(tt.payload, 0)
			if isValid != tt.expected {
				t.Errorf("IsValidJSONObject() = %v, expected %v. Error: %v", isValid, tt.expected, err)
			}
			if !isValid && tt.expected {
				t.Errorf("Expected valid JSON but got error: %v", err)
			}
		})
	}
}
