package utxoindex

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
)

// UTXOIndex maintains an index between transaction scriptPublicKeys
// and UTXOs
type UTXOIndex struct {
	domain domain.Domain
	store  *utxoIndexStore

	mutex          sync.Mutex
	utxoIndexCache *utxoIndexLRUCache
	maxCacheSize   int
}
type utxoIndexLRUCache struct {
	maxSize    int
	items      map[string]*utxoIndexLRUNode
	head, tail *utxoIndexLRUNode
}

type utxoIndexLRUNode struct {
	key        string
	value      []UTXOPair
	prev, next *utxoIndexLRUNode
}

func newUtxoIndexLRUCache(maxSize int) *utxoIndexLRUCache {
	return &utxoIndexLRUCache{
		maxSize: maxSize,
		items:   make(map[string]*utxoIndexLRUNode),
	}
}

func (c *utxoIndexLRUCache) Get(key string) ([]UTXOPair, bool) {
	node, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.moveToFront(node)
	return node.value, true
}

func (c *utxoIndexLRUCache) Put(key string, value []UTXOPair) {
	if node, ok := c.items[key]; ok {
		node.value = value
		c.moveToFront(node)
		return
	}
	node := &utxoIndexLRUNode{key: key, value: value}
	c.items[key] = node
	c.addToFront(node)
	if len(c.items) > c.maxSize {
		c.evict()
	}
}

func (c *utxoIndexLRUCache) Delete(key string) {
	node, ok := c.items[key]
	if !ok {
		return
	}
	c.remove(node)
	delete(c.items, key)
}

func (c *utxoIndexLRUCache) moveToFront(node *utxoIndexLRUNode) {
	if c.head == node {
		return
	}
	c.remove(node)
	c.addToFront(node)
}

func (c *utxoIndexLRUCache) addToFront(node *utxoIndexLRUNode) {
	node.prev = nil
	node.next = c.head
	if c.head != nil {
		c.head.prev = node
	}
	c.head = node
	if c.tail == nil {
		c.tail = node
	}
}

func (c *utxoIndexLRUCache) remove(node *utxoIndexLRUNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		c.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		c.tail = node.prev
	}
}

func (c *utxoIndexLRUCache) evict() {
	if c.tail == nil {
		return
	}
	key := c.tail.key
	c.remove(c.tail)
	delete(c.items, key)
}

// New creates a new UTXO index.
//
// NOTE: While this is called no new blocks can be added to the consensus.
func New(domain domain.Domain, database database.Database) (*UTXOIndex, error) {
	const defaultCacheSize = 10000
	utxoIndex := &UTXOIndex{
		domain:         domain,
		store:          newUTXOIndexStore(database),
		utxoIndexCache: newUtxoIndexLRUCache(defaultCacheSize),
		maxCacheSize:   defaultCacheSize,
	}
	isSynced, err := utxoIndex.isSynced()
	if err != nil {
		return nil, err
	}

	///Has check is for migration to circulating supply, can be removed eventually.
	hasCirculatingSupplyKey, err := utxoIndex.store.database.Has(circulatingSupplyKey)
	if err != nil {
		return nil, err
	}

	if !isSynced || !hasCirculatingSupplyKey {

		err := utxoIndex.Reset()
		if err != nil {
			return nil, err
		}
	}

	return utxoIndex, nil
}

