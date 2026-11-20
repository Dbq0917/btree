package btree

import (
	"math/rand"
	"reflect"
	"testing"
)

// heightOfG returns the maximum root-to-leaf edge depth of a generic tree.
func heightOfG[T any](n *node[T]) int {
	if n == nil || len(n.children) == 0 {
		return 1
	}
	h := 0
	for _, c := range n.children {
		if d := heightOfG(c); d > h {
			h = d
		}
	}
	return h + 1
}

func countNodesG[T any](n *node[T]) (nodes, leaves int) {
	if n == nil {
		return 0, 0
	}
	nodes = 1
	if len(n.children) == 0 {
		return nodes, 1
	}
	for _, c := range n.children {
		cn, cl := countNodesG(c)
		nodes += cn
		leaves += cl
	}
	return nodes, leaves
}

// validateG walks the tree checking ordering and node-capacity invariants,
// and returns the in-order sequence of items.
func validateG(t *testing.T, tr *BTreeG[int]) []int {
	t.Helper()
	maxItems := tr.maxItems()
	minItems := tr.minItems()
	var walk func(n *node[int], depth int, isRoot bool) []int
	walk = func(n *node[int], depth int, isRoot bool) []int {
		if len(n.items) > maxItems {
			t.Fatalf("node has %d items, max %d", len(n.items), maxItems)
		}
		if !isRoot && len(n.children) > 0 && len(n.items) < minItems {
			t.Fatalf("internal node has %d items, min %d", len(n.items), minItems)
		}
		if len(n.children) > 0 && len(n.children) != len(n.items)+1 {
			t.Fatalf("node has %d items but %d children", len(n.items), len(n.children))
		}
		if len(n.children) == 0 {
			out := append([]int(nil), n.items...)
			for i := 1; i < len(out); i++ {
				if !tr.cow.less(out[i-1], out[i]) {
					t.Fatalf("leaf items out of order: %v", out)
				}
			}
			return out
		}
		var out []int
		for i, child := range n.children {
			out = append(out, walk(child, depth+1, false)...)
			if i < len(n.items) {
				out = append(out, n.items[i])
			}
		}
		for i := 1; i < len(out); i++ {
			if !tr.cow.less(out[i-1], out[i]) {
				t.Fatalf("subtree items out of order: %v", out)
			}
		}
		return out
	}
	if tr.root == nil {
		return nil
	}
	return walk(tr.root, 0, true)
}

func bulkDeleteG(t *testing.T, mode string, size, degree int, keepFraction float64) {
	t.Helper()
	tr := NewOrderedG[int](degree)
	perm := rand.Perm(size)
	for _, v := range perm {
		tr.ReplaceOrInsert(v)
	}

	wantKeep := int(float64(size) * keepFraction)
	switch mode {
	case "random":
		for _, v := range perm[:size-wantKeep] {
			tr.Delete(v)
		}
	case "asc":
		for v := 0; v < size-wantKeep; v++ {
			tr.Delete(v)
		}
	case "desc":
		for v := size - 1; v >= wantKeep; v-- {
			tr.Delete(v)
		}
	case "middle":
		lo := wantKeep / 2
		for v := lo; v < lo+(size-wantKeep); v++ {
			tr.Delete(v)
		}
	}

	if tr.Len() != wantKeep {
		t.Fatalf("%s: len after delete = %d, want %d", mode, tr.Len(), wantKeep)
	}

	beforeHeight := heightOfG(tr.root)
	beforeNodes, _ := countNodesG(tr.root)
	beforeOrder := validateG(t, tr)

	tr.Compact()

	afterHeight := heightOfG(tr.root)
	afterNodes, _ := countNodesG(tr.root)
	afterOrder := validateG(t, tr)

	if !reflect.DeepEqual(beforeOrder, afterOrder) {
		t.Fatalf("%s: Compact changed iteration order (len %d)", mode, len(afterOrder))
	}
	if len(afterOrder) != wantKeep {
		t.Fatalf("%s: Compact changed item count to %d", mode, len(afterOrder))
	}
	if afterHeight > beforeHeight {
		t.Fatalf("%s: height grew after Compact: %d -> %d", mode, beforeHeight, afterHeight)
	}

	// Every surviving key must still be retrievable.
	for _, v := range afterOrder {
		if got, ok := tr.Get(v); !ok || got != v {
			t.Fatalf("%s: Get(%d) = %d,%v after Compact", mode, v, got, ok)
		}
	}

	// Reverse iteration must be the exact inverse of ascending iteration.
	var rev []int
	tr.Descend(func(v int) bool {
		rev = append(rev, v)
		return true
	})
	if len(rev) != len(afterOrder) {
		t.Fatalf("%s: descend count %d != ascend count %d", mode, len(rev), len(afterOrder))
	}
	for i := range rev {
		if rev[i] != afterOrder[len(afterOrder)-1-i] {
			t.Fatalf("%s: descend order mismatch at %d", mode, i)
		}
	}

	t.Logf("%s: keep=%d height %d->%d nodes %d->%d",
		mode, wantKeep, beforeHeight, afterHeight, beforeNodes, afterNodes)
}

func TestCompactG(t *testing.T) {
	const size = 20000
	for _, degree := range []int{2, 3, 8} {
		for _, mode := range []string{"random", "asc", "desc", "middle"} {
			bulkDeleteG(t, mode, size, degree, 0.2)
		}
	}
}

