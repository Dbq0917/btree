package btree

import (
	"iter"
	"runtime"
	"slices"
	"testing"
)

func TestMapIterSnapshotWithConcurrentWrites(t *testing.T) {
	const count = 8192

	var m Map[int, int]
	want := make([]int, count)
	for i := range count {
		m.Set(i, i)
		want[i] = i
	}

	var got []int
	for key := range m.All() {
		got = append(got, key)

		// Mutate through the same map while the iterator is active. The
		// inserts can split the current path, and the deletes remove items
		// that have not been visited yet.
		for i := range 8 {
			m.Set(count+key*8+i, count+key*8+i)
		}
		if key%64 == 0 {
			for i := 1; i <= 8; i++ {
				m.Delete(key + i*97)
			}
		}
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d keys from snapshot, got %d", len(want), len(got))
	}
	if !slices.Equal(got, want) {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("snapshot mismatch at index %d: want %d, got %d",
					i, want[i], got[i])
			}
		}
	}
}

func TestMapRangeIteratorsUseSnapshot(t *testing.T) {
	const count = 2048
	emptyValue := 0

	tests := []struct {
		name string
		seq  func(*Map[int, int]) iter.Seq2[int, int]
	}{
		{"all", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.All()
		}},
		{"backward", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.Backward()
		}},
		{"ascend", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.Ascend(count / 2)
		}},
		{"descend", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.Descend(count / 2)
		}},
		{"ascend-at", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.AscendAt(count / 2)
		}},
		{"descend-at", func(m *Map[int, int]) iter.Seq2[int, int] {
			return m.DescendAt(count / 2)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Map[int, int]
			for i := range count {
				m.Set(i, i)
			}

			var want []int
			for key := range tt.seq(&m) {
				want = append(want, key)
			}

			var got []int
			for key := range tt.seq(&m) {
				got = append(got, key)
				m.Set(count+key, emptyValue)
				m.Delete((key*37 + 1) % count)
			}

			if !slices.Equal(got, want) {
				t.Fatalf("snapshot changed during iteration: want %d keys, got %d",
					len(want), len(got))
			}
		})
	}
}

func TestMapIterSnapshotWithWriterGoroutine(t *testing.T) {
	const count = 32768

	var m Map[int, int]
	want := make([]int, count)
	for i := range count {
		m.Set(i, i)
		want[i] = i
	}

	started := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		<-started
		for i := range 20000 {
			m.Set(count+i, i)
			m.Delete((i * 37) % count)
		}
	}()

	var got []int
	for key := range m.All() {
		if len(got) == 0 {
			close(started)
		}
		got = append(got, key)
		if len(got)%256 == 0 {
			runtime.Gosched()
		}
	}
	<-writerDone

	if !slices.Equal(got, want) {
		t.Fatalf("snapshot changed during concurrent write: want %d keys, got %d",
			len(want), len(got))
	}
}
