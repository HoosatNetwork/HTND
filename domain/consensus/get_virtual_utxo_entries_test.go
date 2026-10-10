package consensus

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestGetVirtualUTXOEntries pins the two things the UTXO RPCs rely on. Answers line up with the
// outpoints asked about, including across the chunks the lookup is split into. And the call never
// waits out a long consensus lock hold: block processing keeps the lock through a pruning point UTXO
// set update for 15 seconds to nearly 4 minutes on mainnet, and a GetUtxosByAddresses that queued
// behind it reached its client as DeadlineExceeded.
//
// It builds its own mainnet config instead of using testutils.ForAllNets, which imports this package
// and so cannot be used from a test inside it - and being inside it is what reaches the lock.
func TestGetVirtualUTXOEntries(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	func() {
		config := &Config{Params: dagconfig.MainnetParams}
		config.SkipProofOfWork = true
		tc, teardown, err := NewFactory().NewTestConsensus(config, "TestGetVirtualUTXOEntries")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		tip := config.GenesisHash
		for i := 0; i < 10; i++ {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}
		virtualParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("GetVirtualSelectedParent: %+v", err)
		}
		held, err := tc.GetVirtualUTXOs([]*externalapi.DomainHash{virtualParent}, nil, 1000)
		if err != nil {
			t.Fatalf("GetVirtualUTXOs: %+v", err)
		}
		if len(held) == 0 {
			t.Fatalf("mining 10 blocks left virtual's UTXO set empty, so there is no held coin to look up")
		}

		// Outpoints virtual never held, with held coins placed on both sides of chunk boundaries.
		count := 3*virtualUTXOEntriesChunkSize + 7
		outpoints := make([]*externalapi.DomainOutpoint, count)
		want := make([]externalapi.UTXOEntry, count)
		for i := range outpoints {
			id := [externalapi.DomainHashSize]byte{byte(i), byte(i >> 8), 0xfe, 0xed}
			outpoints[i] = &externalapi.DomainOutpoint{
				TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(&id)),
				Index:         uint32(i),
			}
		}
		for k, position := range []int{0, virtualUTXOEntriesChunkSize - 1, virtualUTXOEntriesChunkSize, count - 1} {
			pair := held[k%len(held)]
			outpoints[position] = pair.Outpoint
			want[position] = pair.UTXOEntry
		}

		entries, virtualParents, ok, err := tc.GetVirtualUTXOEntries(outpoints, time.Second)
		if err != nil || !ok {
			t.Fatalf("an uncontended lookup must succeed: ok=%t err=%+v", ok, err)
		}
		if len(entries) != count {
			t.Fatalf("expected %d answers, got %d", count, len(entries))
		}
		virtualInfo, err := tc.GetVirtualInfo()
		if err != nil {
			t.Fatalf("GetVirtualInfo: %+v", err)
		}
		if !externalapi.HashesEqual(virtualParents, virtualInfo.ParentHashes) {
			t.Fatalf("the lookup must report virtual's parents while it ran: got %v, virtual has %v",
				virtualParents, virtualInfo.ParentHashes)
		}
		for i := range entries {
			switch {
			case want[i] == nil && entries[i] != nil:
				t.Fatalf("outpoint %d was never held by virtual but got an entry", i)
			case want[i] != nil && (entries[i] == nil || !entries[i].Equal(want[i])):
				t.Fatalf("outpoint %d is held by virtual; got %v, want %v", i, entries[i], want[i])
			}
		}

		if entries, _, ok, err := tc.GetVirtualUTXOEntries(nil, time.Second); err != nil || !ok || len(entries) != 0 {
			t.Fatalf("no outpoints must be answered with no entries: %v %t %+v", entries, ok, err)
		}

		// A holder that keeps the lock: the call gives up near maxWait rather than waiting for it.
		lock := tc.(*testConsensus).consensus.lock
		lock.Lock()
		start := time.Now()
		entries, _, ok, err = tc.GetVirtualUTXOEntries(outpoints[:1], 50*time.Millisecond)
		waited := time.Since(start)
		lock.Unlock()
		if err != nil || ok || entries != nil {
			t.Fatalf("a held lock must be reported as busy, not answered or errored: %v %t %+v", entries, ok, err)
		}
		if waited > time.Second {
			t.Fatalf("with maxWait 50ms the call waited %s for the lock holder", waited)
		}

		// A holder that lets go within maxWait: the call takes the lock and answers.
		lock.Lock()
		go func() {
			time.Sleep(20 * time.Millisecond)
			lock.Unlock()
		}()
		if _, _, ok, err := tc.GetVirtualUTXOEntries(outpoints[:1], 2*time.Second); err != nil || !ok {
			t.Fatalf("a lock released within maxWait must be taken: ok=%t err=%+v", ok, err)
		}
	}()
}
