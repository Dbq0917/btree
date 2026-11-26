package batchkv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestDB(t *testing.T, mutate func(*Options)) *DB {
	t.Helper()
	opts := Options{Dir: t.TempDir()}
	if mutate != nil {
		mutate(&opts)
	}
	db, err := OpenOptions(opts)
	if err != nil {
		t.Fatalf("OpenOptions: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func requireNotFound(t *testing.T, db *DB, key string) {
	t.Helper()
	_, err := db.Get([]byte(key))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(%q) error = %v, want ErrNotFound", key, err)
	}
}

func requireValue(t *testing.T, db *DB, key, want string) {
	t.Helper()
	got, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if string(got) != want {
		t.Fatalf("Get(%q) = %q, want %q", key, got, want)
	}
}

func TestPutGetAndReopen(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("hello"), []byte("world")); err != nil {
		t.Fatal(err)
	}
	if err := db.PutBatch(
		PutEntry([]byte("a"), []byte("1")),
		PutEntry([]byte("b"), []byte("2")),
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	requireValue(t, db2, "hello", "world")
	requireValue(t, db2, "a", "1")
	requireValue(t, db2, "b", "2")
	if db2.Len() != 3 {
		t.Fatalf("Len = %d, want 3", db2.Len())
	}
}

func TestBatchBuilderDeleteAndOverwrite(t *testing.T) {
	db := openTestDB(t, nil)
	if err := db.Put([]byte("delete-me"), []byte("v")); err != nil {
		t.Fatal(err)
	}

	batch := db.NewBatch()
	batch.Put([]byte("k1"), []byte("v1")).
		Put([]byte("k2"), []byte("old")).
		Delete([]byte("delete-me")).
		Put([]byte("k2"), []byte("new"))
	if batch.Len() != 4 {
		t.Fatalf("Len = %d, want 4", batch.Len())
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}

	requireValue(t, db, "k1", "v1")
	requireValue(t, db, "k2", "new")
	requireNotFound(t, db, "delete-me")
}

func TestPutBatchSizeValidationIsAtomic(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, func(opts *Options) {
		opts.Dir = dir
		opts.MaxValueBytes = 8
	})

	err := db.PutBatch(
		PutEntry([]byte("good"), []byte("v")),
		PutEntry([]byte("too-big"), make([]byte, 9)),
	)
	if !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("PutBatch error = %v, want ErrValueTooLarge", err)
	}
	requireNotFound(t, db, "good")

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	requireNotFound(t, db2, "good")
	requireNotFound(t, db2, "too-big")
}

type partialFailFile struct {
	segmentFile
	offset    int64
	failAfter int
	err       error
	failed    bool
}

func (f *partialFailFile) WriteAt(p []byte, off int64) (int, error) {
	if !f.failed && off == f.offset {
		f.failed = true
		n := f.failAfter
		if n > len(p) {
			n = len(p)
		}
		written, writeErr := f.segmentFile.WriteAt(p[:n], off)
		if writeErr != nil {
			return written, writeErr
		}
		return written, f.err
	}
	return f.segmentFile.WriteAt(p, off)
}

func TestPutBatchWriteFailureLeavesHoleButNoIndexOrRecovery(t *testing.T) {
	dir := t.TempDir()
	diskFull := errors.New("simulated disk full")
	var fault *partialFailFile

	db := openTestDB(t, func(opts *Options) {
		opts.Dir = dir
		opts.openFile = func(name string, flag int, perm os.FileMode) (segmentFile, error) {
			file, err := os.OpenFile(name, flag, perm)
			if err != nil {
				return nil, err
			}
			fault = &partialFailFile{
				segmentFile: file,
				offset:      0,
				failAfter:   11,
				err:         diskFull,
			}
			return fault, nil
		}
	})

	err := db.PutBatch(
		PutEntry([]byte("missing-1"), []byte("value-1")),
		PutEntry([]byte("missing-2"), []byte("value-2-longer")),
	)
	if !errors.Is(err, diskFull) {
		t.Fatalf("PutBatch error = %v, want diskFull", err)
	}
	requireNotFound(t, db, "missing-1")
	requireNotFound(t, db, "missing-2")

	holeEnd := db.writeOff
	if holeEnd <= int64(fault.failAfter) {
		t.Fatalf("reserved batch end = %d, want a hole after %d bytes", holeEnd, fault.failAfter)
	}

	if err := db.Put([]byte("after-hole"), []byte("committed")); err != nil {
		t.Fatalf("Put after failed batch: %v", err)
	}
	loc, ok := db.index.Lookup("after-hole")
	if !ok {
		t.Fatal("after-hole missing from index")
	}
	if wantOffset := holeEnd + headerSize; loc.Offset != wantOffset {
		t.Fatalf("new record offset = %d, want first record offset %d", loc.Offset, wantOffset)
	}
	requireValue(t, db, "after-hole", "committed")

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	requireValue(t, db2, "after-hole", "committed")
	requireNotFound(t, db2, "missing-1")
	requireNotFound(t, db2, "missing-2")
}

