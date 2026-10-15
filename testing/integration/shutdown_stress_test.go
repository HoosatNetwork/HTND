package integration

// Shutdown stress harness. Every test starts in-process nodes on loopback with temporary appdirs (simnet), triggers
// ComponentManager.Stop at a chosen point and then checks what app.main relies on:
//
//   - Stop returns within stopBound and reports success; only then is the database closed, as in app.main.
//   - Nothing uses the database during or after Close (guardDB records a call still running when Close is called,
//     and every call made after it).
//   - No goroutine started by the node outlives the shutdown: goroutines with node frames, counted by the function
//     that created them, are compared with a count taken before the node started. This is a heuristic, not proof
//     that every goroutine was joined.
//   - The datadir reopens and the node serves its DAG on it again (Idle also checks that the selected tip is
//     unchanged, the IBD scenarios that the syncee finishes syncing). The restarted node is checked only for a
//     bounded, successful Stop.
//
// Not every scenario runs every check: the per-scenario comments say which ones a scenario skips.
//
// The tests are skipped unless HTND_SHUTDOWN_STRESS=1; run each one in its own process, since a panic in a node
// goroutine exits the whole test binary:
//
//	HTND_SHUTDOWN_STRESS=1 go test -race -p 1 -count=1 -run '^TestShutdownStress<Name>$' ./testing/integration/

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app"
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/pow"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

const stopBound = 30 * time.Second

func skipUnlessStress(t *testing.T) {
	if os.Getenv("HTND_SHUTDOWN_STRESS") != "1" {
		t.Skip("set HTND_SHUTDOWN_STRESS=1 to run the shutdown stress harness")
	}
}

// guardDB wraps the node's database. A call still running when Close is called, and a call made while Close waits
// for it, are recorded: Pebble does not allow Close concurrently with other calls. Close then waits for them anyway,
// so that the test process does not crash, but the shutdown counts as a failure. Calls made after Close are recorded
// and fail instead of reaching Pebble, which would panic. writeDelay slows every write down ("slow DB").
type guardDB struct {
	database.Database
	lock       sync.RWMutex
	closed     bool
	writeDelay time.Duration

	// running counts the calls in progress; closing is set once Close is called. stateLock serializes a call's entry
	// and exit with Close setting closing and reading running, so a call that overlaps Close is seen by one of them.
	stateLock sync.Mutex
	running   int
	closing   bool
	// afterClosing, if set, runs in Close between setting closing and reading running (self-test only).
	afterClosing func()

	violationsLock sync.Mutex
	violations     []string
}

var errUsedAfterClose = errors.New("guardDB: database used after Close")

func (g *guardDB) violation(what string, allGoroutines bool) {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, allGoroutines)]
	g.violationsLock.Lock()
	g.violations = append(g.violations, what+"\n"+string(buf))
	g.violationsLock.Unlock()
}

func (g *guardDB) enter(op string) error {
	g.lock.RLock()
	if g.closed {
		g.lock.RUnlock()
		g.violation(op+" after Close", false)
		return errUsedAfterClose
	}
	g.stateLock.Lock()
	g.running++
	closing := g.closing
	g.stateLock.Unlock()
	if closing {
		g.violation(op+" while Close was called", false)
	}
	return nil
}

func (g *guardDB) exit() {
	g.stateLock.Lock()
	g.running--
	g.stateLock.Unlock()
	g.lock.RUnlock()
}

func (g *guardDB) slow() {
	if g.writeDelay > 0 {
		time.Sleep(g.writeDelay)
	}
}

func (g *guardDB) Violations() []string {
	g.violationsLock.Lock()
	defer g.violationsLock.Unlock()
	return append([]string(nil), g.violations...)
}

func (g *guardDB) Close() error {
	g.stateLock.Lock()
	g.closing = true
	if g.afterClosing != nil {
		g.afterClosing()
	}
	running := g.running
	g.stateLock.Unlock()
	if running > 0 {
		g.violation(fmt.Sprintf("Close called while %d database call(s) were running; all goroutines:", running), true)
	}
	g.lock.Lock()
	defer g.lock.Unlock()
	g.closed = true
	return g.Database.Close()
}

