package btree

import (
	"math/rand"
	"testing"
)

// BenchmarkGetAfterBulkDeleteG compares point-query latency on a tree that
// just lost most of its items (leaving many half-empty nodes and a taller
// path) against the same tree after Compact, which restores minimum height
// and full nodes. Both hold the identical set of surviving keys.
func BenchmarkGetAfterBulkDeleteG(b *testing.B) {
	const size = 50000
	tr := NewOrderedG[int](8)
	perm := rand.Perm(size)
	for _, v := range perm {
		tr.ReplaceOrInsert(v)
	}
	for _, v := range perm[:size*8/10] {
		tr.Delete(v)
	}

	remaining := make([]int, 0, size/5)
	tr.Ascend(func(v int) bool {
		remaining = append(remaining, v)
		return true
	})

	fragmentedHeight := heightOfG(tr.root)
	tr.Compact()
	compactedHeight := heightOfG(tr.root)
	b.Logf("height fragmented=%d compacted=%d remaining=%d",
		fragmentedHeight, compactedHeight, len(remaining))

	// Pre-build both end states once so the benchmark measures only Get.
	frag := NewOrderedG[int](8)
	comp := NewOrderedG[int](8)
	for _, v := range perm {
		frag.ReplaceOrInsert(v)
		comp.ReplaceOrInsert(v)
	}
	for _, v := range perm[:size*8/10] {
		frag.Delete(v)
		comp.Delete(v)
	}
	comp.Compact()

	b.Run("fragmented", func(b *testing.B) {
		b.ResetTimer()
		idx := 0
		for i := 0; i < b.N; i++ {
			frag.Get(remaining[idx%len(remaining)])
			idx++
		}
	})

	b.Run("compacted", func(b *testing.B) {
		b.ResetTimer()
		idx := 0
		for i := 0; i < b.N; i++ {
			comp.Get(remaining[idx%len(remaining)])
			idx++
		}
	})
}

// BenchmarkCompactG measures the one-time O(n) rebuild cost so its magnitude
// relative to the deletions that triggered it is visible.
func BenchmarkCompactG(b *testing.B) {
	const size = 50000
	perm := rand.Perm(size)
	prototype := NewOrderedG[int](8)
	for _, v := range perm {
		prototype.ReplaceOrInsert(v)
	}
	for _, v := range perm[:size*8/10] {
		prototype.Delete(v)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Clone keeps the fragmented shape while giving an independent tree;
		// compacting it never mutates the shared prototype.
		tr := prototype.Clone()
		tr.Compact()
	}
}

// BenchmarkBulkDeleteG measures the amortized delete path, confirming the
// default configuration adds no per-delete structural maintenance beyond the
// existing underflow handling.
func BenchmarkBulkDeleteG(b *testing.B) {
	const size = 50000
	perm := rand.Perm(size)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		tr := NewOrderedG[int](8)
		for _, v := range perm {
			tr.ReplaceOrInsert(v)
		}
		b.StartTimer()
		for _, v := range perm[:size*8/10] {
			tr.Delete(v)
		}
	}
}
