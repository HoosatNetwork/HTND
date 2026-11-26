package utils

import (
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MLDSA44ChangeKeyUnavailable is the message prefix used by daemons that return an untyped error.
const MLDSA44ChangeKeyUnavailable = "cannot derive the ML-DSA-44 change address"

const (
	changeKeyErrorDomain = "htnwallet"
	changeKeyErrorReason = "MLDSA44_CHANGE_KEY_UNAVAILABLE"
)

// MLDSA44ChangeKeyRPCError attaches the reason send and vote use to stop retrying.
func MLDSA44ChangeKeyRPCError(err error) error {
	base := status.New(codes.FailedPrecondition, err.Error())
	withReason, detailErr := base.WithDetails(&errdetails.ErrorInfo{Domain: changeKeyErrorDomain, Reason: changeKeyErrorReason})
	if detailErr != nil {
		return base.Err()
	}
	return withReason.Err()
}

// IsMLDSA44ChangeKeyUnavailable identifies the daemon's change-key failure over gRPC.
func IsMLDSA44ChangeKeyUnavailable(err error) bool {
	rpcStatus, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch rpcStatus.Code() {
	case codes.Unknown:
		// Older daemons return this diagnostic without status details.
		return strings.HasPrefix(rpcStatus.Message(), MLDSA44ChangeKeyUnavailable+":")
	case codes.FailedPrecondition:
	default:
		return false
	}
	for _, detail := range rpcStatus.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == changeKeyErrorDomain && info.Reason == changeKeyErrorReason {
			return true
		}
	}
	return false
}