func (g *guardDB) Put(key *database.Key, value []byte) error {
	if err := g.enter("Put"); err != nil {
		return err
	}
	defer g.exit()
	g.slow()
	return g.Database.Put(key, value)
}

func (g *guardDB) BatchPut(pairs map[*database.Key][]byte) error {
	if err := g.enter("BatchPut"); err != nil {
		return err
	}
	defer g.exit()
	g.slow()
	return g.Database.BatchPut(pairs)
}

func (g *guardDB) Get(key *database.Key) ([]byte, error) {
	if err := g.enter("Get"); err != nil {
		return nil, err
	}
	defer g.exit()
	return g.Database.Get(key)
}

func (g *guardDB) Has(key *database.Key) (bool, error) {
	if err := g.enter("Has"); err != nil {
		return false, err
	}
	defer g.exit()
	return g.Database.Has(key)
}

func (g *guardDB) Delete(key *database.Key) error {
	if err := g.enter("Delete"); err != nil {
		return err
	}
	defer g.exit()
	g.slow()
	return g.Database.Delete(key)
}

func (g *guardDB) Compact() error {
	if err := g.enter("Compact"); err != nil {
		return err
	}
	defer g.exit()
	return g.Database.Compact()
}

func (g *guardDB) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	if err := g.enter("Cursor"); err != nil {
		return nil, err
	}
	defer g.exit()
	cursor, err := g.Database.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	return &guardCursor{Cursor: cursor, g: g}, nil
}

func (g *guardDB) CursorBounds(bucket *database.Bucket, lower, upper []byte) (database.Cursor, error) {
	if err := g.enter("CursorBounds"); err != nil {
		return nil, err
	}
	defer g.exit()
	opener, ok := g.Database.(database.BoundCursorOpener)
	if !ok {
		return nil, errors.New("guardDB: the database cannot open bounded cursors")
	}
	cursor, err := opener.CursorBounds(bucket, lower, upper)
	if err != nil {
		return nil, err
	}
	return &guardCursor{Cursor: cursor, g: g}, nil
}

func (g *guardDB) Begin() (database.Transaction, error) {
	if err := g.enter("Begin"); err != nil {
		return nil, err
	}
	defer g.exit()
	tx, err := g.Database.Begin()
	if err != nil {
		return nil, err
	}
	return &guardTransaction{Transaction: tx, g: g}, nil
}

func (g *guardDB) BeginUnindexed() (database.Transaction, error) {
	if err := g.enter("BeginUnindexed"); err != nil {
		return nil, err
	}
	defer g.exit()
	tx, err := g.Database.BeginUnindexed()
	if err != nil {
		return nil, err
	}
	return &guardTransaction{Transaction: tx, g: g}, nil
}

type guardTransaction struct {
	database.Transaction
	g *guardDB
}

func (tx *guardTransaction) Put(key *database.Key, value []byte) error {
	if err := tx.g.enter("Transaction.Put"); err != nil {
		return err
	}
	defer tx.g.exit()
	return tx.Transaction.Put(key, value)
}

func (tx *guardTransaction) BatchPut(pairs map[*database.Key][]byte) error {
	if err := tx.g.enter("Transaction.BatchPut"); err != nil {
		return err
	}
	defer tx.g.exit()
	return tx.Transaction.BatchPut(pairs)
}

func (tx *guardTransaction) Get(key *database.Key) ([]byte, error) {
	if err := tx.g.enter("Transaction.Get"); err != nil {
		return nil, err
	}
	defer tx.g.exit()
	return tx.Transaction.Get(key)
}

func (tx *guardTransaction) Has(key *database.Key) (bool, error) {
	if err := tx.g.enter("Transaction.Has"); err != nil {
		return false, err
	}
	defer tx.g.exit()
	return tx.Transaction.Has(key)
}

func (tx *guardTransaction) Delete(key *database.Key) error {
	if err := tx.g.enter("Transaction.Delete"); err != nil {
		return err
	}
	defer tx.g.exit()
	return tx.Transaction.Delete(key)
}

func (tx *guardTransaction) Cursor(bucket *database.Bucket) (database.Cursor, error) {
	if err := tx.g.enter("Transaction.Cursor"); err != nil {
		return nil, err
	}
	defer tx.g.exit()
	cursor, err := tx.Transaction.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	return &guardCursor{Cursor: cursor, g: tx.g}, nil
}