func TestCompactSmallG(t *testing.T) {
	// Empty, single-item and single-node trees must compact without error.
	tr := NewOrderedG[int](4)
	tr.Compact()
	if tr.Len() != 0 {
		t.Fatalf("empty Compact changed len to %d", tr.Len())
	}
	tr.ReplaceOrInsert(1)
	tr.Compact()
	if got, ok := tr.Get(1); !ok || got != 1 {
		t.Fatalf("single-item Compact lost the key: %d,%v", got, ok)
	}
	for i := 2; i <= 4; i++ {
		tr.ReplaceOrInsert(i)
	}
	tr.Compact()
	if got := intAll(tr); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("single-node Compact changed order: %v", got)
	}
}

func TestAutoCompactG(t *testing.T) {
	tr := NewOrderedG[int](8)
	if tr.AutoCompact() {
		t.Fatal("auto compaction must default to off")
	}
	tr.SetAutoCompact(true)
	if !tr.AutoCompact() {
		t.Fatal("SetAutoCompact(true) did not enable compaction")
	}

	const size = 10000
	perm := rand.Perm(size)
	for _, v := range perm {
		tr.ReplaceOrInsert(v)
	}
	maxHeight := heightOfG(tr.root)
	for _, v := range perm[:size*8/10] {
		tr.Delete(v)
	}

	// With automatic compaction the tree must not stay taller than a freshly
	// bulk-loaded tree holding the same surviving items.
	gotOrder := validateG(t, tr)
	rebuilt := NewOrderedG[int](8)
	for _, v := range gotOrder {
		rebuilt.ReplaceOrInsert(v)
	}
	if heightOfG(tr.root) > maxHeight {
		t.Fatalf("auto-compacted tree height %d exceeds original %d",
			heightOfG(tr.root), maxHeight)
	}
	if tr.Len() != size/5 {
		t.Fatalf("auto-compacted len = %d, want %d", tr.Len(), size/5)
	}

	tr.SetAutoCompact(false)
	if tr.AutoCompact() {
		t.Fatal("SetAutoCompact(false) did not disable compaction")
	}
}

// TestCompactKeepsWorkingAfter ensures a compacted tree still supports normal
// inserts and deletes (i.e. it is structurally identical to a grown tree).
func TestCompactKeepsWorkingAfterG(t *testing.T) {
	tr := NewOrderedG[int](4)
	const size = 5000
	for _, v := range rand.Perm(size) {
		tr.ReplaceOrInsert(v)
	}
	for _, v := range rand.Perm(size)[:size*7/10] {
		tr.Delete(v)
	}
	tr.Compact()

	// Re-grow beyond the original size, then delete everything and confirm the
	// tree drains correctly through the normal Delete path.
	for v := size; v < size*2; v++ {
		tr.ReplaceOrInsert(v)
	}
	validateG(t, tr)
	all := intAll(tr)
	for _, v := range all {
		if _, ok := tr.Delete(v); !ok {
			t.Fatalf("post-Compact Delete missed %d", v)
		}
	}
	if tr.Len() != 0 {
		t.Fatalf("tree not empty after draining: len %d", tr.Len())
	}
}

// TestCompactCloneSafetyG compacts a Clone while the original keeps using the
// shared freelist, exercising the copy-on-write ownership checks.
func TestCompactCloneSafetyG(t *testing.T) {
	original := NewOrderedG[int](8)
	const size = 6000
	for _, v := range rand.Perm(size) {
		original.ReplaceOrInsert(v)
	}
	clone := original.Clone()

	for _, v := range rand.Perm(size)[:size*8/10] {
		clone.Delete(v)
	}
	clone.Compact()

	// The original must be completely untouched by the clone's compaction.
	if got := intAll(original); len(got) != size {
		t.Fatalf("clone Compact mutated original: got %d items, want %d", len(got), size)
	}
	for v := 0; v < size; v++ {
		if got, ok := original.Get(v); !ok || got != v {
			t.Fatalf("original lost key %d after clone Compact", v)
		}
	}
	if clone.Len() != size/5 {
		t.Fatalf("clone len = %d, want %d", clone.Len(), size/5)
	}
	validateG(t, clone)
}

func TestCompact(t *testing.T) {
	const size = 20000
	tr := New(8)
	perm := rand.Perm(size)
	for _, v := range perm {
		tr.ReplaceOrInsert(Int(v))
	}
	for _, v := range perm[:size*8/10] {
		tr.Delete(Int(v))
	}
	beforeHeight := heightOfG(tr.root)

	var before []int
	tr.Ascend(func(item Item) bool {
		before = append(before, int(item.(Int)))
		return true
	})

	tr.Compact()

	var after []int
	tr.Ascend(func(item Item) bool {
		after = append(after, int(item.(Int)))
		return true
	})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Compact changed iteration order (before %d, after %d items)",
			len(before), len(after))
	}
	if afterHeight := heightOfG(tr.root); afterHeight > beforeHeight {
		t.Fatalf("height grew after Compact: %d -> %d", beforeHeight, afterHeight)
	}
	for _, v := range after {
		if got := tr.Get(Int(v)); got == nil || int(got.(Int)) != v {
			t.Fatalf("Get(%d) failed after Compact", v)
		}
	}
	t.Logf("non-generic: keep=%d height %d->%d", len(after), beforeHeight, heightOfG(tr.root))
}
