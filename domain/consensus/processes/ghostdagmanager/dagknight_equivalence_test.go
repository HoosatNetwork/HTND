package ghostdagmanager

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/lrucache"
	"github.com/pkg/errors"
)

// dkTestDAG is an in-memory block DAG with GHOSTDAG-shaped data (a selected parent per block and a
// blue score that grows along every parent edge), serving as the GHOSTDAG data store, the DAG
// topology manager and the DAG traversal manager for the DAGKnight equivalence tests. Only the
// methods DAGKnight uses are implemented; the embedded interfaces make any other call panic.
type dkTestDAG struct {
	model.GHOSTDAGDataStore
	model.DAGTopologyManager
	model.DAGTraversalManager

	genesis  *externalapi.DomainHash
	blocks   []*externalapi.DomainHash
	parents  map[externalapi.DomainHash][]*externalapi.DomainHash
	children map[externalapi.DomainHash][]*externalapi.DomainHash
	gd       map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData
	rng      *rand.Rand
	nextID   uint64

	gets int
}

func newDKTestDAG(seed int64) *dkTestDAG {
	d := &dkTestDAG{
		parents:  make(map[externalapi.DomainHash][]*externalapi.DomainHash),
		children: make(map[externalapi.DomainHash][]*externalapi.DomainHash),
		gd:       make(map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData),
		rng:      rand.New(rand.NewSource(seed)),
	}
	d.genesis = d.newHash()
	d.gd[*d.genesis] = externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), model.VirtualGenesisBlockHash, nil, nil, nil, 0)
	d.blocks = append(d.blocks, d.genesis)
	return d
}

// newHash returns a unique hash with random leading bytes, so hash order (used for tie-breaks all
// over DAGKnight) is unrelated to creation order.
func (d *dkTestDAG) newHash() *externalapi.DomainHash {
	var b [externalapi.DomainHashSize]byte
	d.rng.Read(b[:24])
	d.nextID++
	binary.BigEndian.PutUint64(b[24:], d.nextID)
	return externalapi.NewDomainHashFromByteArray(&b)
}

func (d *dkTestDAG) blueScore(h *externalapi.DomainHash) uint64 { return d.gd[*h].BlueScore() }

// addBlock adds a block with the given (antichain) parents. Its selected parent is the parent with
// the highest blue score, ties going to the larger hash, and its blue score exceeds every parent's.
func (d *dkTestDAG) addBlock(parents ...*externalapi.DomainHash) *externalapi.DomainHash {
	h := d.newHash()
	selected := parents[0]
	for _, p := range parents[1:] {
		if d.blueScore(p) > d.blueScore(selected) || (d.blueScore(p) == d.blueScore(selected) && selected.Less(p)) {
			selected = p
		}
	}
	score := d.blueScore(selected) + uint64(len(parents))
	d.gd[*h] = externalapi.NewBlockGHOSTDAGData(score, big.NewInt(int64(score)), selected, nil, nil, nil, 0)
	d.parents[*h] = append([]*externalapi.DomainHash(nil), parents...)
	for _, p := range parents {
		d.children[*p] = append(d.children[*p], h)
	}
	d.blocks = append(d.blocks, h)
	return h
}

func (d *dkTestDAG) tips() []*externalapi.DomainHash {
	var tips []*externalapi.DomainHash
	for _, b := range d.blocks {
		if len(d.children[*b]) == 0 {
			tips = append(tips, b)
		}
	}
	return tips
}

// --- model.GHOSTDAGDataStore

func (d *dkTestDAG) Get(_ model.DBReader, _ *model.StagingArea, h *externalapi.DomainHash, _ bool) (*externalapi.BlockGHOSTDAGData, error) {
	d.gets++
	gd, ok := d.gd[*h]
	if !ok {
		return nil, errors.Errorf("no GHOSTDAG data for %s", h)
	}
	return gd, nil
}

// --- model.DAGTopologyManager

func (d *dkTestDAG) Parents(_ *model.StagingArea, h *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	return d.parents[*h], nil
}

func (d *dkTestDAG) Children(_ *model.StagingArea, h *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	return d.children[*h], nil
}

// IsAncestorOf is reflexive, like the reachability-backed original. Blue scores grow along every
// parent edge here, so the search below b never needs to go under a's blue score.
func (d *dkTestDAG) IsAncestorOf(_ *model.StagingArea, a, b *externalapi.DomainHash) (bool, error) {
	return d.isAncestorOf(a, b), nil
}

