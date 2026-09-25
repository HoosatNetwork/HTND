package consensus

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestDroppedVirtualChangeSetIsReported pins that a change set lost to a full events channel is
// reported on the next one delivered. Virtual is committed before its change set is sent, so a lost
// one is a diff the UTXO index never replays; if nothing says so, the index keeps coins consensus
// spent and serves them indefinitely.
//
// Both ways of losing it are covered: the change set itself not fitting, and the BlockAdded event
// sent just before it not fitting, which returns before the change set is attempted.
func TestDroppedVirtualChangeSetIsReported(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	for _, test := range []struct {
		name    string
		prefill int
	}{
		{name: "change set does not fit", prefill: 1},
		{name: "block added event does not fit", prefill: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := &Config{Params: dagconfig.MainnetParams}
			config.SkipProofOfWork = true
			tc, teardown, err := NewFactory().NewTestConsensus(config, "TestDroppedVirtualChangeSetIsReported")
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}
			defer teardown(false)

			events := make(chan externalapi.ConsensusEvent, 2)
			tc.(*testConsensus).consensusEventsChan = events
			drain := func() []externalapi.ConsensusEvent {
				var drained []externalapi.ConsensusEvent
				for len(events) > 0 {
					drained = append(drained, <-events)
				}
				return drained
			}
			lastChangeSet := func(drained []externalapi.ConsensusEvent) *externalapi.VirtualChangeSet {
				for i := len(drained) - 1; i >= 0; i-- {
					if changeSet, ok := drained[i].(*externalapi.VirtualChangeSet); ok {
						return changeSet
					}
				}
				t.Fatalf("no VirtualChangeSet among %d delivered events", len(drained))
				return nil
			}

			for range test.prefill {
				events <- &externalapi.BlockAdded{}
			}
			_, _, err = tc.AddBlock([]*externalapi.DomainHash{config.GenesisHash}, nil, nil)
			if err == nil {
				t.Fatalf("AddBlock succeeded with a full events channel; the test no longer loses a change set")
			}
			drain()

			tip, err := tc.GetVirtualSelectedParent()
			if err != nil {
				t.Fatalf("GetVirtualSelectedParent: %+v", err)
			}
			if tip.Equal(config.GenesisHash) {
				t.Fatalf("the block whose events were lost did not become virtual's selected parent")
			}

			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			if !lastChangeSet(drain()).EarlierChangeSetsDropped {
				t.Fatalf("the change set delivered after a lost one does not report the loss")
			}

			_, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			if lastChangeSet(drain()).EarlierChangeSetsDropped {
				t.Fatalf("the loss is still reported after it was delivered once")
			}
		})
	}
}
