package handshake

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/pkg/errors"
)

func TestExtractUserAgentVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userAgent string
		want      string
		wantOK    bool
	}{
		{
			name:      "plain htnd agent",
			userAgent: "/htnd:1.2.3/",
			want:      "1.2.3",
			wantOK:    true,
		},
		{
			name:      "htnd agent with comments",
			userAgent: "/htnd:1.2.3(arm64; pruned)/other:9.9.9/",
			want:      "1.2.3",
			wantOK:    true,
		},
		{
			name:      "missing htnd agent",
			userAgent: "/other:1.2.3/",
			want:      "",
			wantOK:    false,
		},
		{
			name:      "empty htnd version",
			userAgent: "/htnd:/",
			want:      "",
			wantOK:    false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, ok := extractUserAgentVersion(test.userAgent, userAgentName)
			if ok != test.wantOK {
				t.Fatalf("expected ok=%t, got %t", test.wantOK, ok)
			}
			if got != test.want {
				t.Fatalf("expected version %q, got %q", test.want, got)
			}
		})
	}
}

func TestValidatePeerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		force     bool
		userAgent string
		wantErr   string
	}{
		{
			name:      "disabled ignores mismatches",
			force:     false,
			userAgent: "/htnd:0.0.1/",
		},
		{
			name:      "same version allowed",
			force:     true,
			userAgent: "/htnd:" + userAgentVersion + "/htnd:" + userAgentVersion + "(comment)/",
		},
		{
			name:      "different version rejected",
			force:     true,
			userAgent: "/htnd:0.0.1/",
			wantErr:   "peer version 0.0.1 does not match local version " + userAgentVersion,
		},
		{
			name:      "missing htnd version rejected",
			force:     true,
			userAgent: "/other:1.2.3/",
			wantErr:   "peer is not advertising an htnd version",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validatePeerVersion(test.force, test.userAgent)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error %q, got nil", test.wantErr)
			}

			var protocolErr protocolerrors.ProtocolError
			if !errors.As(err, &protocolErr) {
				t.Fatalf("expected ProtocolError, got %T", err)
			}
			if protocolErr.ShouldBan {
				t.Fatal("expected version mismatch to disconnect without banning")
			}
			if err.Error() != test.wantErr {
				t.Fatalf("expected error %q, got %q", test.wantErr, err.Error())
			}
		})
	}
}