func (d *dkTestDAG) isAncestorOf(a, b *externalapi.DomainHash) bool {
	if a.Equal(b) {
		return true
	}
	if _, ok := d.gd[*a]; !ok {
		return false
	}
	if _, ok := d.gd[*b]; !ok {
		return false
	}
	floor := d.blueScore(a)
	visited := map[externalapi.DomainHash]struct{}{*b: {}}
	stack := []*externalapi.DomainHash{b}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range d.parents[*cur] {
			if p.Equal(a) {
				return true
			}
			if _, ok := visited[*p]; ok || d.blueScore(p) <= floor {
				continue
			}
			visited[*p] = struct{}{}
			stack = append(stack, p)
		}
	}
	return false
}

// --- model.DAGTraversalManager: the same traversal as dagTraversalManager.AnticoneFromBlocks.

func (d *dkTestDAG) AnticoneFromBlocks(_ *model.StagingArea, tips []*externalapi.DomainHash, blockHash *externalapi.DomainHash, _ uint64) ([]*externalapi.DomainHash, error) {
	anticone := []*externalapi.DomainHash{}
	queue := append([]*externalapi.DomainHash(nil), tips...)
	visited := make(map[externalapi.DomainHash]struct{})
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := visited[*current]; ok {
			continue
		}
		visited[*current] = struct{}{}
		if d.isAncestorOf(current, blockHash) {
			continue
		}
		if !d.isAncestorOf(blockHash, current) {
			anticone = append(anticone, current)
		}
		queue = append(queue, d.parents[*current]...)
	}
	return anticone, nil
}

func (d *dkTestDAG) manager() *ghostdagManager {
	return &ghostdagManager{
		dagTopologyManager:  d,
		dagTraversalManager: d,
		ghostdagDataStore:   d,
		umcVotingCache:      lrucache.New[int](500, true),
	}
}

type dkDAGShape struct {
	chainLen     int     // blocks on the common chain below the forks
	branches     int     // forks grown off the recent part of the DAG
	maxDepth     int     // longest fork, in blocks
	window       int     // how far back from the newest blocks a fork may start
	mergePercent int     // chance that a fork block also merges another block
	nested       float64 // chance a fork starts off another fork rather than the common chain
}

// randomDKDAG builds a long common chain and then grows forks off its most recent blocks (or off
// earlier forks), optionally merging across forks, which leaves many tips that share long
// selected-parent chains but split at varying depths.
func randomDKDAG(seed int64, s dkDAGShape) *dkTestDAG {
	d := newDKTestDAG(seed)
	chain := []*externalapi.DomainHash{d.genesis}
	for i := 0; i < s.chainLen; i++ {
		chain = append(chain, d.addBlock(chain[len(chain)-1]))
	}
	recent := append([]*externalapi.DomainHash(nil), chain[max(0, len(chain)-s.window):]...)
	for b := 0; b < s.branches; b++ {
		base := recent[d.rng.Intn(len(recent))]
		if d.rng.Float64() >= s.nested {
			base = chain[len(chain)-1-d.rng.Intn(min(s.window, len(chain)))]
		}
		cur := base
		depth := 1 + d.rng.Intn(s.maxDepth)
		for i := 0; i < depth; i++ {
			parents := []*externalapi.DomainHash{cur}
			if d.rng.Intn(100) < s.mergePercent {
				other := recent[d.rng.Intn(len(recent))]
				if !d.isAncestorOf(other, cur) && !d.isAncestorOf(cur, other) {
					parents = append(parents, other)
				}
			}
			cur = d.addBlock(parents...)
			recent = append(recent, cur)
		}
	}
	return d
}

func hashesEqual(a, b []*externalapi.DomainHash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func errorsMatch(a, b error) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || a.Error() == b.Error()
}

// tipOrder reproduces consensusStateManager.tipsInDecreasingDAGKnightOrder from an OrderDAG ordering.
func tipOrder(tips, ordering []*externalapi.DomainHash) []*externalapi.DomainHash {
	pos := make(map[externalapi.DomainHash]int)
	for i, h := range ordering {
		pos[*h] = i
	}
	out := append([]*externalapi.DomainHash(nil), tips...)
	sort.Slice(out, func(i, j int) bool { return pos[*out[i]] < pos[*out[j]] })
	return out
}

func randomSubset(rng *rand.Rand, from []*externalapi.DomainHash, n int) []*externalapi.DomainHash {
	idx := rng.Perm(len(from))
	out := make([]*externalapi.DomainHash, 0, n)
	for _, i := range idx[:min(n, len(from))] {
		out = append(out, from[i])
	}
	return out
}

