// Package batchkv is a small append-only key/value store with atomic batch
// commits. It uses tidwall/btree as its in-memory position index.
package batchkv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/tidwall/btree/v2"
)

const segmentID uint64 = 1

const (
	DefaultMaxKeyBytes   = 64 << 10
	DefaultMaxValueBytes = 16 << 20
	DefaultMaxBatchBytes = 64 << 20
)

var (
	ErrClosed   = errors.New("batchkv: database is closed")
	ErrNotFound = errors.New("batchkv: key not found")
)

// Location points an index entry at one record in a segment file.
type Location struct {
	Segment uint64
	Offset  int64
	Length  int
}

type segmentFile interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	Stat() (os.FileInfo, error)
}

// Options configures a batch key/value database.
type Options struct {
	// Dir is the directory containing segment files.
	Dir string

	MaxKeyBytes   int
	MaxValueBytes int
	MaxBatchBytes int

	// NoSync disables per-commit fsync. Close still fsyncs the segment.
	NoSync bool

	// openFile is used by tests to inject partial writes and blocking I/O.
	openFile func(name string, flag int, perm os.FileMode) (segmentFile, error)
}

// DB is a goroutine-safe segment-backed key/value store.
type DB struct {
	opts  Options
	path  string
	file  segmentFile
	index *btree.Map[string, Location]

	mu       sync.RWMutex
	cond     *sync.Cond
	wg       sync.WaitGroup
	closing  bool
	closed   bool
	writeOff int64
	nextSeq  uint64
	applySeq uint64
}

// Open opens or creates a database in dir.
func Open(dir string) (*DB, error) {
	return OpenOptions(Options{Dir: dir})
}

// OpenDB is an alias for Open.
func OpenDB(dir string) (*DB, error) {
	return Open(dir)
}

// NewDB is an alias for Open.
func NewDB(dir string) (*DB, error) {
	return Open(dir)
}

// OpenOptions opens or creates a database with explicit options.
func OpenOptions(opts Options) (*DB, error) {
	if opts.Dir == "" {
		return nil, errors.New("batchkv: Options.Dir is required")
	}
	if opts.MaxKeyBytes <= 0 {
		opts.MaxKeyBytes = DefaultMaxKeyBytes
	}
	if opts.MaxValueBytes <= 0 {
		opts.MaxValueBytes = DefaultMaxValueBytes
	}
	if opts.MaxBatchBytes <= 0 {
		opts.MaxBatchBytes = DefaultMaxBatchBytes
	}
	if opts.MaxBatchBytes < headerSize+commitSize {
		return nil, fmt.Errorf("batchkv: MaxBatchBytes must be at least %d", headerSize+commitSize)
	}
	if opts.openFile == nil {
		opts.openFile = func(name string, flag int, perm os.FileMode) (segmentFile, error) {
			return os.OpenFile(name, flag, perm)
		}
	}

	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(opts.Dir, segmentFileName)

	index := btree.NewMap[string, Location]()
	if err := replaySegment(path, index); err != nil {
		return nil, err
	}

	file, err := opts.openFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	db := &DB{
		opts:     opts,
		path:     path,
		file:     file,
		index:    index,
		writeOff: info.Size(),
	}
	db.cond = sync.NewCond(&db.mu)
	return db, nil
}

// Put writes one key/value pair atomically.
func (db *DB) Put(key, value []byte) error {
	return db.commit(PutEntry(key, value))
}

// Delete removes one key atomically.
func (db *DB) Delete(key []byte) error {
	return db.commit(DeleteEntry(key))
}

// PutBatch commits all entries as one atomic batch.
func (db *DB) PutBatch(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	return db.commit(entries...)
}

// BatchPut commits paired keys and values as one atomic batch.
func (db *DB) BatchPut(keys, values [][]byte) error {
	if len(keys) != len(values) {
		return fmt.Errorf("%w: %d keys and %d values", ErrShape, len(keys), len(values))
	}
	entries := make([]Entry, len(keys))
	for i := range keys {
		entries[i] = PutEntry(keys[i], values[i])
	}
	return db.PutBatch(entries...)
}

// Get returns the value for key. The returned slice is owned by the caller.
func (db *DB) Get(key []byte) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}

	loc, ok := db.index.Lookup(string(key))
	if !ok {
		return nil, ErrNotFound
	}

	recordBytes := make([]byte, loc.Length)
	if _, err := io.ReadFull(io.NewSectionReader(db.file, loc.Offset, int64(loc.Length)), recordBytes); err != nil {
		return nil, fmt.Errorf("batchkv: read record at %d: %w", loc.Offset, err)
	}
	_, _, value, err := decodeRecord(recordBytes)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), value...), nil
}

// Has reports whether key is present.
func (db *DB) Has(key []byte) (bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return false, ErrClosed
	}
	_, ok := db.index.Lookup(string(key))
	return ok, nil
}

// Len returns the number of live keys.
func (db *DB) Len() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.index.Len()
}