func (tx *guardTransaction) CursorBounds(bucket *database.Bucket, lower, upper []byte) (database.Cursor, error) {
	if err := tx.g.enter("Transaction.CursorBounds"); err != nil {
		return nil, err
	}
	defer tx.g.exit()
	opener, ok := tx.Transaction.(database.BoundCursorOpener)
	if !ok {
		return nil, errors.New("guardDB: the transaction cannot open bounded cursors")
	}
	cursor, err := opener.CursorBounds(bucket, lower, upper)
	if err != nil {
		return nil, err
	}
	return &guardCursor{Cursor: cursor, g: tx.g}, nil
}

func (tx *guardTransaction) Commit() error {
	if err := tx.g.enter("Transaction.Commit"); err != nil {
		return err
	}
	defer tx.g.exit()
	tx.g.slow()
	return tx.Transaction.Commit()
}

func (tx *guardTransaction) Rollback() error {
	if err := tx.g.enter("Transaction.Rollback"); err != nil {
		return err
	}
	defer tx.g.exit()
	return tx.Transaction.Rollback()
}

func (tx *guardTransaction) RollbackUnlessClosed() error {
	if err := tx.g.enter("Transaction.RollbackUnlessClosed"); err != nil {
		return err
	}
	defer tx.g.exit()
	return tx.Transaction.RollbackUnlessClosed()
}

type guardCursor struct {
	database.Cursor
	g *guardDB
}

func (c *guardCursor) Next() bool {
	if c.g.enter("Cursor.Next") != nil {
		return false
	}
	defer c.g.exit()
	return c.Cursor.Next()
}

func (c *guardCursor) First() bool {
	if c.g.enter("Cursor.First") != nil {
		return false
	}
	defer c.g.exit()
	return c.Cursor.First()
}

func (c *guardCursor) Seek(key *database.Key) error {
	if err := c.g.enter("Cursor.Seek"); err != nil {
		return err
	}
	defer c.g.exit()
	return c.Cursor.Seek(key)
}

func (c *guardCursor) SeekFullKey(key []byte) error {
	if err := c.g.enter("Cursor.SeekFullKey"); err != nil {
		return err
	}
	defer c.g.exit()
	return c.Cursor.SeekFullKey(key)
}

func (c *guardCursor) Key() (*database.Key, error) {
	if err := c.g.enter("Cursor.Key"); err != nil {
		return nil, err
	}
	defer c.g.exit()
	return c.Cursor.Key()
}

func (c *guardCursor) FullKey() ([]byte, error) {
	if err := c.g.enter("Cursor.FullKey"); err != nil {
		return nil, err
	}
	defer c.g.exit()
	return c.Cursor.FullKey()
}

func (c *guardCursor) Value() ([]byte, error) {
	if err := c.g.enter("Cursor.Value"); err != nil {
		return nil, err
	}
	defer c.g.exit()
	return c.Cursor.Value()
}

func (c *guardCursor) Close() error {
	// After the database close, which also closes every open cursor, a user may still close its cursor: that is not a
	// use and is not recorded. Before it, closing a cursor is a use like any other: the database close closes the
	// same cursor, so the two must not overlap.
	c.g.lock.RLock()
	defer c.g.lock.RUnlock()
	if c.g.closed {
		return nil
	}
	c.g.stateLock.Lock()
	c.g.running++
	closing := c.g.closing
	c.g.stateLock.Unlock()
	defer func() {
		c.g.stateLock.Lock()
		c.g.running--
		c.g.stateLock.Unlock()
	}()
	if closing {
		c.g.violation("Cursor.Close while Close was called", false)
	}
	return c.Cursor.Close()
}

// stressConfig is commonConfig without its write to the network parameters that config.DefaultConfig shares between
// configs: a node still shutting down reads them while the next one is configured.
func stressConfig() *config.Config {
	cfg := config.DefaultConfig()
	params := dagconfig.SimnetParams
	params.SkipProofOfWork = true
	params.BlockCoinbaseMaturity = 10
	cfg.ActiveNetParams = &params
	cfg.InputMinAgeDAA = 0
	cfg.TargetOutboundPeers = 0
	cfg.DisableDNSSeed = true
	cfg.Simnet = true
	cfg.AutoUpdateEnabled = config.Bool(false)
	return cfg
}

