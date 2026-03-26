package rpcstats

import "testing"

func TestExtractIP(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"127.0.0.1:16110":          "127.0.0.1",
		"[::1]:16110":              "::1",
		"[::ffff:127.0.0.1]:16110": "127.0.0.1",
		"127.0.0.1":                "127.0.0.1",
		"::1":                      "::1",
		"":                         "unknown",
		"localhost":                "localhost",
	}

	for input, expected := range tests {
		input, expected := input, expected
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			actual := extractIP(input)
			if actual != expected {
				t.Fatalf("extractIP(%q) = %q, expected %q", input, actual, expected)
			}
		})
	}
}

func TestRecordRequestGroupsByNormalizedIP(t *testing.T) {
	t.Parallel()

	stats := NewStats()
	stats.RecordRequest("127.0.0.1:10001", "getInfo")
	stats.RecordRequest("[::ffff:127.0.0.1]:10002", "getBlockCount")
	stats.RecordRequest("[::1]:10003", "getInfo")

	if got := stats.requestsByIP["127.0.0.1"]; got != 2 {
		t.Fatalf("expected 2 requests for 127.0.0.1, got %d", got)
	}
	if got := stats.requestsByIP["::1"]; got != 1 {
		t.Fatalf("expected 1 request for ::1, got %d", got)
	}
	if got := stats.totalRequests; got != 3 {
		t.Fatalf("expected 3 total requests, got %d", got)
	}
	if got := stats.requestsByMethod["getInfo"]; got != 2 {
		t.Fatalf("expected 2 requests for getInfo, got %d", got)
	}
	if got := stats.requestsByMethod["getBlockCount"]; got != 1 {
		t.Fatalf("expected 1 request for getBlockCount, got %d", got)
	}

	topMethods := stats.getTopMethods(10)
	if len(topMethods) != 2 {
		t.Fatalf("expected 2 top methods, got %d", len(topMethods))
	}
	if topMethods[0].Method != "getInfo" || topMethods[0].Count != 2 {
		t.Fatalf("expected top method getInfo with count 2, got %s with %d", topMethods[0].Method, topMethods[0].Count)
	}
	if topMethods[1].Method != "getBlockCount" || topMethods[1].Count != 1 {
		t.Fatalf("expected second method getBlockCount with count 1, got %s with %d", topMethods[1].Method, topMethods[1].Count)
	}
}
