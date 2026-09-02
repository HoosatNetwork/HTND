package pruningstore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func testHash(t *testing.T, hexString string) *externalapi.DomainHash {
	t.Helper()
	hash, err := externalapi.NewDomainHashFromString(hexString)
	if err != nil {
		t.Fatalf("could not parse %s: %s", hexString, err)
	}
	return hash
}

// The three real hashes from the production incident this marker exists to record: the served
// bucket disagreed with the header while the per-block multiset matched it.
const (
	incidentPruningPoint = "a5390732d49c545c1435d43b6a3d529e3bf365fb6800f8ca003acc6e8bc33121"
	incidentHeader       = "5164183495ca18a09f42a8e49dcee3487d3526929a60f7319c7c567a68c25830"
	incidentBucket       = "c7e4188a5931fb0048c8bcc6bfca2664bc38c9d65b8b4d5076d5c99c710db9b0"
	incidentDiffChain    = "4dd124b0e2f9ba2b96229e416be4d2dd23e403b849ebd99ae70000000000000a"
)

func TestPruningPointUTXOSetVerificationRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   *model.PruningPointUTXOSetVerification
	}{
		{
			name: "unverified with every hash present",
			in: &model.PruningPointUTXOSetVerification{
				PruningPoint:      testHash(t, incidentPruningPoint),
				HeaderCommitment:  testHash(t, incidentHeader),
				BucketMultiset:    testHash(t, incidentBucket),
				PerBlockMultiset:  testHash(t, incidentHeader),
				DiffChainMultiset: testHash(t, incidentDiffChain),
				Status:            model.PruningPointUTXOSetUnverified,
				EntryCount:        14076815,
				CheckedAtDAAScore: 221433570,
			},
		},
		{
			name: "verified with the optional hashes absent",
			in: &model.PruningPointUTXOSetVerification{
				PruningPoint:     testHash(t, incidentPruningPoint),
				HeaderCommitment: testHash(t, incidentHeader),
				BucketMultiset:   testHash(t, incidentHeader),
				Status:           model.PruningPointUTXOSetVerified,
				EntryCount:       0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serialized, err := serializePruningPointUTXOSetVerification(test.in)
			if err != nil {
				t.Fatalf("serialize failed: %s", err)
			}
			if len(serialized) != ppuvSerializedLen {
				t.Fatalf("serialized length is %d, want %d", len(serialized), ppuvSerializedLen)
			}

			out, err := deserializePruningPointUTXOSetVerification(serialized)
			if err != nil {
				t.Fatalf("deserialize failed: %s", err)
			}

			if !out.PruningPoint.Equal(test.in.PruningPoint) {
				t.Errorf("pruning point: got %s, want %s", out.PruningPoint, test.in.PruningPoint)
			}
			if !out.HeaderCommitment.Equal(test.in.HeaderCommitment) {
				t.Errorf("header commitment: got %s, want %s", out.HeaderCommitment, test.in.HeaderCommitment)
			}
			if !out.BucketMultiset.Equal(test.in.BucketMultiset) {
				t.Errorf("bucket multiset: got %s, want %s", out.BucketMultiset, test.in.BucketMultiset)
			}
			if out.Status != test.in.Status {
				t.Errorf("status: got %s, want %s", out.Status, test.in.Status)
			}
			if out.EntryCount != test.in.EntryCount {
				t.Errorf("entry count: got %d, want %d", out.EntryCount, test.in.EntryCount)
			}
			if out.CheckedAtDAAScore != test.in.CheckedAtDAAScore {
				t.Errorf("checkedAtDAAScore: got %d, want %d", out.CheckedAtDAAScore, test.in.CheckedAtDAAScore)
			}

			// An absent optional hash must come back absent, not as a zero hash - the status line
			// prints "n/a" for nil and a real-looking hash for zeroes, and those must not be confused.
			assertOptionalHash(t, "perBlock", out.PerBlockMultiset, test.in.PerBlockMultiset)
			assertOptionalHash(t, "diffChain", out.DiffChainMultiset, test.in.DiffChainMultiset)
		})
	}
}

func assertOptionalHash(t *testing.T, name string, got, want *externalapi.DomainHash) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s: got %s, want nil", name, got)
		}
		return
	}
	if got == nil {
		t.Errorf("%s: got nil, want %s", name, want)
		return
	}
	if !got.Equal(want) {
		t.Errorf("%s: got %s, want %s", name, got, want)
	}
}

// A short read must be rejected rather than silently decoded into a plausible-looking verdict.
func TestPruningPointUTXOSetVerificationRejectsMalformed(t *testing.T) {
	valid, err := serializePruningPointUTXOSetVerification(&model.PruningPointUTXOSetVerification{
		PruningPoint:     testHash(t, incidentPruningPoint),
		HeaderCommitment: testHash(t, incidentHeader),
		BucketMultiset:   testHash(t, incidentBucket),
		Status:           model.PruningPointUTXOSetUnverified,
	})
	if err != nil {
		t.Fatalf("serialize failed: %s", err)
	}

	if _, err := deserializePruningPointUTXOSetVerification(valid[:len(valid)-1]); err == nil {
		t.Errorf("expected a truncated record to be rejected, got nil error")
	}

	wrongVersion := make([]byte, len(valid))
	copy(wrongVersion, valid)
	wrongVersion[ppuvOffsetVersion] = pruningPointUTXOSetVerificationVersion + 1
	if _, err := deserializePruningPointUTXOSetVerification(wrongVersion); err == nil {
		t.Errorf("expected an unknown format version to be rejected, got nil error")
	}
}

// The mandatory hashes are mandatory: a caller that forgets one must get an error rather than
// persist a marker that reads as a verdict about nothing.
func TestPruningPointUTXOSetVerificationRequiresMandatoryHashes(t *testing.T) {
	if _, err := serializePruningPointUTXOSetVerification(nil); err == nil {
		t.Errorf("expected serializing nil to fail")
	}
	if _, err := serializePruningPointUTXOSetVerification(&model.PruningPointUTXOSetVerification{
		PruningPoint:     testHash(t, incidentPruningPoint),
		HeaderCommitment: testHash(t, incidentHeader),
		// BucketMultiset deliberately omitted.
	}); err == nil {
		t.Errorf("expected serializing without a bucket multiset to fail")
	}
}

// The status rendering is what an operator greps for, so pin it.
func TestPruningPointUTXOSetStatusString(t *testing.T) {
	tests := map[model.PruningPointUTXOSetStatus]string{
		model.PruningPointUTXOSetVerified:   "verified",
		model.PruningPointUTXOSetUnverified: "unverified",
		model.PruningPointUTXOSetUnknown:    "unknown",
		model.PruningPointUTXOSetStatus(99): "unknown",
	}
	for status, want := range tests {
		if got := status.String(); got != want {
			t.Errorf("status %d: got %q, want %q", byte(status), got, want)
		}
	}
}
