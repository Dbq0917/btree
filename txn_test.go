package btree

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestMapTxnBatchCopiesPathsOnce(t *testing.T) {
	const count = 10000

	var m Map[int, int]
	for i := 0; i < count; i++ {
		m.Set(i, i)
	}
	snapshot := m.Clone()

	tx := m.Begin()
	for i := count; i < count*2; i++ {
		tx.Set(i, i)
	}
	if tx.Len() != count*2 {
		t.Fatalf("transaction len = %d, want %d", tx.Len(), count*2)
	}
	appendCopies := tx.CopyCount()
	appendHeight := tx.work.tree.Height()
	t.Logf("append batch: copies=%d height=%d", appendCopies, appendHeight)
	if appendCopies > 8 {
		t.Fatalf("append transaction copied %d nodes, want at most 8",
			appendCopies)
	}
	if appendHeight > 3 {
		t.Fatalf("append transaction height = %d, want at most 3",
			appendHeight)
	}
	if m.Len() != count {
		t.Fatalf("base changed before commit: len = %d", m.Len())
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if m.Len() != count*2 {
		t.Fatalf("committed len = %d, want %d", m.Len(), count*2)
	}
	if snapshot.Len() != count {
		t.Fatalf("snapshot changed after commit: len = %d", snapshot.Len())
	}
	m.tree.Sane(nil)
	snapshot.tree.Sane(nil)

	rng := rand.New(rand.NewPCG(1, 2))
	tx = m.Begin()
	for i := 0; i < count; i++ {
		key := rng.IntN(count * 2)
		tx.Set(key, -key)
	}
	copies := tx.CopyCount()
	height := tx.work.tree.Height()
	t.Logf("rewrite batch: copies=%d height=%d", copies, height)
	if copies >= count {
		t.Fatalf("rewrite transaction copied %d nodes for %d writes", copies, count)
	}
	if height > 3 {
		t.Fatalf("rewrite transaction height = %d, want at most 3", height)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	m.tree.Sane(nil)
	snapshot.tree.Sane(nil)

	snapshot.Release()
	m.Release()
}

func TestMapTxnCommitRollback(t *testing.T) {
	const count = 1000

	var m Map[int, int]
	for i := 0; i < count; i++ {
		m.Set(i, i)
	}
	snapshot := m.Clone()

	tx := m.Begin()
	for i := 0; i < count; i += 2 {
		tx.Set(i, -i)
	}
	for i := 0; i < count; i++ {
		want := i
		if i%2 == 0 {
			want = -i
		}
		if got, ok := tx.Lookup(i); !ok || got != want {
			t.Fatalf("txn lookup(%d) = %d, %v", i, got, ok)
		}
	}
	if got, _ := m.Lookup(0); got != 0 {
		t.Fatalf("base changed before rollback: got %d", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if got, ok := m.Lookup(i); !ok || got != i {
			t.Fatalf("rollback lookup(%d) = %d, %v", i, got, ok)
		}
	}
	if err := tx.Rollback(); !errors.Is(err, ErrTxnDone) {
		t.Fatalf("second rollback error = %v", err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTxnDone) {
		t.Fatalf("rollback then commit error = %v", err)
	}
	if tx.Len() != 0 || tx.Contains(0) {
		t.Fatal("completed transaction still exposes data")
	}

	tx = m.Begin()
	for i := 1; i < count; i += 2 {
		tx.Set(i, -i)
	}
	tx.Set(count, count)
	tx.Delete(0)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup(0); ok {
		t.Fatal("committed delete was lost")
	}
	for i := 1; i < count; i++ {
		want := i
		if i%2 == 1 {
			want = -i
		}
		if got, ok := m.Lookup(i); !ok || got != want {
			t.Fatalf("commit lookup(%d) = %d, %v; want %d", i, got, ok, want)
		}
	}
	if got, ok := m.Lookup(count); !ok || got != count {
		t.Fatalf("committed insert = %d, %v", got, ok)
	}
	if snapshot.Len() != count {
		t.Fatalf("snapshot changed after commit: len = %d", snapshot.Len())
	}
	for i := 0; i < count; i++ {
		if got, ok := snapshot.Lookup(i); !ok || got != i {
			t.Fatalf("snapshot lookup(%d) = %d, %v", i, got, ok)
		}
	}
	m.tree.Sane(nil)
	snapshot.tree.Sane(nil)

	snapshot.Release()
	m.Release()
}

func TestMapTxnDataRelease(t *testing.T) {
	refs := make(map[string]int)
	add := func(value string) {
		refs[value]++
	}
	del := func(value string) {
		refs[value]--
	}
	m := NewMapOptions(MapOptions[int, string]{
		Copy: func(value string) string {
			add(value)
			return value
		},
		Release: del,
	})
	const count = 512
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("old-%04d", i)
		add(value)
		m.Set(i, value)
	}
	snapshot := m.Clone()

	tx := m.Begin()
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("new-%04d", i)
		add(value)
		tx.Set(count+i, value)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("old-%04d", i)
		if refs[value] != 1 {
			t.Fatalf("rollback changed %s refcount to %d", value, refs[value])
		}
		if got, ok := m.Lookup(i); !ok || got != value {
			t.Fatalf("rollback lookup(%d) = %q, %v", i, got, ok)
		}
	}
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("new-%04d", i)
		if refs[value] != 0 {
			t.Fatalf("rollback left %s refcount at %d", value, refs[value])
		}
	}

	tx = m.Begin()
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("new-%04d", i)
		add(value)
		tx.Set(count+i, value)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		oldValue := fmt.Sprintf("old-%04d", i)
		if refs[oldValue] < 1 {
			t.Fatalf("commit released %s too early: refcount %d", oldValue, refs[oldValue])
		}
		newValue := fmt.Sprintf("new-%04d", i)
		if refs[newValue] != 1 {
			t.Fatalf("commit changed %s refcount to %d", newValue, refs[newValue])
		}
		if got, ok := m.Lookup(count + i); !ok || got != newValue {
			t.Fatalf("commit lookup(%d) = %q, %v", i, got, ok)
		}
		if got, ok := snapshot.Lookup(i); !ok ||
			got != fmt.Sprintf("old-%04d", i) {
			t.Fatalf("snapshot lookup(%d) = %q, %v", i, got, ok)
		}
	}

	snapshot.Release()
	m.Release()
	for value, count := range refs {
		if count != 0 {
			t.Fatalf("%s has final refcount %d", value, count)
		}
	}
}

