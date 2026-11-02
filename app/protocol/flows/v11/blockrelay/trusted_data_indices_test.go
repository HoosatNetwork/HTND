package blockrelay

import (
	"errors"
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestProcessBlockWithTrustedDataRejectsOutOfRangeIndices pins that trusted-data indices pointing past
// the trusted data a peer sent are rejected with a banning protocol error. They used to be used as slice
// indices unchecked, and the panic took the node down during IBD.
func TestProcessBlockWithTrustedDataRejectsOutOfRangeIndices(t *testing.T) {
	block := appmessage.DomainBlockToMsgBlock(dagconfig.MainnetParams.GenesisBlock)
	tests := []struct {
		name  string
		block *appmessage.MsgBlockWithTrustedDataV4
	}{
		{name: "DAA window index", block: &appmessage.MsgBlockWithTrustedDataV4{Block: block, DAAWindowIndices: []uint64{3}}},
		{name: "GHOSTDAG data index", block: &appmessage.MsgBlockWithTrustedDataV4{Block: block, GHOSTDAGDataIndices: []uint64{3}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flow := &handleIBDFlow{}
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						err = fmt.Errorf("panic: %v", recovered)
					}
				}()
				err = flow.processBlockWithTrustedData(nil, test.block, &appmessage.MsgTrustedData{})
			}()

			var protocolErr protocolerrors.ProtocolError
			if !errors.As(err, &protocolErr) || !protocolErr.ShouldBan {
				t.Fatalf("expected a banning protocol error, got %v", err)
			}
		})
	}
}
