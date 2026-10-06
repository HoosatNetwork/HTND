package server

import (
	"testing"
	"time"
)

// TestCompoundNeedsUTXORefresh pins that a compound reuses a recent UTXO set rather than fetching it
// for every transaction, and still fetches it when the set is missing, old, stale or fetched with
// another limit.
func TestCompoundNeedsUTXORefresh(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const limit = uint32(10000)

	testCases := []struct {
		name     string
		server   *server
		expected bool
	}{
		{
			name:     "never refreshed",
			server:   &server{},
			expected: true,
		},
		{
			name: "recent refresh with the same limit",
			server: &server{
				startTimeOfLastCompletedRefresh: now.Add(-time.Minute),
				limitOfLastCompletedRefresh:     limit,
			},
			expected: false,
		},
		{
			name: "refresh older than the max age",
			server: &server{
				startTimeOfLastCompletedRefresh: now.Add(-compoundUTXOSetMaxAge - time.Second),
				limitOfLastCompletedRefresh:     limit,
			},
			expected: true,
		},
		{
			name: "recent refresh with another limit",
			server: &server{
				startTimeOfLastCompletedRefresh: now.Add(-time.Minute),
				limitOfLastCompletedRefresh:     0,
			},
			expected: true,
		},
		{
			name: "recent refresh marked stale by a broadcast",
			server: &server{
				startTimeOfLastCompletedRefresh: now.Add(-time.Minute),
				limitOfLastCompletedRefresh:     limit,
				utxoSetIsStale:                  true,
			},
			expected: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			actual := testCase.server.compoundNeedsUTXORefresh(limit, now)
			if actual != testCase.expected {
				t.Fatalf("unexpected result: got %t, want %t", actual, testCase.expected)
			}
		})
	}
}
