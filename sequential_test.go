// Copyright 2022 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build go1.18
// +build go1.18

package btree

import (
	"math"
	"math/rand"
	"testing"
)

const sequentialInsertN = 100000

// leafDepth walks the subtree and reports the min/max leaf depth plus the
// number of leaf nodes and the items they hold. A B-Tree that stays balanced
// has every leaf at the same depth; a degenerate right-spine tree would have
// leaves spread across a wide range of depths.
func leafDepth[T any](n *node[T], depth int, minDepth, maxDepth *int, leafCount, leafItems *int) {
	if len(n.children) == 0 {
		if *leafCount == 0 || depth < *minDepth {
			*minDepth = depth
		}
		if depth > *maxDepth {
			*maxDepth = depth
		}
		*leafCount++
		*leafItems += len(n.items)
		return
	}
	for _, child := range n.children {
		leafDepth(child, depth+1, minDepth, maxDepth, leafCount, leafItems)
	}
}

// TestSequentialInsertStaysBalanced inserts auto-incrementing keys (the
// worst-case access pattern for a structure that only splits). A B-Tree must
// remain balanced purely through splitting: no per-insert rebalancing pass is
// allowed, yet the height must stay logarithmic rather than approaching the
// number of inserted items.
func TestSequentialInsertStaysBalanced(t *testing.T) {
	for _, degree := range []int{2, 4, 16} {
		tree := NewOrderedG[int](degree)
		for i := 0; i < sequentialInsertN; i++ {
			tree.ReplaceOrInsert(i)
		}
		if tree.Len() != sequentialInsertN {
			t.Fatalf("degree %d: Len = %d, want %d", degree, tree.Len(), sequentialInsertN)
		}

		var minDepth, maxDepth, leafCount, leafItems int
		leafDepth(tree.root, 1, &minDepth, &maxDepth, &leafCount, &leafItems)
		if minDepth != maxDepth {
			t.Fatalf("degree %d: leaves span depths [%d, %d], expected a single uniform depth",
				degree, minDepth, maxDepth)
		}

		// Standard B-Tree height bound: h <= 1 + log_degree((n+1)/2). This is
		// logarithmic in n and orders of magnitude smaller than n itself.
		bound := int(math.Ceil(1+math.Log(float64(sequentialInsertN+1)/2)/math.Log(float64(degree)))) + 1
		if maxDepth > bound {
			t.Fatalf("degree %d: height %d exceeds B-Tree bound %d", degree, maxDepth, bound)
		}
		t.Logf("degree=%d height=%d leaves=%d avgLeafItems=%.2f (n=%d)",
			degree, maxDepth, leafCount, float64(leafItems)/float64(leafCount), sequentialInsertN)

		// In-order traversal must remain sorted and complete.
		prev := -1
		tree.Ascend(func(v int) bool {
			if v != prev+1 {
				t.Fatalf("degree %d: ascending order broken at %d after %d", degree, v, prev)
			}
			prev = v
			return true
		})
		if prev != sequentialInsertN-1 {
			t.Fatalf("degree %d: Ascend visited %d items, want %d", degree, prev+1, sequentialInsertN)
		}
	}
}

func buildSequential(degree, n int) *BTreeG[int] {
	tree := NewOrderedG[int](degree)
	for i := 0; i < n; i++ {
		tree.ReplaceOrInsert(i)
	}
	return tree
}

func buildShuffled(degree, n int) *BTreeG[int] {
	tree := NewOrderedG[int](degree)
	for _, v := range rand.New(rand.NewSource(1)).Perm(n) {
		tree.ReplaceOrInsert(v)
	}
	return tree
}

// BenchmarkGetSequentialShape vs BenchmarkGetShuffledShape compare point
// lookup cost after sequential (auto-increment) and random insertion. If the
// sequential tree degenerated into a spine, its lookup path would be far
// longer and clearly slower than the shuffled tree.
func BenchmarkGetSequentialShape(b *testing.B) {
	tree := buildSequential(4, sequentialInsertN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.Get(i % sequentialInsertN)
	}
}

func BenchmarkGetShuffledShape(b *testing.B) {
	tree := buildShuffled(4, sequentialInsertN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.Get(i % sequentialInsertN)
	}
}

func BenchmarkInsertSequential(b *testing.B) {
	tree := NewOrderedG[int](4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.ReplaceOrInsert(i)
	}
}
