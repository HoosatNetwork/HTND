package utils

import (
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMLDSA44ChangeKeyUnavailableOverRPC(t *testing.T) {
	keyErr := status.Error(codes.Unknown, MLDSA44ChangeKeyUnavailable+": generate more ML-DSA-44 keys")
	if !IsMLDSA44ChangeKeyUnavailable(keyErr) {
		t.Fatal("the retry loop must return the missing-change-key error")
	}
	for _, err := range []error{nil, status.Error(codes.Unavailable, "daemon unavailable"), errors.New("Insufficient funds for send")} {
		if IsMLDSA44ChangeKeyUnavailable(err) {
			t.Errorf("retryable error %v was classified as a missing change key", err)
		}
	}
}

func TestChangeKeyRetryUsesRPCReason(t *testing.T) {
	rpcStatus, err := status.New(codes.FailedPrecondition, "different wording").WithDetails(
		&errdetails.ErrorInfo{Domain: "htnwallet", Reason: "MLDSA44_CHANGE_KEY_UNAVAILABLE"})
	if err != nil {
		t.Fatal(err)
	}
	wired := status.FromProto(rpcStatus.Proto()).Err()
	if !IsMLDSA44ChangeKeyUnavailable(wired) {
		t.Fatal("the daemon's change-key reason must survive RPC encoding and changes to the message")
	}

	messageOnly := status.Error(codes.FailedPrecondition, MLDSA44ChangeKeyUnavailable+": different wording")
	if IsMLDSA44ChangeKeyUnavailable(messageOnly) {
		t.Fatal("a status without the change-key reason should not stop retrying")
	}
	encoded := MLDSA44ChangeKeyRPCError(errors.New("different wording"))
	if status.Convert(encoded).Message() != "different wording" || !IsMLDSA44ChangeKeyUnavailable(encoded) {
		t.Fatalf("the RPC status lost its message or change-key reason: %v", encoded)
	}
}
