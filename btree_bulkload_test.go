package btree

import (
	"testing"
)

// TestBulkLoadInvariantsG exhaustively checks that bulkLoadWith produces a
// valid B-tree (ordering, separator bounds, min/max fill, child counts) for
// many sizes and degrees, including the awkward sizes just around node and
// level boundaries.
func TestBulkLoadInvariantsG(t *testing.T) {
	for _, degree := range []int{2, 3, 4, 8, 16, 32} {
		tr := NewOrderedG[int](degree)
		maxItems := tr.maxItems()
		minItems := tr.minItems()
		for size := 1; size <= 5000; size++ {
			sorted := make(items[int], 0, size)
			for i := 0; i < size; i++ {
				sorted = append(sorted, i)
			}
			privateCow := &copyOnWriteContext[int]{
				freelist: NewFreeListG[int](0),
				less:     tr.cow.less,
			}
			root := tr.bulkLoadWith(sorted, privateCow)
			tr.reassignCow(root, tr.cow, privateCow)

			var total int
			var check func(n *node[int], lo, hi *int, isRoot bool)
			check = func(n *node[int], lo, hi *int, isRoot bool) {
				if len(n.items) > maxItems {
					t.Fatalf("degree=%d size=%d: overfull node %d", degree, size, len(n.items))
				}
				if !isRoot && len(n.children) > 0 && len(n.items) < minItems {
					t.Fatalf("degree=%d size=%d: underfull internal node %d (min %d)",
						degree, size, len(n.items), minItems)
				}
				if len(n.children) > 0 && len(n.children) != len(n.items)+1 {
					t.Fatalf("degree=%d size=%d: children %d != items+1 (%d)",
						degree, size, len(n.children), len(n.items)+1)
				}
				for i, it := range n.items {
					if lo != nil && it <= *lo {
						t.Fatalf("degree=%d size=%d: item %d not > bound %d", degree, size, it, *lo)
					}
					if hi != nil && it >= *hi {
						t.Fatalf("degree=%d size=%d: item %d not < bound %d", degree, size, it, *hi)
					}
					if i > 0 && it <= n.items[i-1] {
						t.Fatalf("degree=%d size=%d: unordered node items", degree, size)
					}
				}
				total += len(n.items)
				for ci, child := range n.children {
					var childLo, childHi *int
					if ci > 0 {
						v := n.items[ci-1]
						childLo = &v
					} else {
						childLo = lo
					}
					if ci < len(n.items) {
						v := n.items[ci]
						childHi = &v
					} else {
						childHi = hi
					}
					check(child, childLo, childHi, false)
				}
			}
			check(root, nil, nil, true)
			if total != size {
				t.Fatalf("degree=%d size=%d: tree holds %d keys", degree, size, total)
			}

			// The built tree must support the full insert/delete lifecycle.
			probe := NewOrderedG[int](degree)
			probe.root = root
			probe.length = size
			probe.deletesSinceCompact = 0
			for i := 0; i < size; i += 7 {
				if _, ok := probe.Delete(i); !ok {
					t.Fatalf("degree=%d size=%d: post-load Delete(%d) failed", degree, size, i)
				}
			}
			for i := size; i < size+100; i++ {
				probe.ReplaceOrInsert(i)
			}
			got := 0
			prev := -1
			probe.Ascend(func(v int) bool {
				if v <= prev {
					t.Fatalf("degree=%d size=%d: iteration out of order", degree, size)
				}
				prev = v
				got++
				return true
			})
			want := size - countMultiples(size, 7) + 100
			if got != want {
				t.Fatalf("degree=%d size=%d: after lifecycle got %d want %d", degree, size, got, want)
			}
		}
	}
}

func countMultiples(size, step int) int {
	count := 0
	for i := 0; i < size; i += step {
		count++
	}
	return count
}