// Keys returns all live keys in sorted order.
func (db *DB) Keys() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	keys := make([]string, 0, db.index.Len())
	for key := range db.index.All() {
		keys = append(keys, key)
	}
	return keys
}

// Sync fsyncs the segment file.
func (db *DB) Sync() error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return ErrClosed
	}
	return db.file.Sync()
}

// Close waits for ordered commit completion and closes the segment.
func (db *DB) Close() error {
	db.mu.Lock()
	if db.closed {
		db.mu.Unlock()
		return nil
	}
	db.closing = true
	db.mu.Unlock()

	db.wg.Wait()

	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	syncErr := db.file.Sync()
	closeErr := db.file.Close()
	db.closed = true
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (db *DB) commit(entries ...Entry) error {
	// Encoding and all size validation happen before taking the commit lock.
	frame, records, err := encodeBatch(
		0, entries,
		db.opts.MaxKeyBytes, db.opts.MaxValueBytes, db.opts.MaxBatchBytes,
	)
	if err != nil {
		return err
	}

	db.mu.Lock()
	if db.closing || db.closed {
		db.mu.Unlock()
		return ErrClosed
	}
	db.wg.Add(1)
	seq := db.nextSeq
	db.nextSeq++
	offset := db.writeOff
	db.writeOff += int64(len(frame))
	binary.BigEndian.PutUint64(frame[8:16], seq)
	db.mu.Unlock()

	defer db.wg.Done()

	writeErr := db.persistFrame(frame, offset)

	db.mu.Lock()
	// Reservations are sequential, but disk writes are not. Wait for earlier
	// sequences so out-of-order writes cannot make an older index entry win.
	for db.applySeq < seq {
		db.cond.Wait()
	}

	if writeErr == nil {
		for _, rec := range records {
			loc := Location{
				Segment: segmentID,
				Offset:  offset + int64(rec.offset),
				Length:  rec.length,
			}
			if rec.op == opDelete {
				db.index.Delete(rec.key)
			} else {
				db.index.Set(rec.key, loc)
			}
		}
	}

	db.applySeq = seq + 1
	db.mu.Unlock()
	db.cond.Broadcast()
	return writeErr
}

func (db *DB) persistFrame(frame []byte, offset int64) error {
	if err := writeFullAt(db.file, frame, offset); err != nil {
		return err
	}
	if !db.opts.NoSync {
		if err := db.file.Sync(); err != nil {
			return err
		}
	}

	marker := commitMarker(frame)
	markerOffset := offset + int64(len(frame)-commitSize)
	if err := writeFullAt(db.file, marker, markerOffset); err != nil {
		return err
	}
	if !db.opts.NoSync {
		return db.file.Sync()
	}
	return nil
}

func writeFullAt(w io.WriterAt, p []byte, offset int64) error {
	for len(p) > 0 {
		n, err := w.WriteAt(p, offset)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
		offset += int64(n)
	}
	return nil
}

func replaySegment(path string, index *btree.Map[string, Location]) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	const chunkSize = 64 << 10
	chunk := make([]byte, chunkSize+len(batchMagic)-1)
	var scan int64

	for scan < size {
		n, readErr := file.ReadAt(chunk, scan)
		if n > 0 {
			if relative := bytes.Index(chunk[:n], batchMagic[:]); relative >= 0 {
				frameOff := scan + int64(relative)
				frameLen, records, ok := readFrameAt(file, frameOff, size)
				if !ok {
					// A torn or false magic leaves its bytes unreachable and
					// scanning continues after this candidate.
					scan = frameOff + 1
					continue
				}

				for _, rec := range records {
					loc := Location{
						Segment: segmentID,
						Offset:  frameOff + int64(rec.offset),
						Length:  rec.length,
					}
					if rec.op == opDelete {
						index.Delete(rec.key)
					} else {
						index.Set(rec.key, loc)
					}
				}
				scan = frameOff + int64(frameLen)
				continue
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
		if n < len(batchMagic) {
			break
		}
		advance := int64(n - len(batchMagic) + 1)
		if advance <= 0 {
			break
		}
		scan += advance
	}
	return nil
}

func readFrameAt(file io.ReaderAt, offset, size int64) (int, []record, bool) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, headerSize), header); err != nil {
		return 0, nil, false
	}
	if string(header[:len(batchMagic)]) != string(batchMagic[:]) {
		return 0, nil, false
	}

	recordsLen64 := binary.BigEndian.Uint64(header[20:28])
	if recordsLen64 > math.MaxInt {
		return 0, nil, false
	}
	remaining := size - offset
	if remaining < int64(headerSize+commitSize) {
		return 0, nil, false
	}
	if recordsLen64 > uint64(remaining-int64(headerSize+commitSize)) {
		return 0, nil, false
	}

	frameLen := headerSize + int(recordsLen64) + commitSize
	frame := make([]byte, frameLen)
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, int64(frameLen)), frame); err != nil {
		return 0, nil, false
	}
	return decodeCommittedFrame(frame)
}
