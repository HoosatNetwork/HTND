package blockrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/pkg/errors"
)

func requireNonBanningProtocolError(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error ending the IBD round", what)
	}
	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("%s: expected a ProtocolError, got %T: %v", what, err, err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("%s: the peer must not be banned for this node's own block statuses: %v", what, err)
	}
}

// TestIBDDoesNotBanPeerForLocallyDisqualifiedChain pins that an IBD round which ends on this node's
// own disqualified chain - resolve fails with a rule error, the repair does not help - does not ban
// the peer. The peer sent valid blocks; the verdict is about what this node had already recorded.
// Banning burnt through honest peers one IBD round at a time, and left a node that needed a peer to
// recover from with none.
func TestIBDDoesNotBanPeerForLocallyDisqualifiedChain(t *testing.T) {
	ruleErr := errors.Wrapf(ruleerrors.ErrBadUTXOCommitment, "block X is disqualified")
	consensus := &repairTestConsensus{resolveErrs: []error{ruleErr}}
	flow := newRepairTestFlow(consensus)

	requireNonBanningProtocolError(t, flow.resolveVirtual(100), "resolve failing with a rule error")
	if consensus.repairCalls != 0 {
		t.Fatalf("a rule error that is not the stuck-virtual case must not trigger the repair")
	}

	// The same after the stuck-virtual repair was tried and did not help.
	consensus = &repairTestConsensus{
		resolveErrs: []error{
			errors.WithStack(externalapi.ErrVirtualHasNoUsableTip),
			errors.WithStack(externalapi.ErrVirtualHasNoUsableTip),
		},
		repairReset: 4,
	}
	flow = newRepairTestFlow(consensus)
	requireNonBanningProtocolError(t, flow.resolveVirtual(100), "resolve still stuck after the repair")
}

// TestIBDRepairsOnConsensusStateManagerNoPendingTip pins that the IBD repair fires on the error the
// consensus state manager actually returns when no tip is left to resolve - "no pending tip", wrapped
// around ErrVirtualHasNoUsableTip - and not only on the bare sentinel. It used to be a plain error,
// so the repair written for exactly this state never ran and IBD stopped on the locally disqualified
// hash every round.
func TestIBDRepairsOnConsensusStateManagerNoPendingTip(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{errors.Wrapf(externalapi.ErrVirtualHasNoUsableTip,
			"no pending tip: all %d tips are disqualified/invalid", 3)},
		repairReset: 2,
	}
	flow := newRepairTestFlow(consensus)

	if err := flow.resolveVirtual(100); err != nil {
		t.Fatalf("resolveVirtual after a repair that fixed the statuses: %+v", err)
	}
	if consensus.repairCalls != 1 || consensus.resolveCalls != 2 {
		t.Fatalf("expected one repair and a second resolve, got %d repairs and %d resolves",
			consensus.repairCalls, consensus.resolveCalls)
	}
}

// The IBD deadline covers this node's own work - virtual resolution and the status repair after it,
// the slow part on a node with a long disqualified segment - so running past it is no evidence
// against the peer.
func TestIBDTimeoutDoesNotBan(t *testing.T) {
	requireNonBanningProtocolError(t, ibdTimeoutError(), "IBD timeout")
}
