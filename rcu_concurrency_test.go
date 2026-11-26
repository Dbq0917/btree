package btree

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRCUMapIteratorPinsCompleteSnapshot(t *testing.T) {
	var m Map[int, int]
	const count = 2048
	for i := range count {
		m.Set(i, i)
	}
	want := slices.Collect(m.Keys())

	var got []int
	for key := range m.All() {
		got = append(got, key)
		m.Set(count+key, key)
		if key%2 == 0 && key+1 < count {
			m.Delete(key + 1)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("iterator observed a mutated tree: got %d keys, want %d",
			len(got), len(want))
	}
}

func TestRCUMapOneWriterManyReaders(t *testing.T) {
	var m Map[int, int]
	const initial = 2048
	for i := range initial {
		m.Set(i, i)
	}

	var stop atomic.Bool
	var violations atomic.Int64
	var wg sync.WaitGroup
	readers := max(4, runtime.GOMAXPROCS(0)*2)
	for r := range readers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for !stop.Load() {
				switch id & 7 {
				case 0:
					prev, seen := -1, 0
					for key, value := range m.All() {
						if key != value || key <= prev {
							violations.Add(1)
							return
						}
						prev = key
						seen++
					}
					if seen == 0 {
						violations.Add(1)
						return
					}
				case 1:
					for key := range initial {
						value, ok := m.Lookup(key)
						if ok && value != key {
							violations.Add(1)
							return
						}
					}
				case 2:
					key, value, ok := m.Seek(initial / 2)
					if ok && (key < initial/2 || value != key) {
						violations.Add(1)
						return
					}
				case 3:
					keys := m.Keys()
					prev, seen := -1, 0
					for key := range keys {
						if key <= prev {
							violations.Add(1)
							return
						}
						prev = key
						seen++
					}
					if seen == 0 {
						violations.Add(1)
						return
					}
				case 4:
					_, _, _ = m.LookupAt(0)
					_, _ = m.GetAt(0)
					_, _ = m.IndexOf(initial / 2)
				case 5:
					_, _, _ = m.Front()
					_, _, _ = m.Back()
					_, _, _ = m.SeekPrev(initial / 2)
				case 6:
					prev, seen := 1<<30, 0
					for key := range m.Backward() {
						if key >= prev {
							violations.Add(1)
							return
						}
						prev = key
						seen++
					}
					if seen == 0 {
						violations.Add(1)
						return
					}
				case 7:
					for range m.AscendAt(64) {
					}
					for range m.DescendAt(64) {
					}
				}
			}
		}(r)
	}

	for i := range 50000 {
		key := i % (initial * 2)
		if i%127 == 0 {
			m.Delete(key)
		} else {
			m.Set(key, key)
		}
	}
	stop.Store(true)
	wg.Wait()

	if violations.Load() != 0 {
		t.Fatalf("observed %d torn or inconsistent reads", violations.Load())
	}
}

func TestRCUReadLeasePinsRootUntilTraversalEnds(t *testing.T) {
	var released atomic.Int64
	m := NewMapOptions(MapOptions[int, int]{
		Release: func(int) { released.Add(1) },
	})
	m.Set(1, 1)

	view, lease := m.tree.readView()
	if view.root == nil || lease.root == nil {
		t.Fatal("missing published root")
	}
	oldRoot := lease.root

	done := make(chan struct{})
	go func() {
		m.Set(2, 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked behind an active reader")
	}

	if refs := oldRoot.rc.Load(); refs != 0 {
		t.Fatalf("pinned old root refs = %d, want 0 after writer release", refs)
	}
	if count := released.Load(); count != 0 {
		t.Fatalf("old root reclaimed before read end: %d releases", count)
	}

	lease.End()
	if refs := oldRoot.rc.Load(); refs != -1 {
		t.Fatalf("old root refs = %d, want -1 after read end", refs)
	}
	if released.Load() == 0 {
		t.Fatal("old root values were not released after read end")
	}
}

func TestRCUMapZeroValueConcurrentInit(t *testing.T) {
	var m Map[int, int]
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_, _ = m.Lookup(1)
				_ = m.Len()
			}
		}()
	}
	for i := range 10000 {
		m.Set(i, i)
	}
	stop.Store(true)
	wg.Wait()
}

func TestRCUActionCallbackCanReenterRead(t *testing.T) {
	var m Map[int, int]
	m.Set(1, -1)
	m.Action(1, func(value int) (int, Action) {
		if value != -1 || m.Len() != 1 {
			t.Errorf("unexpected action state: value=%d len=%d", value, m.Len())
		}
		if got, ok := m.Lookup(1); !ok || got != -1 {
			t.Errorf("reentrant lookup = (%d,%v)", got, ok)
		}
		return 42, ActionReplace
	})
	if got, ok := m.Lookup(1); !ok || got != 42 {
		t.Fatalf("after Action got (%d,%v)", got, ok)
	}
}

func TestRCUClearReleasesPinnedRootAfterReadEnds(t *testing.T) {
	var m Map[int, int]
	m.Set(1, 1)

	_, lease := m.tree.readView()
	oldRoot := lease.root
	if oldRoot == nil {
		t.Fatal("missing root")
	}
	m.Clear()
	if refs := oldRoot.rc.Load(); refs != 0 {
		t.Fatalf("pinned cleared root refs = %d, want 0", refs)
	}

	lease.End()
	if refs := oldRoot.rc.Load(); refs != -1 {
		t.Fatalf("cleared root refs = %d, want -1", refs)
	}
	if m.Len() != 0 {
		t.Fatalf("Len = %d after Clear", m.Len())
	}
}
