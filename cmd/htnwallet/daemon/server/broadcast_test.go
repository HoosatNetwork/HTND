package server

import (
	"errors"
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func TestShouldReleaseUsedOutpointsOnBroadcastError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "rejected transaction",
			err:      errors.New("rpc error: code = Unknown desc = error submitting transaction: Rejected transaction abc"),
			expected: true,
		},
		{
			name:     "already spent",
			err:      errors.New("error submitting transaction: Rejected transaction abc output (def: 1) already spent by transaction ghi"),
			expected: true,
		},
		{
			name:     "compound rate limit",
			err:      errors.New("rpc error: code = Unknown desc = error submitting transaction: Compound transaction rate limit exceeded"),
			expected: true,
		},
		{
			name:     "transient transport failure",
			err:      errors.New("context deadline exceeded"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			actual := shouldReleaseUsedOutpointsOnBroadcastError(testCase.err)
			if actual != testCase.expected {
				t.Fatalf("unexpected result: got %t, want %t", actual, testCase.expected)
			}
		})
	}
}

func TestReleaseUsedOutpoints(t *testing.T) {
	t.Parallel()

	firstOutpoint := externalapi.DomainOutpoint{Index: 1}
	secondOutpoint := externalapi.DomainOutpoint{Index: 2}
	unrelatedOutpoint := externalapi.DomainOutpoint{Index: 3}

	serverInstance := &server{
		usedOutpoints: map[externalapi.DomainOutpoint]time.Time{
			firstOutpoint:     time.Now(),
			secondOutpoint:    time.Now(),
			unrelatedOutpoint: time.Now(),
		},
	}

	tx := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: firstOutpoint},
			{PreviousOutpoint: secondOutpoint},
		},
	}

	serverInstance.releaseUsedOutpoints(tx)

	if _, ok := serverInstance.usedOutpoints[firstOutpoint]; ok {
		t.Fatalf("first outpoint was not released")
	}
	if _, ok := serverInstance.usedOutpoints[secondOutpoint]; ok {
		t.Fatalf("second outpoint was not released")
	}
	if _, ok := serverInstance.usedOutpoints[unrelatedOutpoint]; !ok {
		t.Fatalf("unrelated outpoint should remain reserved")
	}
}