func TestRecoveryIgnoresTornBatchAndFindsLaterBatch(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("committed"), []byte("yes")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	torn, _, err := encodeBatch(77, []Entry{
		PutEntry([]byte("torn-1"), []byte("nope-1")),
		PutEntry([]byte("torn-2"), []byte("nope-2")),
	}, DefaultMaxKeyBytes, DefaultMaxValueBytes, DefaultMaxBatchBytes)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, segmentFileName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(torn[:len(torn)/2]); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	requireValue(t, db2, "committed", "yes")
	requireNotFound(t, db2, "torn-1")
	requireNotFound(t, db2, "torn-2")

	if err := db2.Put([]byte("later"), []byte("found")); err != nil {
		t.Fatal(err)
	}
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
	db3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db3.Close()
	requireValue(t, db3, "later", "found")
	requireNotFound(t, db3, "torn-1")
}

func TestRecoveryScansLargeHoleToLaterCommittedFrame(t *testing.T) {
	dir := t.TempDir()
	diskFull := errors.New("simulated large write failure")
	var fault *partialFailFile

	db := openTestDB(t, func(opts *Options) {
		opts.Dir = dir
		opts.MaxValueBytes = 512 << 10
		opts.MaxBatchBytes = 1 << 20
		opts.openFile = func(name string, flag int, perm os.FileMode) (segmentFile, error) {
			file, err := os.OpenFile(name, flag, perm)
			if err != nil {
				return nil, err
			}
			fault = &partialFailFile{
				segmentFile: file,
				offset:      0,
				failAfter:   headerSize + 3,
				err:         diskFull,
			}
			return fault, nil
		}
	})

	err := db.PutBatch(PutEntry([]byte("large-missing"), make([]byte, 200<<10)))
	if !errors.Is(err, diskFull) {
		t.Fatalf("large batch error = %v, want diskFull", err)
	}
	if db.writeOff < 128<<10 {
		t.Fatalf("reserved hole = %d bytes, want a large hole", db.writeOff)
	}
	if err := db.Put([]byte("after-large-hole"), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	requireValue(t, db2, "after-large-hole", "ok")
	requireNotFound(t, db2, "large-missing")
}

type lockProbeFile struct {
	segmentFile
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	frameWrites   int
	mu            sync.Mutex
}

func (f *lockProbeFile) WriteAt(p []byte, off int64) (int, error) {
	if len(p) != commitSize {
		f.mu.Lock()
		f.frameWrites++
		n := f.frameWrites
		f.mu.Unlock()

		if n == 1 {
			close(f.firstStarted)
			<-f.releaseFirst
		} else if n == 2 {
			select {
			case <-f.secondStarted:
			default:
				close(f.secondStarted)
			}
		}
	}
	return f.segmentFile.WriteAt(p, off)
}

func TestBatchWriteDoesNotHoldCommitLockDuringIO(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	probe := &lockProbeFile{
		firstStarted:  firstStarted,
		secondStarted: secondStarted,
		releaseFirst:  releaseFirst,
	}

	db := openTestDB(t, func(opts *Options) {
		opts.openFile = func(name string, flag int, perm os.FileMode) (segmentFile, error) {
			file, err := os.OpenFile(name, flag, perm)
			if err != nil {
				return nil, err
			}
			probe.segmentFile = file
			return probe, nil
		}
	})

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- db.PutBatch(PutEntry([]byte("first"), []byte("1")))
	}()

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch write did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- db.PutBatch(PutEntry([]byte("second"), []byte("2")))
	}()

	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("second batch could not append while first I/O was blocked")
	}

	close(releaseFirst)
	for i, ch := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("batch %d: %v", i+1, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("batch %d did not finish", i+1)
		}
	}
	requireValue(t, db, "first", "1")
	requireValue(t, db, "second", "2")
}

func TestConcurrentPutBatches(t *testing.T) {
	db := openTestDB(t, nil)
	const goroutines = 24
	const keysPerBatch = 20

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			batch := db.NewBatch()
			for i := 0; i < keysPerBatch; i++ {
				key := fmt.Sprintf("key-%02d-%03d", g, i)
				value := fmt.Sprintf("value-%02d-%03d", g, i)
				batch.Put([]byte(key), []byte(value))
			}
			if err := batch.Commit(); err != nil {
				t.Errorf("commit %d: %v", g, err)
			}
		}(g)
	}
	wg.Wait()

	if db.Len() != goroutines*keysPerBatch {
		t.Fatalf("Len = %d, want %d", db.Len(), goroutines*keysPerBatch)
	}
	for g := 0; g < goroutines; g++ {
		for i := 0; i < keysPerBatch; i++ {
			key := fmt.Sprintf("key-%02d-%03d", g, i)
			value := fmt.Sprintf("value-%02d-%03d", g, i)
			requireValue(t, db, key, value)
		}
	}

	dir := db.opts.Dir
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if db2.Len() != goroutines*keysPerBatch {
		t.Fatalf("reopened Len = %d, want %d", db2.Len(), goroutines*keysPerBatch)
	}
}
