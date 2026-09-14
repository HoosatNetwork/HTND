package flowcontext

import (
	"errors"
	"strings"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

// ErrPingTimeout signifies that a ping operation timed out.
var ErrPingTimeout = protocolerrors.New(false, "timeout expired on ping")

// HandleError handles an error from a flow,
// It sends the error to errChan if isStopping == 0 and increments isStopping
//
// If this is ErrRouteClosed - forward it to errChan
// If this is ProtocolError - logs the error, and forward it to errChan
// Otherwise - converts it to a ProtocolError (banning only for rule violations and malformed wire data) and
// forwards it to errChan
func (*FlowContext) HandleError(err error, flowName string, isStopping *uint32, errChan chan<- error) {
	isErrRouteClosed := errors.Is(err, router.ErrRouteClosed)
	if !isErrRouteClosed {
		// A database not-found that a flow returns ends that flow, so it must disconnect the peer. It
		// used to be dropped here without reaching errChan: the flow goroutine had already exited, the
		// peer stayed connected (and counted toward outbound targets), and nothing read that flow's
		// route again - requests to it timed out on the peer's side, and a dead relay flow meant no
		// more blocks from that peer. It is not grounds for banning: missing entries can come from
		// races with pruning or partial local state. An explicit protocol error that wraps a not-found
		// keeps its own ban decision, and the cause is formatted rather than wrapped so it is not
		// mistaken for a not-found again downstream.
		if protocolErr := (protocolerrors.ProtocolError{}); !errors.As(err, &protocolErr) && database.IsNotFoundError(err) {
			log.Warnf("Database entry not found in %s, disconnecting the peer: %v", flowName, err)
			err = protocolerrors.Errorf(false, "database entry not found in %s: %s", flowName, err.Error())
		} else if isWireFormatError(err) {
			// Check if this is a wire-format parsing error and treat it as a protocol error
			// instead of panicking. This allows graceful disconnection from peers sending
			// malformed data.
			log.Errorf("Wire format error from peer in %s, disconnecting: %v", flowName, err)
			// Convert to a ProtocolError that should ban the peer
			err = protocolerrors.Errorf(true, "invalid wire-format data: %s", err.Error())
		} else if protocolErr := (protocolerrors.ProtocolError{}); !errors.As(err, &protocolErr) {
			// Check if this is a rule error and treat it as a protocol error
			// instead of panicking. Rule violations from consensus should ban the peer.
			if ruleErr := (ruleerrors.RuleError{}); errors.As(err, &ruleErr) {
				err = protocolerrors.Wrapf(true, err, "rule violation in %s", flowName)
			} else {
				// For any other unexpected error, log it as a critical error but don't panic.
				// Panicking would crash the entire node, which is disproportionate to a peer error.
				// Disconnect, but do not ban: this catch-all is mostly this node's own failures - a database
				// I/O error, a full disk - which recur with every peer. Banning here made a local fault ban
				// the node's honest peers one after another.
				log.Errorf("Unexpected error in %s (not a protocol or rule error): %+v", flowName, err)
				err = protocolerrors.Errorf(false, "unexpected error in %s: %s", flowName, err.Error())
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