// TestDAGKnightTipOrderingMatchesLegacy is the consensus-critical check: for random DAGs with many
// tips and forks of varying depth, OrderDAG over the tips - what ResolveVirtual feeds it - must
// return exactly the selected tip and ordering the pre-fix implementation returned, and therefore
// the same tip order. It also pins the LCA and partition primitives on random inputs.
func TestDAGKnightTipOrderingMatchesLegacy(t *testing.T) {
	const cases = 400
	for seed := int64(1); seed <= cases; seed++ {
		rng := rand.New(rand.NewSource(seed * 7919))
		shape := dkDAGShape{
			chainLen:     5 + rng.Intn(300),
			branches:     1 + rng.Intn(40),
			maxDepth:     1 + rng.Intn(60),
			window:       1 + rng.Intn(80),
			mergePercent: rng.Intn(50),
			nested:       rng.Float64(),
		}
		d := randomDKDAG(seed, shape)
		tips := d.tips()
		newGM, oldGM := d.manager(), d.manager()

		newTip, newOrdering, newErr := newGM.OrderDAG(nil, tips)
		oldTip, oldOrdering, oldErr := oldGM.legacyOrderDAG(nil, tips)
		if !errorsMatch(newErr, oldErr) {
			t.Fatalf("seed %d %+v: OrderDAG error %v, legacy %v", seed, shape, newErr, oldErr)
		}
		if !newTip.Equal(oldTip) || !hashesEqual(newOrdering, oldOrdering) {
			t.Fatalf("seed %d %+v (%d tips): OrderDAG selected %s (ordering of %d), legacy %s (ordering of %d)",
				seed, shape, len(tips), newTip, len(newOrdering), oldTip, len(oldOrdering))
		}
		if !hashesEqual(tipOrder(tips, newOrdering), tipOrder(tips, oldOrdering)) {
			t.Fatalf("seed %d: tip order differs", seed)
		}

		// LCA of random block sets, including blocks on each other's chains and the genesis.
		for i := 0; i < 30; i++ {
			P := randomSubset(rng, d.blocks, 1+rng.Intn(6))
			newLCA, newErr := newGM.latestCommonChainAncestor(nil, P, nil, newDAGKnightMemo())
			oldLCA, oldErr := oldGM.legacylatestCommonChainAncestor(nil, P, nil)
			if !errorsMatch(newErr, oldErr) || !newLCA.Equal(oldLCA) {
				t.Fatalf("seed %d: LCA(%v) = %s (%v), legacy %s (%v)", seed, P, newLCA, newErr, oldLCA, oldErr)
			}
		}

		// Partitioning of random tip sets around their LCA.
		for i := 0; i < 5 && len(tips) > 1; i++ {
			P := randomSubset(rng, tips, 2+rng.Intn(len(tips)-1))
			memo := newDAGKnightMemo()
			g, err := newGM.latestCommonChainAncestor(nil, P, tips, memo)
			if err != nil {
				t.Fatalf("seed %d: LCA: %v", seed, err)
			}
			futureG, err := newGM.getFuture(nil, g, tips)
			if err != nil {
				t.Fatalf("seed %d: getFuture: %v", seed, err)
			}
			newParts := newGM.partitionByLCAFuture(nil, P, futureG, memo)
			oldParts, err := oldGM.legacypartitionByLCAFuture(nil, P, g, tips)
			if err != nil {
				t.Fatalf("seed %d: legacy partition: %v", seed, err)
			}
			if len(newParts) != len(oldParts) {
				t.Fatalf("seed %d: %d partitions, legacy %d", seed, len(newParts), len(oldParts))
			}
			for j := range newParts {
				if !hashesEqual(newParts[j], oldParts[j]) {
					t.Fatalf("seed %d: partition %d differs", seed, j)
				}
			}
		}
	}
}

