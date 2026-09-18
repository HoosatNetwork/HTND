package blockrelay

import (
	stderrors "errors"
	"testing"

	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// The interfaces are embedded rather than implemented: only the handful of methods resolveVirtual
// actually reaches are overridden, and anything else would panic on the nil interface rather than
// pass silently.
type repairTestConsensus struct {
	externalapi.Consensus
	resolveErrs  []error
	resolveCalls int
	repairCalls  int
	repairErr    error
	repairReset  uint64
}

func (c *repairTestConsensus) ResolveVirtual(_ func(uint64, uint64)) error {
	c.resolveCalls++
	if len(c.resolveErrs) == 0 {
		return nil
	}
	err := c.resolveErrs[0]
	c.resolveErrs = c.resolveErrs[1:]
	return err
}

func (c *repairTestConsensus) RepairDisqualifiedTipChains() (uint64, error) {
	c.repairCalls++
	if c.repairErr != nil {
		return 0, c.repairErr
	}
	return c.repairReset, nil
}

type repairTestDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d *repairTestDomain) Consensus() externalapi.Consensus { return d.consensus }

type repairTestContext struct {
	IBDContext
	domain domain.Domain
}

func (c *repairTestContext) Domain() domain.Domain { return c.domain }

func newRepairTestFlow(consensus *repairTestConsensus) *handleIBDFlow {
	return &handleIBDFlow{
		IBDContext: &repairTestContext{domain: &repairTestDomain{consensus: consensus}},
	}
}

// TestResolveVirtualRepairsBlockStatusesWhenEveryTipIsDisqualified pins the recovery: a node whose
// tips are all disqualified cannot move virtual off the virtual genesis marker, and re-running IBD
// reaches the same statuses and stops in the same place, so the disqualified chains have to be reset
// and the resolve retried instead of the flow giving up.
func TestResolveVirtualRepairsBlockStatusesWhenEveryTipIsDisqualified(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{errors.WithStack(externalapi.ErrVirtualHasNoUsableTip)},
		repairReset: 7,
	}
	flow := newRepairTestFlow(consensus)

	if err := flow.resolveVirtual(100); err != nil {
		t.Fatalf("resolveVirtual after a repair that fixed the statuses: %+v", err)
	}
	if consensus.repairCalls != 1 {
		t.Fatalf("expected block statuses to be repaired exactly once, they were repaired %d times",
			consensus.repairCalls)
	}
	if consensus.resolveCalls != 2 {
		t.Fatalf("expected virtual to be resolved again after the repair, ResolveVirtual ran %d times",
			consensus.resolveCalls)
	}
}

// The repair is a recovery step, not a retry loop: if the second resolve is stuck in the same place
// the statuses were not the problem, and repairing again would not help.
func TestResolveVirtualRepairsBlockStatusesOnlyOnce(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{
			errors.WithStack(externalapi.ErrVirtualHasNoUsableTip),
			errors.WithStack(externalapi.ErrVirtualHasNoUsableTip),
		},
		repairReset: 3,
	}
	flow := newRepairTestFlow(consensus)

	if err := flow.resolveVirtual(100); err == nil {
		t.Fatalf("expected resolveVirtual to report the failure when the repair did not help")
	}
	if consensus.repairCalls != 1 {
		t.Fatalf("expected exactly one repair, got %d", consensus.repairCalls)
	}
	if consensus.resolveCalls != 2 {
		t.Fatalf("expected exactly two resolve attempts, got %d", consensus.resolveCalls)
	}
}

// A resolve failure that is not the stuck-virtual case must not trigger the repair at all.
func TestResolveVirtualDoesNotRepairOnOtherErrors(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{stderrors.New("some other resolve failure")},
	}
	flow := newRepairTestFlow(consensus)

	if err := flow.resolveVirtual(100); err == nil {
		t.Fatalf("expected resolveVirtual to report the failure")
	}
	if consensus.repairCalls != 0 {
		t.Fatalf("expected no repair for an unrelated error, got %d", consensus.repairCalls)
	}
}

// A repair that itself fails must leave the original resolve failure as the reported error, rather
// than replacing it with the repair's.
func TestResolveVirtualReportsResolveFailureWhenRepairFails(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{errors.WithStack(externalapi.ErrVirtualHasNoUsableTip)},
		repairErr:   stderrors.New("repair exploded"),
	}
	flow := newRepairTestFlow(consensus)

	err := flow.resolveVirtual(100)
	if err == nil {
		t.Fatalf("expected resolveVirtual to report a failure")
	}
	if !errors.Is(err, externalapi.ErrVirtualHasNoUsableTip) {
		t.Fatalf("expected the original resolve failure to be reported, got %v", err)
	}
	if consensus.resolveCalls != 1 {
		t.Fatalf("expected no second resolve after a failed repair, ResolveVirtual ran %d times",
			consensus.resolveCalls)
	}
}

// When nothing on any tip's chain was disqualified, the tips are invalid rather than disqualified.
// Resetting statuses cannot help, so the flow must report rather than resolve a second time for
// nothing. The whole-store repair could not tell these apart; this one can, because it counts what
// it reset.
func TestResolveVirtualDoesNotRetryWhenNothingWasReset(t *testing.T) {
	consensus := &repairTestConsensus{
		resolveErrs: []error{errors.WithStack(externalapi.ErrVirtualHasNoUsableTip)},
		repairReset: 0,
	}
	flow := newRepairTestFlow(consensus)

	if err := flow.resolveVirtual(100); err == nil {
		t.Fatalf("expected resolveVirtual to report the failure when there was nothing to reset")
	}
	if consensus.repairCalls != 1 {
		t.Fatalf("expected exactly one repair attempt, got %d", consensus.repairCalls)
	}
	if consensus.resolveCalls != 1 {
		t.Fatalf("expected no second resolve when nothing was reset, ResolveVirtual ran %d times",
			consensus.resolveCalls)
	}
}
