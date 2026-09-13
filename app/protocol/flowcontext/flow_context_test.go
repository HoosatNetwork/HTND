package flowcontext

import "testing"

func TestCloseIsIdempotent(t *testing.T) {
	flowContext := &FlowContext{
		shutdownChan: make(chan struct{}),
	}

	flowContext.Close()
	flowContext.Close()

	select {
	case <-flowContext.ShutdownChan():
	default:
		t.Fatalf("expected shutdown channel to be closed")
	}
}
