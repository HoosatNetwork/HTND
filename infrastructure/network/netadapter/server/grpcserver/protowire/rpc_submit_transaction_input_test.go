package protowire

import (
	"testing"
	"time"
)

// TestRpcTransactionInputWithVerboseDataConverts pins that an RPC transaction input whose verbose data
// is present converts and returns. The conversion used to loop on the presence check instead of
// testing it once, so such an input spun forever inside the connection's receive path.
func TestRpcTransactionInputWithVerboseDataConverts(t *testing.T) {
	input := &RpcTransactionInput{
		PreviousOutpoint: &RpcOutpoint{
			TransactionId: "0000000000000000000000000000000000000000000000000000000000000001",
			Index:         0,
		},
		VerboseData: &RpcTransactionInputVerboseData{},
	}

	done := make(chan error, 1)
	go func() {
		_, err := input.toAppMessage()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("toAppMessage: %+v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("converting an input with verbose data did not return")
	}
}