// stressNode is one in-process node.
type stressNode struct {
	t   *testing.T
	cfg *config.Config
	db  *guardDB
	app *app.ComponentManager
	rpc *testRPCClient

	p2pAddress    string
	miningAddress string
}

type stressNodeOptions struct {
	appDir     string
	utxoIndex  bool
	writeDelay time.Duration
	params     func(cfg *config.Config)
}

func startStressNode(t *testing.T, options stressNodeOptions) *stressNode {
	t.Helper()
	constants.ForceSetBlockVersion(1)
	cfg := stressConfig()
	cfg.AppDir = options.appDir
	if cfg.AppDir == "" {
		cfg.AppDir = randomDirectory(t)
	}
	p2pAddress, rpcAddress := reserveLoopbackAddress(), reserveLoopbackAddress()
	cfg.Listeners = []string{p2pAddress}
	cfg.RPCListeners = []string{rpcAddress}
	cfg.UTXOIndex = options.utxoIndex
	cfg.AllowSubmitBlockWhenNotSynced = true
	if options.params != nil {
		options.params(cfg)
	}

	innerDB, err := openDB(cfg)
	if err != nil {
		t.Fatalf("opening the database: %+v", err)
	}
	db := &guardDB{Database: innerDB, writeDelay: options.writeDelay}
	componentManager, err := app.NewComponentManager(cfg, db, make(chan struct{}))
	if err != nil {
		t.Fatalf("creating the node: %+v", err)
	}
	releaseReservedAddress(p2pAddress)
	releaseReservedAddress(rpcAddress)
	componentManager.Start()
	rpcClient, err := newTestRPCClient(rpcAddress)
	if err != nil {
		t.Fatalf("connecting the RPC client: %+v", err)
	}
	return &stressNode{
		t: t, cfg: cfg, db: db, app: componentManager, rpc: rpcClient,
		p2pAddress: p2pAddress, miningAddress: miningAddress1,
	}
}

// stop does what app.main does: Stop, and close the database only if Stop reports success. It reports whether the
// database was closed.
func (n *stressNode) stop() bool {
	t := n.t
	t.Helper()
	_ = n.rpc.Close()
	start := time.Now()
	stopped := make(chan bool, 1)
	go func() { stopped <- n.app.Stop() }()
	select {
	case ok := <-stopped:
		t.Logf("Stop returned %v after %s", ok, time.Since(start).Round(time.Millisecond))
		if !ok {
			t.Errorf("Stop reported that the node did not shut down cleanly")
			return false
		}
	case <-time.After(stopBound):
		t.Errorf("Stop did not return within %s; node goroutines:\n%s", stopBound, nodeGoroutines())
		return false
	}
	err := n.db.Close()
	if err != nil {
		t.Errorf("closing the database: %+v", err)
	}
	return true
}

// checkNoUseAfterClose waits grace for late users and fails on any database call made during or after Close.
func (n *stressNode) checkNoUseAfterClose(grace time.Duration) {
	n.t.Helper()
	time.Sleep(grace)
	violations := n.db.Violations()
	if len(violations) > 0 {
		n.t.Errorf("the database was used %d time(s) during or after Close; first use:\n%s", len(violations), violations[0])
	}
}

func (n *stressNode) mine(t *testing.T) {
	t.Helper()
	if err := mineWith(n.rpc, n.cfg); err != nil {
		t.Fatalf("mining: %+v", err)
	}
}

func (n *stressNode) connectTo(t *testing.T, other *stressNode) {
	t.Helper()
	err := n.rpc.AddPeer(other.p2pAddress, false)
	if err != nil {
		t.Fatalf("AddPeer: %+v", err)
	}
}

// goroutine snapshots, keyed by the function that created the goroutine. Only goroutines with a node frame count;
// those created by the tests themselves, the RPC client and the net adapters the tests use as peers are not the node's.
var createdByPattern = regexp.MustCompile(`(?m)^created by (\S+)`)

