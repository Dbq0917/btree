package btree

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// Delete a random half of the keys (forcing sibling merges) and then look up
// every surviving key. A broken merge pointer leaves survivors unreachable, so
// any miss is a direct regression. Runs both plain and copy-on-write trees.
func TestBTreeMergeDeleteLookupHitRate(t *testing.T) {
	for _, cloned := range []bool{false, true} {
		for trial := 0; trial < 20; trial++ {
			rng := rand.New(rand.NewPCG(uint64(trial*1000+1), uint64(trial*7+3)))
			src := NewMap[int, int]()
			keys := make([]int, 0, 2000)
			for i := range 2000 {
				k := rng.Int()
				if _, inserted := src.Insert(k, i); inserted {
					keys = append(keys, k)
				}
			}
			tree := src
			if cloned {
				tree = src.Clone()
				src.Release()
			}

			rng.Shuffle(len(keys), func(i, j int) {
				keys[i], keys[j] = keys[j], keys[i]
			})
			deleted := make(map[int]bool, len(keys)/2)
			for _, k := range keys[:len(keys)/2] {
				if _, ok := tree.Delete(k); !ok {
					t.Fatalf("cloned=%v trial %d: delete missing key %d", cloned, trial, k)
				}
				deleted[k] = true
			}

			misses := 0
			for _, k := range keys {
				if !deleted[k] {
					if _, ok := tree.Lookup(k); !ok {
						misses++
					}
				}
			}
			if misses != 0 {
				t.Fatalf("cloned=%v trial %d: %d surviving keys unreachable after merge",
					cloned, trial, misses)
			}
			tree.tree.Sane(func(k int, v int) bool { return k == 0 && v == 0 })
			tree.Release()
		}
	}
}

// String keys exercise the prefix-accelerated search path. Same guarantee:
// heavy merges must never strand a surviving session key.
func TestBTreeMergeStringKeys(t *testing.T) {
	for _, cloned := range []bool{false, true} {
		for trial := 0; trial < 10; trial++ {
			rng := rand.New(rand.NewPCG(uint64(trial*97+5), uint64(trial*13+7)))
			src := NewMap[string, int]()
			keys := make([]string, 0, 3000)
			seen := make(map[string]bool)
			for i := range 3000 {
				k := fmt.Sprintf("session-%06d-%d", rng.IntN(1_000_000), i)
				if seen[k] {
					continue
				}
				seen[k] = true
				if _, inserted := src.Insert(k, i); inserted {
					keys = append(keys, k)
				}
			}
			tree := src
			if cloned {
				tree = src.Clone()
				src.Release()
			}

			rng.Shuffle(len(keys), func(i, j int) {
				keys[i], keys[j] = keys[j], keys[i]
			})
			cut := len(keys) * 3 / 4
			for _, k := range keys[:cut] {
				if _, ok := tree.Delete(k); !ok {
					t.Fatalf("cloned=%v trial %d: delete missing %q", cloned, trial, k)
				}
			}
			for _, k := range keys[cut:] {
				if _, ok := tree.Lookup(k); !ok {
					t.Fatalf("cloned=%v trial %d: surviving session %q unreachable",
						cloned, trial, k)
				}
			}
			tree.tree.Sane(func(k string, v int) bool { return k == "" && v == 0 })
			tree.Release()
		}
	}
}

// The hot path described in the report: interleave deletes with lookups of
// every survivor on a cloned tree so merges and reads race through the same
// nodes. A dangling merged node shows up as a lost key.
func TestBTreeMergeInterleaved(t *testing.T) {
	for trial := 0; trial < 5; trial++ {
		rng := rand.New(rand.NewPCG(uint64(trial*31+17), uint64(trial*41+23)))
		src := NewMap[int, int]()
		keys := make([]int, 0, 1500)
		for i := range 1500 {
			k := rng.Int()
			if _, inserted := src.Insert(k, i); inserted {
				keys = append(keys, k)
			}
		}
		tree := src.Clone()
		src.Release()
		live := make(map[int]bool, len(keys))
		for _, k := range keys {
			live[k] = true
		}
		rng.Shuffle(len(keys), func(i, j int) {
			keys[i], keys[j] = keys[j], keys[i]
		})
		for idx, k := range keys {
			if _, ok := tree.Delete(k); !ok {
				t.Fatalf("trial %d: delete missing %d", trial, k)
			}
			delete(live, k)
			if idx%50 == 0 {
				for lk := range live {
					if _, ok := tree.Lookup(lk); !ok {
						t.Fatalf("trial %d: surviving key %d lost after deleting %d",
							trial, lk, k)
					}
				}
			}
		}
		tree.Release()
	}
}

// A user Release hook must never run for data that is still referenced. A
// merge that drops a moved subtree's ownership incorrectly manifests here as
// a negative live count (double free), including under copy-on-write churn.
func TestBTreeMergeNoDoubleRelease(t *testing.T) {
	live := make(map[int]int)
	for trial := 0; trial < 20; trial++ {
		clear(live)
		rng := rand.New(rand.NewPCG(uint64(trial*1000+11), uint64(trial*7+13)))
		src := NewMapOptions(MapOptions[int, int]{
			Copy: func(v int) int {
				live[v]++
				return v
			},
			Release: func(v int) {
				live[v]--
				if live[v] < 0 {
					t.Fatalf("trial %d: value %d released while still referenced",
						trial, v)
				}
			},
		})
		keys := make([]int, 0, 2000)
		for i := range 2000 {
			k := rng.Int()
			v := trial*100000 + i
			if _, inserted := src.Insert(k, v); inserted {
				keys = append(keys, k)
				live[v] = 1
			}
		}
		tree := src.Clone()
		src.Release()

		rng.Shuffle(len(keys), func(i, j int) {
			keys[i], keys[j] = keys[j], keys[i]
		})
		survivors := keys[len(keys)/2:]
		for _, k := range keys[:len(keys)/2] {
			if _, ok := tree.Delete(k); !ok {
				t.Fatalf("trial %d: delete missing key %d", trial, k)
			}
		}
		for _, k := range survivors {
			if _, ok := tree.LookupMut(k); !ok {
				t.Fatalf("trial %d: surviving key %d missing", trial, k)
			}
			if _, ok := tree.Lookup(k); !ok {
				t.Fatalf("trial %d: surviving key %d missing on plain lookup", trial, k)
			}
		}
		tree.Release()
	}
}
