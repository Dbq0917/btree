package btree

import (
	"sync"
	"testing"
)

func TestAscendSnapshotDuringSplits(t *testing.T) {
	tr := NewG[int](2, func(a, b int) bool { return a < b })
	const initial = 4096
	for i := 0; i < initial; i++ {
		tr.ReplaceOrInsert(i)
	}

	visited := 0
	inserted := 0
	tr.Ascend(func(v int) bool {
		if v != visited {
			t.Fatalf("iteration saw %d at position %d", v, visited)
		}
		visited++
		if inserted < initial {
			tr.ReplaceOrInsert(initial + inserted)
			inserted++
		}
		return true
	})

	if visited != initial {
		t.Fatalf("iteration visited %d keys, want complete pre-split view of %d", visited, initial)
	}
	if got := tr.Len(); got != 2*initial {
		t.Fatalf("Len after inserts = %d, want %d", got, 2*initial)
	}

	visited = 0
	prev := -1
	tr.Ascend(func(v int) bool {
		if v <= prev {
			t.Fatalf("post-insert iteration out of order: %d after %d", v, prev)
		}
		prev = v
		visited++
		return true
	})
	if visited != 2*initial {
		t.Fatalf("post-insert iteration visited %d keys, want %d", visited, 2*initial)
	}
}

func TestConcurrentAscendAndInsert(t *testing.T) {
	tr := New(2)
	const initial = 5000
	const extra = 5000
	for i := 0; i < initial; i++ {
		tr.ReplaceOrInsert(Int(i))
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := initial; i < initial+extra; i++ {
			tr.ReplaceOrInsert(Int(i))
		}
	}()

	for i := 0; i < 25; i++ {
		count := 0
		prev := Int(-1)
		tr.Ascend(func(item Item) bool {
			v := item.(Int)
			if count > 0 && v <= prev {
				t.Errorf("iteration out of order: %d after %d", v, prev)
			}
			prev = v
			count++
			return true
		})
		if count < initial || count > initial+extra {
			t.Fatalf("iteration visited %d keys, want a snapshot between %d and %d",
				count, initial, initial+extra)
		}
	}

	wg.Wait()
	if got := tr.Len(); got != initial+extra {
		t.Fatalf("final Len = %d, want %d", got, initial+extra)
	}
}