func TestMapTxnRandomized(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	ref := make(map[int]int)
	var m Map[int, int]

	for batch := 0; batch < 500; batch++ {
		want := maps.Clone(ref)
		tx := m.Begin()
		for i := 0; i < 100; i++ {
			key := rng.IntN(1000)
			switch rng.IntN(3) {
			case 0:
				value := rng.Int()
				if _, ok := want[key]; ok {
					tx.Set(key, value)
				} else {
					tx.Insert(key, value)
				}
				want[key] = value
			case 1:
				tx.Set(key, -key)
				want[key] = -key
			default:
				tx.Delete(key)
				delete(want, key)
			}
		}
		if tx.Len() != len(want) {
			t.Fatalf("batch %d txn len = %d, want %d", batch, tx.Len(), len(want))
		}
		if batch%3 == 0 {
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			ref = want
		}
		got := maps.Collect(m.All())
		if !maps.Equal(got, ref) {
			t.Fatalf("batch %d map mismatch: got %d items, want %d",
				batch, len(got), len(ref))
		}
		m.tree.Sane(nil)
	}
	if height := m.tree.Height(); height > 4 {
		t.Fatalf("height = %d, want at most 4", height)
	}
	m.Release()
}

func TestSetAndTableTxn(t *testing.T) {
	var set Set[int]
	for i := 0; i < 100; i++ {
		set.Insert(i)
	}
	setSnapshot := set.Clone()
	stx := set.Begin()
	for i := 50; i < 150; i++ {
		stx.Insert(i)
	}
	for i := 0; i < 25; i++ {
		stx.Delete(i)
	}
	if stx.Len() != 125 {
		t.Fatalf("set txn len = %d, want 125", stx.Len())
	}
	if err := stx.Commit(); err != nil {
		t.Fatal(err)
	}
	if set.Len() != 125 || set.Contains(0) || !set.Contains(149) {
		t.Fatal("set commit did not apply")
	}
	if setSnapshot.Len() != 100 || !setSnapshot.Contains(0) {
		t.Fatal("set snapshot changed")
	}

	var table Table[int]
	for i := 0; i < 100; i++ {
		table.Insert(i)
	}
	tableSnapshot := table.Clone()
	ttx := table.Begin()
	for i := 25; i < 75; i++ {
		ttx.Delete(i)
	}
	for i := 100; i < 150; i++ {
		ttx.Insert(i)
	}
	if err := ttx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if table.Len() != 100 || !table.Contains(25) || table.Contains(100) {
		t.Fatal("table rollback changed base")
	}
	ttx = table.Begin()
	for i := 10; i < 20; i++ {
		ttx.Delete(i)
	}
	if err := ttx.Commit(); err != nil {
		t.Fatal(err)
	}
	if table.Len() != 90 || table.Contains(10) || !table.Contains(20) {
		t.Fatal("table commit did not apply")
	}
	if !slices.Equal(
		slices.Collect(tableSnapshot.All()),
		func() []int {
			items := make([]int, 100)
			for i := range items {
				items[i] = i
			}
			return items
		}(),
	) {
		t.Fatal("table snapshot changed")
	}

	setSnapshot.Release()
	set.Release()
	tableSnapshot.Release()
	table.Release()
}
