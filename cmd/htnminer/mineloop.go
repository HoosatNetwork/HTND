package main

import (
	nativeerrors "errors"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/v2/version"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnminer/templatemanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/util"
	utilrandom "github.com/HoosatNetwork/HTND/v2/util/random"
	"github.com/pkg/errors"
)

var hashesTried atomic.Uint64

const logHashRateInterval = 60 * time.Second

type PowTransfer struct {
	Block   *externalapi.DomainBlock
	PowHash *externalapi.DomainHash
}

func mineLoop(client *minerClient, numberOfBlocks uint64, targetBlocksPerSecond float64, mineWhenNotSynced bool,
	miningAddr util.Address, threads *int,
) error {
	errChan := make(chan error)
	doneChan := make(chan struct{})

	// Each template is solved at most once, so this rarely holds more than a block or two. The
	// capacity stays below router.DefaultMaxMessages so a slow node can't get us disconnected.
	foundBlockChan := make(chan *externalapi.DomainBlock, router.DefaultMaxMessages/2)

	if targetBlocksPerSecond > 0 {
		minSolveInterval := time.Duration(float64(time.Second) / targetBlocksPerSecond)
		log.Infof("Minimum time between mined blocks: %s", minSolveInterval)
		templatemanager.SetMinSolveInterval(minSolveInterval)
	}

	spawn("templatesLoop", func() {
		templatesLoop(client, miningAddr, errChan)
	})

	for t := 0; t < *threads; t++ {
		isFirstThread := t == 0
		spawn("mineThread", func() {
			mineThread(foundBlockChan, mineWhenNotSynced, isFirstThread)
		})
	}

	spawn("handleFoundBlock", func() {
		for i := uint64(0); numberOfBlocks == 0 || i < numberOfBlocks; i++ {
			block := <-foundBlockChan
			err := handleFoundBlock(client, block)
			if err != nil {
				errChan <- err
				return
			}
		}
		doneChan <- struct{}{}
	})

	logHashRate()

	select {
	case err := <-errChan:
		return err
	case <-doneChan:
		return nil
	}
}

// mineThread mines the current job until it is solved or replaced. It copies the PoW state once
// per job, so the per-hash cost is the hash itself plus one atomic load.
func mineThread(foundBlockChan chan<- *externalapi.DomainBlock, mineWhenNotSynced bool, shouldLog bool) {
	for {
		generation := templatemanager.Generation()
		job := waitForJob(mineWhenNotSynced, shouldLog)
		state := *job.State

		nonce, err := utilrandom.Uint64()
		if err != nil {
			panic(err)
		}
		for templatemanager.Generation() == generation {
			nonce++
			state.Nonce = nonce
			powNum, hash := state.CalculateProofOfWorkValue()
			hashesTried.Add(1)
			if powNum.Cmp(&state.Target) > 0 {
				continue
			}
			if !templatemanager.MarkSolved(job.ID) {
				// Another thread solved this template first; this block would be its sibling.
				break
			}
			block := *job.Block
			mutHeader := block.Header.ToMutable()
			mutHeader.SetNonce(nonce)
			block.Header = mutHeader.ToImmutable()
			block.PoWHash = hash.String()
			foundBlockChan <- &block
			break
		}
	}
}

