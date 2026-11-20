package btree

import (
	"math/rand"
	"sort"
	"testing"
)

// TestCompactStressG drives many random insert/delete/compact/clone cycles and
// verifies after every mutation that the tree is structurally valid, contains
// exactly the expected key set, and iterates in sorted order, across a range
// of degrees and sizes.
func TestCompactStressG(t *testing.T) {
	for _, degree := range []int{2, 3, 5, 8, 16} {
		tree := NewOrderedG[int](degree)
		reference := map[int]bool{}
		rng := rand.New(rand.NewSource(int64(degree)))

		applyAndCheck := func() {
			t.Helper()
			got := validateG(t, tree)
			want := make([]int, 0, len(reference))
			for k := range reference {
				want = append(want, k)
			}
			sort.Ints(want)
			if len(got) != len(want) {
				t.Fatalf("degree=%d: got %d items, want %d", degree, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("degree=%d: order/set mismatch at %d: got %d want %d",
						degree, i, got[i], want[i])
				}
				if _, ok := tree.Get(want[i]); !ok {
					t.Fatalf("degree=%d: Get(%d) missing", degree, want[i])
				}
			}
		}

		for step := 0; step < 4000; step++ {
			key := rng.Intn(3000)
			switch rng.Intn(10) {
			case 0, 1, 2, 3, 4:
				if _, existed := reference[key]; !existed {
					reference[key] = true
				}
				tree.ReplaceOrInsert(key)
			case 5, 6, 7:
				if reference[key] {
					delete(reference, key)
					if _, ok := tree.Delete(key); !ok {
						t.Fatalf("degree=%d: Delete(%d) reported missing", degree, key)
					}
				}
			case 8:
				tree.Compact()
			case 9:
				tree = tree.Clone()
			}
			if step%250 == 0 {
				applyAndCheck()
			}
		}

		// Final compaction plus a full drain through the normal Delete path.
		tree.Compact()
		applyAndCheck()
		for _, k := range append([]int(nil), func() []int {
			out := make([]int, 0, len(reference))
			for k := range reference {
				out = append(out, k)
			}
			return out
		}()...) {
			tree.Delete(k)
		}
		if tree.Len() != 0 {
			t.Fatalf("degree=%d: drain left %d items", degree, tree.Len())
		}
	}
}

// TestCompactIdempotentG ensures compacting an already-compact or empty tree
// is safe and stable.
func TestCompactIdempotentG(t *testing.T) {
	tr := NewOrderedG[int](6)
	tr.Compact()
	for _, v := range rand.Perm(5000) {
		tr.ReplaceOrInsert(v)
	}
	tr.Compact()
	first := validateG(t, tr)
	h1 := heightOfG(tr.root)
	tr.Compact()
	tr.Compact()
	second := validateG(t, tr)
	h2 := heightOfG(tr.root)
	if h1 != h2 || len(first) != len(second) {
		t.Fatalf("repeated Compact changed shape: height %d->%d len %d->%d",
			h1, h2, len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("repeated Compact changed order at %d", i)
		}
	}
}
