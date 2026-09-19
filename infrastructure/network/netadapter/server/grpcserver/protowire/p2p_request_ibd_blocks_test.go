package protowire

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func hashesForRequestIBDBlocksTest(count int) []*externalapi.DomainHash {
	hashes := make([]*externalapi.DomainHash, count)
	for i := range hashes {
		hashes[i] = &externalapi.DomainHash{}
	}
	return hashes
}

// TestRequestIBDBlocksHashesCap is HTN-115's regression test.
//
// RequestIBDBlocks had no element-count cap at all, unlike every other P2P list message
// (MaxRequestRelayBlocksHashes covers RequestRelayBlocks). A legitimate request never exceeds
// getIBDBatchSize() (495 today), so an uncapped message lets a peer ask a syncer to serve an
// arbitrarily large slice of the whole stored DAG in one shot. This pins that both conversion
// directions (toAppMessage, used when receiving one over the wire, and fromAppMessage, used when
// this node builds one to send) reject a message over MaxRequestIBDBlocksHashes and accept one at
// the boundary.
func TestRequestIBDBlocksHashesCap(t *testing.T) {
	tests := []struct {
		name      string
		count     int
		expectErr bool
	}{
		{name: "at the cap", count: appmessage.MaxRequestIBDBlocksHashes, expectErr: false},
		{name: "one over the cap", count: appmessage.MaxRequestIBDBlocksHashes + 1, expectErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hashes := hashesForRequestIBDBlocksTest(test.count)

			protoMessage := &RequestIBDBlocksMessage{Hashes: domainHashesToProto(hashes)}
			_, err := protoMessage.toAppMessage()
			if test.expectErr && err == nil {
				t.Fatalf("toAppMessage: expected an error for %d hashes, got none", test.count)
			}
			if !test.expectErr && err != nil {
				t.Fatalf("toAppMessage: unexpected error for %d hashes: %+v", test.count, err)
			}
			if test.expectErr && !strings.Contains(err.Error(), "too many hashes") {
				t.Fatalf("toAppMessage: expected a 'too many hashes' error, got: %+v", err)
			}

			appMsg := &appmessage.MsgRequestIBDBlocks{Hashes: hashes}
			wireMsg := &HoosatdMessage_RequestIBDBlocks{}
			err = wireMsg.fromAppMessage(appMsg)
			if test.expectErr && err == nil {
				t.Fatalf("fromAppMessage: expected an error for %d hashes, got none", test.count)
			}
			if !test.expectErr && err != nil {
				t.Fatalf("fromAppMessage: unexpected error for %d hashes: %+v", test.count, err)
			}
			if test.expectErr && !strings.Contains(err.Error(), "too many hashes") {
				t.Fatalf("fromAppMessage: expected a 'too many hashes' error, got: %+v", err)
			}
		})
	}
}