// TestDAGKnightSubDAGProceduresMatchLegacy runs OrderDAG, CalculateRank, KColouring and TieBreaking
// on small DAGs with arbitrary G (not just tips), where the recursive paths - KColouring's agrees()
// and conditioning, UMC voting on non-trivial sets - actually execute. The DAGs are kept small
// because the procedures themselves are exponential in |G|, before and after the fix.
func TestDAGKnightSubDAGProceduresMatchLegacy(t *testing.T) {
	const cases = 300
	for seed := int64(1); seed <= cases; seed++ {
		rng := rand.New(rand.NewSource(seed * 104729))
		d := randomDKDAG(seed, dkDAGShape{
			chainLen:     2 + rng.Intn(12),
			branches:     1 + rng.Intn(4),
			maxDepth:     1 + rng.Intn(3),
			window:       1 + rng.Intn(4),
			mergePercent: rng.Intn(60),
			nested:       rng.Float64(),
		})
		// G: the most recent blocks (a past-closed-above window, as when ordering a sub-DAG).
		n := 3 + rng.Intn(min(8, len(d.blocks)-2))
		G := append([]*externalapi.DomainHash(nil), d.blocks[len(d.blocks)-n:]...)
		newGM, oldGM := d.manager(), d.manager()

		newTip, newOrdering, newErr := newGM.OrderDAG(nil, G)
		oldTip, oldOrdering, oldErr := oldGM.legacyOrderDAG(nil, G)
		if !errorsMatch(newErr, oldErr) || !newTip.Equal(oldTip) || !hashesEqual(newOrdering, oldOrdering) {
			t.Fatalf("seed %d: OrderDAG(G of %d) = %s/%d (%v), legacy %s/%d (%v)",
				seed, len(G), newTip, len(newOrdering), newErr, oldTip, len(oldOrdering), oldErr)
		}

		for i := 0; i < 3; i++ {
			P := randomSubset(rng, G, 1+rng.Intn(3))
			newRank, newErr := newGM.CalculateRank(nil, P, G)
			oldRank, oldErr := oldGM.legacyCalculateRank(nil, P, G)
			if !errorsMatch(newErr, oldErr) || newRank != oldRank {
				t.Fatalf("seed %d: CalculateRank = %d (%v), legacy %d (%v)", seed, newRank, newErr, oldRank, oldErr)
			}

			C := G[rng.Intn(len(G))]
			k := rng.Intn(5)
			free := rng.Intn(2) == 0
			var cond *externalapi.DomainHash
			if rng.Intn(2) == 0 {
				cond = d.blocks[rng.Intn(len(d.blocks))]
			}
			newRes, newErr := newGM.KColouring(nil, C, G, k, free, cond)
			oldRes, oldErr := oldGM.legacyKColouring(nil, C, G, k, free, cond)
			if !errorsMatch(newErr, oldErr) || !hashesEqual(newRes.Blues, oldRes.Blues) || !hashesEqual(newRes.Chain, oldRes.Chain) {
				t.Fatalf("seed %d: KColouring(k=%d free=%v cond=%v) differs", seed, k, free, cond != nil)
			}

			Ps := randomSubset(rng, G, 1+rng.Intn(3))
			newWin, newErr := newGM.TieBreaking(nil, G, Ps, k)
			oldWin, oldErr := oldGM.legacyTieBreaking(nil, G, Ps, k)
			if !errorsMatch(newErr, oldErr) || !newWin.Equal(oldWin) {
				t.Fatalf("seed %d: TieBreaking = %s (%v), legacy %s (%v)", seed, newWin, newErr, oldWin, oldErr)
			}
		}
	}
}

// TestLCAReadsScaleWithForkDepthNotChainLength pins the complexity fix itself: two tips forking 10
// blocks below the tip of a 50,000 block chain must be resolved by reading about 20 blocks' GHOSTDAG
// data, not 100,000.
func TestLCAReadsScaleWithForkDepthNotChainLength(t *testing.T) {
	d := newDKTestDAG(1)
	cur := d.genesis
	for i := 0; i < 50_000; i++ {
		cur = d.addBlock(cur)
	}
	a, b := cur, cur
	for i := 0; i < 10; i++ {
		a = d.addBlock(a)
		b = d.addBlock(b)
	}
	gm := d.manager()
	d.gets = 0
	lca, err := gm.latestCommonChainAncestor(nil, []*externalapi.DomainHash{a, b}, nil, newDAGKnightMemo())
	if err != nil || !lca.Equal(cur) {
		t.Fatalf("LCA = %s (%v), want %s", lca, err, cur)
	}
	if d.gets > 30 {
		t.Fatalf("LCA of a 10-deep fork read %d blocks' GHOSTDAG data", d.gets)
	}
}

func benchmarkOrderDAG(b *testing.B, tips int, legacy bool) {
	d := randomDKDAG(42, dkDAGShape{chainLen: 20_000, branches: tips, maxDepth: 200, window: 100, mergePercent: 0, nested: 0.3})
	G := d.tips()
	gm := d.manager()
	d.gets = 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gm.umcVotingCache = lrucache.New[int](500, true)
		var err error
		if legacy {
			_, _, err = gm.legacyOrderDAG(nil, G)
		} else {
			_, _, err = gm.OrderDAG(nil, G)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(d.gets)/float64(b.N), "ghostdag-reads/op")
	b.ReportMetric(float64(len(G)), "tips")
}

// BenchmarkOrderDAGTips measures OrderDAG over the DAG tips, as ResolveVirtual calls it, on a
// 20,000 block chain with forks up to 200 blocks deep. The testnet chain is an order of magnitude
// longer, and the legacy cost grows linearly with it while the new cost does not.
func BenchmarkOrderDAGTips(b *testing.B) {
	for _, tips := range []int{2, 12, 50} {
		for _, legacy := range []bool{true, false} {
			name := "new"
			if legacy {
				name = "legacy"
			}
			b.Run(fmt.Sprintf("tips=%d/%s", tips, name), func(b *testing.B) { benchmarkOrderDAG(b, tips, legacy) })
		}
	}
}