func nodeGoroutineStacks() []string {
	buf := make([]byte, 16<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var stacks []string
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(stack, "HoosatNetwork/HTND/v2/") ||
			strings.Contains(stack, "HTND/v2/testing/integration.") ||
			strings.Contains(stack, "HTND/v2/infrastructure/network/rpcclient") ||
			strings.Contains(stack, "HTND/v2/infrastructure/logger.") {
			continue
		}
		stacks = append(stacks, stack)
	}
	return stacks
}

func nodeGoroutines() string {
	return strings.Join(nodeGoroutineStacks(), "\n\n")
}

func goroutineCounts() map[string]int {
	counts := map[string]int{}
	for _, stack := range nodeGoroutineStacks() {
		key := "?"
		if match := createdByPattern.FindStringSubmatch(stack); match != nil {
			key = match[1]
		}
		counts[key]++
	}
	return counts
}

// checkNoGoroutineLeak fails if, within wait, goroutines created by the node remain beyond the baseline.
func checkNoGoroutineLeak(t *testing.T, baseline map[string]int, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		var leaked []string
		for key, count := range goroutineCounts() {
			if count > baseline[key] {
				leaked = append(leaked, fmt.Sprintf("%d x %s", count-baseline[key], key))
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			sort.Strings(leaked)
			t.Errorf("goroutines outlived the shutdown: %s\n%s", strings.Join(leaked, ", "), nodeGoroutines())
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// restartAndCheck starts a node on the stopped node's appdir and checks that it serves its DAG.
func restartAndCheck(t *testing.T, stopped *stressNode) *stressNode {
	t.Helper()
	restarted := startStressNode(t, stressNodeOptions{appDir: stopped.cfg.AppDir, utxoIndex: stopped.cfg.UTXOIndex,
		params: func(cfg *config.Config) { cfg.ActiveNetParams = stopped.cfg.ActiveNetParams }})
	_, err := restarted.rpc.GetBlockDAGInfo()
	if err != nil {
		t.Fatalf("the restarted node does not serve its DAG: %+v", err)
	}
	return restarted
}

func waitForSameTip(t *testing.T, a, b *stressNode) {
	t.Helper()
	deadline := time.Now().Add(defaultTimeout)
	for time.Now().Before(deadline) {
		tipA, errA := a.rpc.GetSelectedTipHash()
		tipB, errB := b.rpc.GetSelectedTipHash()
		if errA == nil && errB == nil && tipA.SelectedTipHash == tipB.SelectedTipHash {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the nodes did not converge on one selected tip")
}

func TestShutdownStressIdle(t *testing.T) {
	skipUnlessStress(t)
	baseline := goroutineCounts()
	node := startStressNode(t, stressNodeOptions{utxoIndex: true})
	for range 5 {
		node.mine(t)
	}
	before, err := node.rpc.GetSelectedTipHash()
	if err != nil {
		t.Fatal(err)
	}
	if node.stop() {
		node.checkNoUseAfterClose(time.Second)
		checkNoGoroutineLeak(t, baseline, 5*time.Second)
		restarted := restartAndCheck(t, node)
		after, err := restarted.rpc.GetSelectedTipHash()
		if err != nil {
			t.Fatal(err)
		}
		if after.SelectedTipHash != before.SelectedTipHash {
			t.Errorf("the selected tip changed across a clean restart: %s, then %s", before.SelectedTipHash, after.SelectedTipHash)
		}
		restarted.stop()
	}
}

// stopDuringIBD stops one side of an IBD after delay. stopSyncee picks the syncing node, otherwise the serving node.
func stopDuringIBD(t *testing.T, delay time.Duration, stopSyncee bool, writeDelay time.Duration) {
	baseline := goroutineCounts()
	syncer := startStressNode(t, stressNodeOptions{})
	const blocks = 300
	for range blocks {
		syncer.mine(t)
	}
	syncee := startStressNode(t, stressNodeOptions{utxoIndex: true, writeDelay: writeDelay})
	syncee.connectTo(t, syncer)
	time.Sleep(delay)

	if info, err := syncee.rpc.GetBlockDAGInfo(); err == nil {
		t.Logf("stopping with the syncee at %d headers, %d blocks of %d", info.HeaderCount, info.BlockCount, blocks+1)
	}
	stopping, other := syncee, syncer
	if !stopSyncee {
		stopping, other = syncer, syncee
	}
	if !stopping.stop() {
		return
	}
	stopping.checkNoUseAfterClose(time.Second)
	if !other.stop() {
		return
	}
	other.checkNoUseAfterClose(0)
	checkNoGoroutineLeak(t, baseline, 5*time.Second)

	// Both datadirs must open again, and the syncee must finish syncing from what it kept.
	restartedSyncer := restartAndCheck(t, syncer)
	restartedSyncee := restartAndCheck(t, syncee)
	restartedSyncee.connectTo(t, restartedSyncer)
	waitForSameTip(t, restartedSyncer, restartedSyncee)
	restartedSyncee.stop()
	restartedSyncer.stop()
}

var ibdStopDelays = []time.Duration{0, 200 * time.Millisecond, time.Second, 2500 * time.Millisecond, 4 * time.Second, 6 * time.Second}

func TestShutdownStressIBDSyncee(t *testing.T) {
	skipUnlessStress(t)
	for _, delay := range ibdStopDelays {
		t.Run(delay.String(), func(t *testing.T) { stopDuringIBD(t, delay, true, 0) })
	}
}

func TestShutdownStressIBDSyncer(t *testing.T) {
	skipUnlessStress(t)
	for _, delay := range ibdStopDelays {
		t.Run(delay.String(), func(t *testing.T) { stopDuringIBD(t, delay, false, 0) })
	}
}

func TestShutdownStressSlowDB(t *testing.T) {
	skipUnlessStress(t)
	for _, delay := range ibdStopDelays {
		t.Run(delay.String(), func(t *testing.T) { stopDuringIBD(t, delay, true, 2*time.Millisecond) })
	}
}

// TestShutdownStressRPCLoad stops a node while RPC clients keep it busy: block and DAG queries, UTXO index queries
// and mining (GetBlockTemplate and SubmitBlock).
func TestShutdownStressRPCLoad(t *testing.T) {
	skipUnlessStress(t)
	for round := range 5 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			baseline := goroutineCounts()
			node := startStressNode(t, stressNodeOptions{utxoIndex: true})
			var genesis string
			for range 20 {
				node.mine(t)
			}
			info, err := node.rpc.GetBlockDAGInfo()
			if err != nil {
				t.Fatal(err)
			}
			genesis = info.PruningPointHash

			var stopLoad atomic.Bool
			var clients sync.WaitGroup
			for client := range 6 {
				clients.Add(1)
				go func() {
					defer clients.Done()
					rpcClient, err := newTestRPCClient(node.cfg.RPCListeners[0])
					if err != nil {
						return
					}
					defer rpcClient.Close()
					rpcClient.SetTimeout(2 * time.Second)
					for !stopLoad.Load() {
						switch client % 3 {
						case 0:
							_, err = rpcClient.GetBlocks(genesis, true, true)
						case 1:
							_, err = rpcClient.GetUTXOsByAddresses([]string{miningAddress1}, 0)
						case 2:
							err = mineWith(rpcClient, node.cfg)
						}
						if err != nil {
							return
						}
					}
				}()
			}
			time.Sleep(time.Duration(100+round*150) * time.Millisecond)
			stopped := node.stop()
			stopLoad.Store(true)
			clients.Wait()
			if stopped {
				node.checkNoUseAfterClose(time.Second)
				checkNoGoroutineLeak(t, baseline, 5*time.Second)
				restartAndCheck(t, node).stop()
			}
		})
	}
}

func mineWith(rpcClient *testRPCClient, cfg *config.Config) error {
	template, err := rpcClient.GetBlockTemplate(miningAddress1, "integration")
	if err != nil {
		return err
	}
	block, err := appmessage.RPCBlockToDomainBlock(template.Block, "REAL_MAIN_POW_HASH")
	if err != nil {
		return err
	}
	_, powHash := pow.NewState(block.Header.ToMutable()).CalculateProofOfWorkValue()
	block.PoWHash = powHash.String()
	_, err = rpcClient.SubmitBlockAlsoIfNonDAA(block, block.PoWHash)
	return err
}

// TestShutdownStressP2PChurn stops a node while peers keep connecting to it and dropping the connection.
func TestShutdownStressP2PChurn(t *testing.T) {
	skipUnlessStress(t)
	for round := range 5 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			baseline := goroutineCounts()
			node := startStressNode(t, stressNodeOptions{})
			for range 5 {
				node.mine(t)
			}
			var stopChurn atomic.Bool
			var churners sync.WaitGroup
			cfg := stressConfig()
			cfg.Listeners, cfg.RPCListeners = nil, nil
			for range 4 {
				churners.Add(1)
				go func() {
					defer churners.Done()
					for !stopChurn.Load() {
						peer, err := netadapter.NewNetAdapter(cfg)
						if err != nil {
							return
						}
						peer.SetP2PRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
						peer.SetRPCRouterInitializer(func(*router.Router, *netadapter.NetConnection) {})
						if peer.Start() != nil {
							return
						}
						_ = peer.P2PConnect(node.p2pAddress)
						time.Sleep(time.Duration(round*5) * time.Millisecond)
						_ = peer.Stop()
					}
				}()
			}
			time.Sleep(time.Duration(200+round*100) * time.Millisecond)
			stopped := node.stop()
			stopChurn.Store(true)
			churners.Wait()
			if stopped {
				node.checkNoUseAfterClose(time.Second)
				checkNoGoroutineLeak(t, baseline, 5*time.Second)
				restartAndCheck(t, node).stop()
			}
		})
	}
}

