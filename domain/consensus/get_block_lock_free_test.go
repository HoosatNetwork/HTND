package consensus_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// TestGetBlockWhileBlocksAreAdded runs GetBlock, GetBlockEvenIfHeaderOnly, HasBlock, GetBlockHeader,
// GetBlockHeaders, GetBlockInfo and IsNearlySynced, which take no consensus lock, alongside block
// insertion. Run under -race it checks that the lock-free reads share no
// unsynchronized state with the writes; without it, that every block a reader asks for after its
// insertion is served whole.
func TestGetBlockWhileBlocksAreAdded(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGetBlockWhileBlocksAreAdded")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		const blockCount = 50
		var (
			inserted atomic.Pointer[[]*externalapi.DomainHash]
			done     atomic.Bool
			wg       sync.WaitGroup
		)
		empty := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		inserted.Store(&empty)

		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; !done.Load(); i++ {
					hashes := *inserted.Load()
					hash := hashes[i%len(hashes)]
					block, found, err := tc.GetBlock(hash)
					if err != nil {
						errs <- err
						return
					}
					if !found || block == nil || block.Header == nil {
						errs <- fmt.Errorf("GetBlock(%s) found=%t after the block was inserted", hash, found)
						return
					}
					if _, err := tc.GetBlockEvenIfHeaderOnly(hash); err != nil {
						errs <- err
						return
					}
					if has, err := tc.HasBlock(hash); err != nil || !has {
						errs <- fmt.Errorf("HasBlock(%s) = %t, %v after the block was inserted", hash, has, err)
						return
					}
					if _, err := tc.GetBlockHeader(hash); err != nil {
						errs <- err
						return
					}
					if _, err := tc.GetBlockHeaders(hashes); err != nil {
						errs <- err
						return
					}
					if info, err := tc.GetBlockInfo(hash); err != nil || !info.Exists {
						errs <- fmt.Errorf("GetBlockInfo(%s) = %+v, %v after the block was inserted", hash, info, err)
						return
					}
					if _, err := tc.IsNearlySynced(); err != nil {
						errs <- err
						return
					}
				}
			}()
		}

		tip := consensusConfig.GenesisHash
		for range blockCount {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			next := append(append([]*externalapi.DomainHash{}, *inserted.Load()...), tip)
			inserted.Store(&next)
		}
		done.Store(true)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%+v", err)
		}
	})
}