// Reset deletes the whole UTXO index and resyncs it from consensus.
func (ui *UTXOIndex) Reset() error {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	log.Infof("Starting UTXO index reset")

	err := ui.store.deleteAll()
	if err != nil {
		return err
	}

	virtualInfo, err := ui.domain.Consensus().GetVirtualInfo()
	if err != nil {
		return err
	}

	err = ui.store.initializeCirculatingSompiSupply() //At this point the database is empty, so the sole purpose of this call is to initialize the circulating supply key
	if err != nil {
		return err
	}

	var fromOutpoint *externalapi.DomainOutpoint
	for {
		const step = 1000
		virtualUTXOs, err := ui.domain.Consensus().GetVirtualUTXOs(virtualInfo.ParentHashes, fromOutpoint, step)
		if err != nil {
			return err
		}

		err = ui.store.addAndCommitOutpointsWithoutTransaction(virtualUTXOs)
		if err != nil {
			return err
		}

		if len(virtualUTXOs) < step {
			break
		}

		fromOutpoint = virtualUTXOs[len(virtualUTXOs)-1].Outpoint
	}

	// This has to be done last to mark that the reset went smoothly and no reset has to be called next time.
	err = ui.store.updateAndCommitVirtualParentsWithoutTransaction(virtualInfo.ParentHashes)
	if err != nil {
		return err
	}

	log.Infof("Finished UTXO index reset")
	return nil
}

func (ui *UTXOIndex) isSynced() (bool, error) {
	utxoIndexVirtualParents, err := ui.store.getVirtualParents()
	if err != nil {
		if database.IsNotFoundError(err) {
			return false, nil
		}
		return false, err
	}

	virtualInfo, err := ui.domain.Consensus().GetVirtualInfo()
	if err != nil {
		return false, err
	}

	return externalapi.HashesEqual(virtualInfo.ParentHashes, utxoIndexVirtualParents), nil
}

// Update updates the UTXO index with the given DAG selected parent chain changes
func (ui *UTXOIndex) Update(virtualChangeSet *externalapi.VirtualChangeSet) (*UTXOChanges, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "UTXOIndex.Update")
	defer onEnd()

	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	log.Tracef("Updating UTXO index with VirtualUTXODiff: %+v", virtualChangeSet.VirtualUTXODiff)
	err := ui.removeUTXOs(virtualChangeSet.VirtualUTXODiff.ToRemove())
	if err != nil {
		return nil, err
	}

	err = ui.addUTXOs(virtualChangeSet.VirtualUTXODiff.ToAdd())
	if err != nil {
		return nil, err
	}

	ui.store.updateVirtualParents(virtualChangeSet.VirtualParents)

	added, removed, _ := ui.store.stagedData()
	utxoIndexChanges := &UTXOChanges{
		Added:   added,
		Removed: removed,
	}

	err = ui.store.commit()
	if err != nil {
		return nil, err
	}

	log.Tracef("UTXO index updated with the UTXOChanged: %+v", utxoIndexChanges)
	return utxoIndexChanges, nil
}

func (ui *UTXOIndex) addUTXOs(toAdd externalapi.UTXOCollection) error {
	iterator := toAdd.Iterator()
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return err
		}

		log.Tracef("Adding outpoint %s to UTXO index", outpoint)
		ui.utxoIndexCache.Delete(entry.ScriptPublicKey().String())
		err = ui.store.add(entry.ScriptPublicKey(), outpoint, entry)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ui *UTXOIndex) removeUTXOs(toRemove externalapi.UTXOCollection) error {
	iterator := toRemove.Iterator()
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return err
		}

		log.Tracef("Removing outpoint %s from UTXO index", outpoint)
		ui.utxoIndexCache.Delete(entry.ScriptPublicKey().String())
		err = ui.store.remove(entry.ScriptPublicKey(), outpoint, entry)
		if err != nil {
			return err
		}
	}
	return nil
}

// UTXOs returns all the UTXOs for the given scriptPublicKey
func (ui *UTXOIndex) UTXOs(scriptPublicKey *externalapi.ScriptPublicKey) ([]UTXOPair, error) {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if pair, ok := ui.utxoIndexCache.Get(scriptPublicKey.String()); ok {
		return pair, nil
	}

	pair, err := ui.store.UTXOs(scriptPublicKey)
	ui.utxoIndexCache.Put(scriptPublicKey.String(), pair)
	return pair, err
}

// GetCirculatingSompiSupply returns the current circulating supply of sompis in the network
func (ui *UTXOIndex) GetCirculatingSompiSupply() (uint64, error) {

	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	return ui.store.getCirculatingSompiSupply()
}
