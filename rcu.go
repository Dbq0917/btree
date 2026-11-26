package btree

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

const rcuClaimBit = int64(1) << 62

type publication[K, V any] struct {
	root  *node[K, V]
	count int
}

type readLease[K, V any] struct {
	t        *tree[K, V]
	root     *node[K, V]
	borrowed bool
}

func (l *readLease[K, V]) End() {
	if l == nil || l.borrowed || l.root == nil {
		return
	}
	root := l.root
	l.root = nil
	l.t.releaseNode(root)
}

func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	text := string(buf[:n])
	text, ok := strings.CutPrefix(text, "goroutine ")
	if !ok {
		return 0
	}
	end := strings.IndexByte(text, ' ')
	if end < 0 {
		return 0
	}
	id, _ := strconv.ParseUint(text[:end], 10, 64)
	return id
}

func (t *tree[K, V]) writeMutex() *sync.Mutex {
	mu := (*sync.Mutex)(atomic.LoadPointer(&t.writeMu))
	if mu != nil {
		return mu
	}
	candidate := new(sync.Mutex)
	if atomic.CompareAndSwapPointer(&t.writeMu, nil,
		unsafe.Pointer(candidate)) {
		return candidate
	}
	return (*sync.Mutex)(atomic.LoadPointer(&t.writeMu))
}

func (t *tree[K, V]) lockWrite() func() {
	_, owner := t.enterWrite()
	return func() { t.leaveWrite(owner) }
}

func (t *tree[K, V]) enterWrite() (int, uint64) {
	owner := goroutineID()
	mu := t.writeMutex()
	if mu.TryLock() {
		atomic.StoreUint64(&t.writeOwner, owner)
		t.writeDepth = 1
		return 1, owner
	}
	if atomic.LoadUint64(&t.writeOwner) == owner {
		t.writeDepth++
		return t.writeDepth, owner
	}
	mu.Lock()
	atomic.StoreUint64(&t.writeOwner, owner)
	t.writeDepth = 1
	return 1, owner
}

func (t *tree[K, V]) leaveWrite(owner uint64) {
	if atomic.LoadUint64(&t.writeOwner) != owner {
		return
	}
	t.writeDepth--
	if t.writeDepth > 0 {
		return
	}
	t.writeDepth = 0
	atomic.StoreUint64(&t.writeOwner, 0)
	t.writeMutex().Unlock()
}

func (t *tree[K, V]) initialize(init func()) {
	if atomic.LoadUint32(&t.initState) == 1 {
		return
	}
	_, owner := t.enterWrite()
	defer t.leaveWrite(owner)
	if atomic.LoadUint32(&t.initState) == 0 {
		init()
		atomic.StoreUint32(&t.initState, 1)
	}
}

// beginWrite serializes writers and checks out the latest published tree as
// the writer-private working tree. The returned function publishes the
// completed tree exactly once and releases roots replaced by cowRoot.
func (t *tree[K, V]) beginWrite() func() {
	depth, owner := t.enterWrite()
	if depth > 1 {
		return func() { t.leaveWrite(owner) }
	}
	p := (*publication[K, V])(atomic.LoadPointer(&t.pub))
	if p == nil {
		p = &publication[K, V]{root: t.root, count: t.count}
		atomic.StorePointer(&t.pub, unsafe.Pointer(p))
	}
	t.root = p.root
	t.count = p.count
	t.rcuWrite = true
	t.pendingRoots = t.pendingRoots[:0]
	t.claimedRoot = nil
	t.claimBase = 0

	root := t.root
	if root != nil {
		for {
			refs := root.rc.Load()
			if refs < 0 || refs&rcuClaimBit != 0 {
				runtime.Gosched()
				continue
			}
			if root.rc.CompareAndSwap(refs, refs|rcuClaimBit) {
				t.claimedRoot = root
				t.claimBase = refs
				break
			}
		}
	}

	return func() {
		roots := t.pendingRoots
		claimedRoot := t.claimedRoot
		t.pendingRoots = nil
		t.claimedRoot = nil
		t.rcuWrite = false
		atomic.StorePointer(&t.pub, unsafe.Pointer(&publication[K, V]{
			root:  t.root,
			count: t.count,
		}))
		if claimedRoot != nil {
			claimedRoot.rc.Add(-rcuClaimBit)
		}
		t.leaveWrite(owner)
		for _, root := range roots {
			t.releaseNode(root)
		}
	}
}

func (t *tree[K, V]) acquireRoot() *node[K, V] {
	for {
		p := (*publication[K, V])(atomic.LoadPointer(&t.pub))
		if p == nil || p.root == nil {
			return nil
		}
		root := p.root
		for {
			refs := root.rc.Load()
			if refs < 0 || refs&rcuClaimBit != 0 {
				break
			}
			if root.rc.CompareAndSwap(refs, refs+1) {
				return root
			}
		}
		runtime.Gosched()
	}
}

// readView returns an immutable tree value rooted at a reference-counted root.
// The caller must invoke the returned lease's End method after the traversal.
func (t *tree[K, V]) readView() (tree[K, V], readLease[K, V]) {
	if owner := atomic.LoadUint64(&t.writeOwner); owner != 0 &&
		owner == goroutineID() {
		view := tree[K, V]{
			strpre:      t.strpre,
			nopre:       t.nopre,
			stdops:      t.stdops,
			alt:         t.alt,
			initd:       true,
			initState:   1,
			count:       t.count,
			root:        t.root,
			dataCopy:    t.dataCopy,
			dataRelease: t.dataRelease,
			dataCompare: t.dataCompare,
			dataSearch:  t.dataSearch,
		}
		return view, readLease[K, V]{t: t, borrowed: true}
	}
	root := t.acquireRoot()
	var count int
	p := (*publication[K, V])(atomic.LoadPointer(&t.pub))
	if p != nil && p.root == root {
		count = p.count
	} else if root != nil {
		count = root.count()
	}
	var view tree[K, V]
	if atomic.LoadUint32(&t.initState) == 1 {
		view = tree[K, V]{
			strpre:      t.strpre,
			nopre:       t.nopre,
			stdops:      t.stdops,
			alt:         t.alt,
			initd:       true,
			count:       count,
			root:        root,
			dataCopy:    t.dataCopy,
			dataRelease: t.dataRelease,
			dataCompare: t.dataCompare,
			dataSearch:  t.dataSearch,
		}
	} else {
		view = tree[K, V]{
			initd: false,
			count: count,
			root:  root,
		}
	}
	return view, readLease[K, V]{t: t, root: root}
}
