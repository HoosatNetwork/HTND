package blockrelay

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

type anticoneNotFoundConsensus struct {
	externalapi.Consensus
}

func (anticoneNotFoundConsensus) GetAnticone(*externalapi.DomainHash, *externalapi.DomainHash, uint64) (
	[]*externalapi.DomainHash, error,
) {
	return nil, errors.Wrapf(database.ErrNotFound, "block does not exist")
}

type anticoneNotFoundDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d anticoneNotFoundDomain) Consensus() externalapi.Consensus { return d.consensus }

type anticoneNotFoundContext struct {
	domain domain.Domain
	config *config.Config
}

func (c anticoneNotFoundContext) Domain() domain.Domain  { return c.domain }
func (c anticoneNotFoundContext) Config() *config.Config { return c.config }

// TestHandleRequestAnticoneDoesNotForceABanOnNotFound is HTN-168's regression test.
//
// GetAnticone fails, among other reasons, whenever the peer asks about a hash this node has pruned or
// never had - a legitimate case, not evidence of anything attributable to the peer's request. The flow
// used to wrap every GetAnticone error in a banning protocol error unconditionally
// (protocolerrors.Wrap(true, err, ...)), so an honest peer asking about a hash outside this node's
// retained window got banned for it, same as this node's own local errors underneath GetAnticone would.
// The fix returns the error unwrapped, matching the sibling handle_request_headers.go flow, so
// flowcontext.HandleError's own classifier decides instead - which does not ban on a not-found.
func TestHandleRequestAnticoneDoesNotForceABanOnNotFound(t *testing.T) {
	context := anticoneNotFoundContext{
		domain: anticoneNotFoundDomain{consensus: anticoneNotFoundConsensus{}},
		config: &config.Config{Flags: &config.Flags{NetworkFlags: config.NetworkFlags{ActiveNetParams: &dagconfig.MainnetParams}}},
	}

	incoming := router.NewRoute("incoming")
	outgoing := router.NewRoute("outgoing")
	if err := incoming.Enqueue(appmessage.NewMsgRequestAnticone(&externalapi.DomainHash{}, &externalapi.DomainHash{})); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}

	err := HandleRequestAnticone(context, incoming, outgoing, nil)
	if err == nil {
		t.Fatalf("expected the flow to end with the not-found error, got nil")
	}
	if !database.IsNotFoundError(err) {
		t.Fatalf("expected the raw not-found error to propagate unwrapped, got: %+v", err)
	}

	var protocolErr protocolerrors.ProtocolError
	if errors.As(err, &protocolErr) && protocolErr.ShouldBan {
		t.Fatalf("a peer asking about a hash this node does not have must not be banned for it, got: %+v", err)
	}
}
