//go:build go1.18
// +build go1.18

package btree

import "testing"

// TestFreeListDefersReclaimWhilePinnedG verifies that a node freed by a
// writer is neither truncated nor recycled while a reader holds a pin, and
// that reclamation resumes once the reader unpins.
func TestFreeListDefersReclaimWhilePinnedG(t *testing.T) {
	tr := NewOrderedG[int](2)
	for i := 0; i < 100; i++ {
		tr.ReplaceOrInsert(i)
	}
	cow := tr.cow
	fl := cow.freelist

	victim := tr.root
	kids := append([]*node[int](nil), tr.root.children...)

	e := fl.pin()
	cow.freeNode(victim)
	// Retire more owned nodes to rotate epochs and trigger reclaim attempts.
	for _, k := range kids {
		cow.freeNode(k)
	}
	if len(victim.items) == 0 {
		t.Fatal("node reclaimed while a reader was pinned")
	}
	fl.unpin(e)

	// With no readers pinned, further retires must drain the limbo bags and
	// reclaim the victim.
	for i := 0; i < 4 && len(victim.items) != 0; i++ {
		cow.freeNode(&node[int]{cow: cow})
	}
	if len(victim.items) != 0 {
		t.Fatal("node not reclaimed after all readers unpinned")
	}
}

// TestFreeListRecycleAfterReclaimG verifies that reclaimed nodes are returned
// to the free list and handed out again, so the freelist keeps working.
func TestFreeListRecycleAfterReclaimG(t *testing.T) {
	tr := NewOrderedG[int](2)
	for i := 0; i < 100; i++ {
		tr.ReplaceOrInsert(i)
	}
	cow := tr.cow
	fl := cow.freelist

	victim := tr.root
	cow.freeNode(victim)
	// No readers pinned: a few retires rotate epochs and reclaim the victim.
	for i := 0; i < 4; i++ {
		cow.freeNode(&node[int]{cow: cow})
	}
	if len(victim.items) != 0 {
		t.Fatal("node not reclaimed with no readers pinned")
	}
	found := false
	for _, n := range fl.freelist {
		if n == victim {
			found = true
		}
	}
	if !found {
		t.Fatal("reclaimed node was not returned to the free list")
	}
}