// TestShutdownStressDNSSeedLate stops a node while its DNS seeding lookup is still running, and lets the lookup
// return after the database was closed.
func TestShutdownStressDNSSeedLate(t *testing.T) {
	skipUnlessStress(t)
	baseline := goroutineCounts()
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	var startedOnce sync.Once
	node := startStressNode(t, stressNodeOptions{params: func(cfg *config.Config) {
		cfg.DisableDNSSeed = false
		cfg.DNSSeed = "seed.invalid"
		cfg.TargetOutboundPeers = 1
		cfg.Lookup = func(string) ([]net.IP, error) {
			startedOnce.Do(func() { close(lookupStarted) })
			<-releaseLookup
			return []net.IP{net.IPv4(44, 1, 2, 3)}, nil
		}
	}})
	select {
	case <-lookupStarted:
	case <-time.After(defaultTimeout):
		t.Fatal("the node did not start DNS seeding")
	}
	if !node.stop() {
		return
	}
	close(releaseLookup)
	waitForGoroutineToEnd(t, "dnsseed.SeedFromDNS")
	node.checkNoUseAfterClose(0)
	checkNoGoroutineLeak(t, baseline, 5*time.Second)
	restartAndCheck(t, node).stop()
}

func waitForGoroutineToEnd(t *testing.T, function string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(nodeGoroutines(), function) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s is still running", function)
}

