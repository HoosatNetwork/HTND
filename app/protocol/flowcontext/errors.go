package flowcontext

import (
	"errors"
	"strings"
	"sync/atomic"

	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"

	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
)

var (
	// ErrPingTimeout signifies that a ping operation timed out.
	ErrPingTimeout = protocolerrors.New(false, "timeout expired on ping")
)

// HandleError handles an error from a flow,
// It sends the error to errChan if isStopping == 0 and increments isStopping
//
// If this is ErrRouteClosed - forward it to errChan
// If this is ProtocolError - logs the error, and forward it to errChan
// Otherwise - panics
func (*FlowContext) HandleError(err error, flowName string, isStopping *uint32, errChan chan<- error) {
	isErrRouteClosed := errors.Is(err, router.ErrRouteClosed)
	if !isErrRouteClosed {
		// Treat database not-found errors as recoverable/non-fatal for flows.
		// Returning such an error from a flow would otherwise cause HandleError to panic
		// because it's not a ProtocolError. In practice missing DB entries can appear
		// due to races / partial state while processing P2P messages; handle them
		// gracefully by logging and NOT escalating them to the protocol manager.
		if database.IsNotFoundError(err) {
			log.Debugf("Non-fatal DB not found in %s: %v", flowName, err)
			return
		}

		// Check if this is a wire-format parsing error and treat it as a protocol error
		// instead of panicking. This allows graceful disconnection from peers sending
		// malformed data.
		if isWireFormatError(err) {
			log.Errorf("Wire format error from peer in %s, disconnecting: %v", flowName, err)
			// Convert to a ProtocolError that should ban the peer
			err = protocolerrors.Errorf(true, "invalid wire-format data: %s", err.Error())
		} else if protocolErr := (protocolerrors.ProtocolError{}); !errors.As(err, &protocolErr) {
			// Check if this is a rule error and treat it as a protocol error
			// instead of panicking. Rule violations from consensus should ban the peer.
			if ruleErr := (ruleerrors.RuleError{}); errors.As(err, &ruleErr) {
				err = protocolerrors.Wrapf(true, err, "rule violation in %s", flowName)
			} else {
				panic(err)
			}
		}
		if errors.Is(err, ErrPingTimeout) {
			// Avoid printing the call stack on ping timeouts, since users get panicked and this case is not interesting
			log.Errorf("error from %s: %s", flowName, err)
		} else {
			// Explain to the user that this is not a panic, but only a protocol error with a specific peer
			logFrame := strings.Repeat("=", 52)
			log.Errorf("Non-critical peer protocol error from %s, printing the full stack for debug purposes: \n%s\n%+v \n%s",
				flowName, logFrame, err, logFrame)
		}
	}

	if atomic.AddUint32(isStopping, 1) == 1 {
		errChan <- err
	}
}

// isWireFormatError checks if an error is related to wire format parsing
func isWireFormatError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "proto: cannot parse invalid wire-format data") ||
		strings.Contains(errStr, "invalid wire-format") ||
		strings.Contains(errStr, "proto: ") && strings.Contains(errStr, "wire-format") ||
		strings.Contains(errStr, "protobuf") && strings.Contains(errStr, "parse")
}

// IsRecoverableError returns whether the error is recoverable
func (*FlowContext) IsRecoverableError(err error) bool {
	return err == nil || errors.Is(err, router.ErrRouteClosed) || errors.As(err, &protocolerrors.ProtocolError{}) || database.IsNotFoundError(err) || isWireFormatError(err)
}