// waitForJob blocks until there is a job this thread may mine.
func waitForJob(mineWhenNotSynced bool, shouldLog bool) *templatemanager.Job {
	const logInterval = 2 * time.Second
	lastLog := time.Time{}
	for {
		job, changed, resumeAt := templatemanager.Current()
		if job != nil && (job.IsSynced || mineWhenNotSynced) {
			return job
		}
		if shouldLog && time.Since(lastLog) >= logInterval {
			hasTemplate, isSynced := templatemanager.HasTemplate()
			switch {
			case !hasTemplate:
				log.Info("Waiting for the initial template")
				lastLog = time.Now()
			case !isSynced && !mineWhenNotSynced:
				log.Warnf("Hoosatd is not synced. Skipping current block template")
				lastLog = time.Now()
			}
		}

		wait := logInterval
		if !resumeAt.IsZero() {
			wait = time.Until(resumeAt)
		}
		timer := time.NewTimer(wait)
		select {
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func logHashRate() {
	spawn("logHashRate", func() {
		lastCheck := time.Now()
		for range time.Tick(logHashRateInterval) {
			currentHashesTried := hashesTried.Load()
			currentTime := time.Now()
			kiloHashesTried := float64(currentHashesTried) / 1000.0
			hashRate := kiloHashesTried / currentTime.Sub(lastCheck).Seconds()
			log.Infof("Current hash rate is %.2f Khash/s", hashRate)
			lastCheck = currentTime
			// subtract from hashesTried the hashes we already sampled
			hashesTried.Add(-currentHashesTried)
		}
	})
}

func handleFoundBlock(client *minerClient, block *externalapi.DomainBlock) error {
	blockHash := consensushashing.BlockHash(block)
	log.Infof("Submitting block: %s with PoW Hash: %s", blockHash, block.PoWHash)

	rejectReason, err := client.SubmitBlock(block, block.PoWHash)
	if err == nil {
		// The node notifies about a new template once it has added the block, but ask right away
		// as well: until the template builds on this block the threads have nothing to mine.
		client.requestTemplateRefresh()
		return nil
	}
	templatemanager.SubmitFailed(block)

	if nativeerrors.Is(err, router.ErrTimeout) {
		log.Warnf("Got timeout while submitting block: %s\n with PoW Hash: %s\n%s", blockHash, block.PoWHash, err)
		return client.Reconnect()
	}
	if nativeerrors.Is(err, router.ErrRouteClosed) {
		log.Infof("Got route is closed while submitting block to %s. "+
			"The client is most likely reconnecting", client.Address())
		return nil
	}
	switch rejectReason {
	case appmessage.RejectReasonIsInIBD:
		const waitTime = 100 * time.Millisecond
		log.Warnf("Block %s was rejected because the node is in IBD. Waiting for %s", blockHash, waitTime)
		time.Sleep(waitTime)
		return nil
	case appmessage.RejectReasonBlockInvalid:
		// Usually a template that went stale while we mined it. One bad block is no reason to stop mining.
		log.Warnf("Block %s was rejected: %s", blockHash, err)
		client.requestTemplateRefresh()
		return nil
	}
	return errors.Wrapf(err, "Error submitting block %s to %s", blockHash, client.Address())
}

func templatesLoop(client *minerClient, miningAddr util.Address, errChan chan error) {
	getBlockTemplate := func() {
		template, err := client.GetBlockTemplate(miningAddr.String(), "hoosatminer-"+version.Version())
		if nativeerrors.Is(err, router.ErrTimeout) {
			log.Warnf("Got timeout while requesting block template from %s: %s", client.Address(), err)
			reconnectErr := client.Reconnect()
			if reconnectErr != nil {
				errChan <- reconnectErr
			}
			return
		}
		if nativeerrors.Is(err, router.ErrRouteClosed) {
			log.Debugf("Got route is closed while requesting block template from %s. "+
				"The client is most likely reconnecting", client.Address())
			return
		}
		if err != nil {
			errChan <- errors.Wrapf(err, "Error getting block template from %s", client.Address())
			return
		}
		err = templatemanager.Set(template)
		if err != nil {
			errChan <- errors.Wrapf(err, "Error setting block template from %s", client.Address())
			return
		}
	}

	getBlockTemplate()
	const tickerTime = 100 * time.Millisecond
	ticker := time.NewTicker(tickerTime)
	defer ticker.Stop()
	for {
		select {
		case <-client.newBlockTemplateNotificationChan:
			getBlockTemplate()
			ticker.Reset(tickerTime)
		case <-ticker.C:
			getBlockTemplate()
		}
	}
}