// TestShutdownStressPruningPointMove stops a node at different points while it mines on parameters small enough
// that its pruning point moves every few blocks, then restarts it on what is durable.
func TestShutdownStressPruningPointMove(t *testing.T) {
	skipUnlessStress(t)
	smallPruning := func(cfg *config.Config) {
		params := *cfg.ActiveNetParams
		params.K = []externalapi.KType{0}
		params.FinalityDuration = []time.Duration{30 * params.TargetTimePerBlock[0]}
		params.PruningProofM = 1
		params.DifficultyAdjustmentWindowSize = []int{10}
		cfg.ActiveNetParams = &params
	}
	for _, stopAfter := range []int{70, 95, 110, 125, 140} {
		t.Run(fmt.Sprint(stopAfter), func(t *testing.T) {
			baseline := goroutineCounts()
			node := startStressNode(t, stressNodeOptions{utxoIndex: true, writeDelay: 200 * time.Microsecond, params: smallPruning})
			var mined atomic.Int32
			miningDone := make(chan struct{})
			go func() {
				defer close(miningDone)
				for range stopAfter + 30 {
					if mineWith(node.rpc, node.cfg) != nil {
						return
					}
					mined.Add(1)
				}
			}()
			for mined.Load() < int32(stopAfter) {
				select {
				case <-miningDone:
					t.Fatalf("mining stopped after %d blocks", mined.Load())
				case <-time.After(5 * time.Millisecond):
				}
			}
			info, err := node.rpc.GetBlockDAGInfo()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("stopping after %d blocks; pruning point %s", mined.Load(), info.PruningPointHash)
			if !node.stop() {
				return
			}
			<-miningDone
			node.checkNoUseAfterClose(time.Second)
			checkNoGoroutineLeak(t, baseline, 5*time.Second)
			restarted := restartAndCheck(t, node)
			restarted.mine(t)
			restarted.stop()
		})
	}
}

