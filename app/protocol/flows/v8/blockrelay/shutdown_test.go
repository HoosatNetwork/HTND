package blockrelay

import (
	stderrors "errors"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/flowcontext"
	peerpkg "github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

type testRelayInvsContext struct {
	shutdownChan <-chan struct{}
}

func (t *testRelayInvsContext) Domain() domain.Domain { return nil }

func (t *testRelayInvsContext) Config() *config.Config { return nil }

func (t *testRelayInvsContext) OnNewBlock(_ *externalapi.DomainBlock) error { return nil }

func (t *testRelayInvsContext) OnNewBlockTemplate() error { return nil }

func (t *testRelayInvsContext) OnPruningPointUTXOSetOverride() error { return nil }

func (t *testRelayInvsContext) SharedRequestedBlocks() *flowcontext.SharedRequestedBlocks { return nil }

func (t *testRelayInvsContext) Broadcast(_ appmessage.Message) error { return nil }

func (t *testRelayInvsContext) AddOrphan(_ *externalapi.DomainBlock) {}

func (t *testRelayInvsContext) GetOrphanRoots(_ *externalapi.DomainHash) ([]*externalapi.DomainHash, bool, error) {
	return nil, false, nil
}

func (t *testRelayInvsContext) IsOrphan(_ *externalapi.DomainHash) bool { return false }

func (t *testRelayInvsContext) IsIBDRunning() bool { return false }

func (t *testRelayInvsContext) IsRecoverableError(_ error) bool { return false }

func (t *testRelayInvsContext) IsNearlySynced() (bool, error) { return true, nil }

func (t *testRelayInvsContext) ShutdownChan() <-chan struct{} { return t.shutdownChan }

type testIBDContext struct {
	shutdownChan <-chan struct{}
}

func (t *testIBDContext) Domain() domain.Domain { return nil }

func (t *testIBDContext) Config() *config.Config { return nil }

func (t *testIBDContext) OnNewBlock(_ *externalapi.DomainBlock) error { return nil }

func (t *testIBDContext) OnNewBlockTemplate() error { return nil }

func (t *testIBDContext) OnPruningPointUTXOSetOverride() error { return nil }

func (t *testIBDContext) IsIBDRunning() bool { return false }

func (t *testIBDContext) TrySetIBDRunning(_ *peerpkg.Peer, _ bool) bool { return false }

func (t *testIBDContext) UnsetIBDRunning() {}

func (t *testIBDContext) IsRecoverableError(_ error) bool { return false }

func (t *testIBDContext) AddressManager() *addressmanager.AddressManager { return nil }

func (t *testIBDContext) ShutdownChan() <-chan struct{} { return t.shutdownChan }

func TestHandleRelayInvsReadInvStopsOnShutdown(t *testing.T) {
	shutdownChan := make(chan struct{})
	flow := &handleRelayInvsFlow{
		RelayInvsContext: &testRelayInvsContext{shutdownChan: shutdownChan},
		invChan:          make(chan invRelayBlock),
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := flow.readInv()
		errChan <- err
	}()

	close(shutdownChan)

	select {
	case err := <-errChan:
		if !stderrors.Is(err, router.ErrRouteClosed) {
			t.Fatalf("expected ErrRouteClosed, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("readInv did not exit on shutdown")
	}
}

func TestHandleIBDStartStopsOnShutdown(t *testing.T) {
	shutdownChan := make(chan struct{})
	close(shutdownChan)

	flow := &handleIBDFlow{
		IBDContext: &testIBDContext{shutdownChan: shutdownChan},
	}

	err := flow.start()
	if !stderrors.Is(err, router.ErrRouteClosed) {
		t.Fatalf("expected ErrRouteClosed, got %v", err)
	}
}