// blockingPutDB holds Put until released.
type blockingPutDB struct {
	database.Database
	entered, release chan struct{}
}

func (db *blockingPutDB) Put(*database.Key, []byte) error {
	close(db.entered)
	<-db.release
	return nil
}

func (db *blockingPutDB) Close() error { return nil }

// blockingCloseCursor holds Close until released.
type blockingCloseCursor struct {
	database.Cursor
	entered, release chan struct{}
}

func (c *blockingCloseCursor) Close() error {
	close(c.entered)
	<-c.release
	return nil
}

// waitForViolations waits until g has recorded n violations, so that a held call is released only after Close has
// seen it.
func waitForViolations(t *testing.T, g *guardDB, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(g.Violations()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d violation(s) recorded, expected %d", len(g.Violations()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestShutdownStressGuardDB checks the harness's own guard: a call still running when Close is called, a cursor
// close still running when Close is called, a call that finishes while Close is being called, and a call made after
// Close, must all be recorded; a cursor closed after Close must not be.
func TestShutdownStressGuardDB(t *testing.T) {
	skipUnlessStress(t)
	inner := &blockingPutDB{entered: make(chan struct{}), release: make(chan struct{})}
	g := &guardDB{Database: inner}
	putDone := make(chan struct{})
	go func() {
		defer close(putDone)
		_ = g.Put(nil, nil)
	}()
	<-inner.entered
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		_ = g.Close()
	}()
	waitForViolations(t, g, 1)
	close(inner.release)
	<-putDone
	<-closeDone
	if len(g.Violations()) != 1 {
		t.Fatalf("a Put running when Close was called was not recorded once (%d violations)", len(g.Violations()))
	}
	if err := g.Put(nil, nil); !errors.Is(err, errUsedAfterClose) || len(g.Violations()) != 2 {
		t.Fatalf("a Put after Close was not refused and recorded: %v, %d violations", err, len(g.Violations()))
	}

	g = &guardDB{Database: &blockingPutDB{}}
	cursor := &blockingCloseCursor{entered: make(chan struct{}), release: make(chan struct{})}
	cursorDone := make(chan struct{})
	go func() {
		defer close(cursorDone)
		_ = (&guardCursor{Cursor: cursor, g: g}).Close()
	}()
	<-cursor.entered
	closeDone = make(chan struct{})
	go func() {
		defer close(closeDone)
		_ = g.Close()
	}()
	waitForViolations(t, g, 1)
	close(cursor.release)
	<-cursorDone
	<-closeDone
	if len(g.Violations()) != 1 {
		t.Fatalf("a cursor close running when Close was called was not recorded once (%d violations)", len(g.Violations()))
	}
	if err := (&guardCursor{Cursor: &blockingCloseCursor{}, g: g}).Close(); err != nil || len(g.Violations()) != 1 {
		t.Fatalf("a cursor closed after Close was not a no-op: %v, %d violations", err, len(g.Violations()))
	}

	inner = &blockingPutDB{entered: make(chan struct{}), release: make(chan struct{})}
	g = &guardDB{Database: inner}
	putDone = make(chan struct{})
	go func() {
		defer close(putDone)
		_ = g.Put(nil, nil)
	}()
	<-inner.entered
	// The Put is released after Close has set its flag and before it counts running calls, and is given time to
	// finish there.
	g.afterClosing = func() {
		close(inner.release)
		select {
		case <-putDone:
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = g.Close()
	<-putDone
	if len(g.Violations()) != 1 {
		t.Fatalf("a Put that finished while Close was being called was not recorded once (%d violations)", len(g.Violations()))
	}
}
