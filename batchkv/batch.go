package batchkv

// Batch accumulates writes before one atomic Commit.
type Batch struct {
	db      *DB
	entries []Entry
}

// WriteBatch is an alternate name for Batch.
type WriteBatch = Batch

// NewBatch creates an accumulated batch bound to db.
func (db *DB) NewBatch() *Batch {
	return &Batch{db: db}
}

// NewWriteBatch creates an accumulated write batch bound to db.
func (db *DB) NewWriteBatch() *WriteBatch {
	return db.NewBatch()
}

// Put appends a put operation. Key and value are copied so later caller
// mutations cannot change the staged operation.
func (b *Batch) Put(key, value []byte) *Batch {
	keyCopy := append([]byte(nil), key...)
	valueCopy := append([]byte(nil), value...)
	b.entries = append(b.entries, PutEntry(keyCopy, valueCopy))
	return b
}

// Delete appends a delete operation.
func (b *Batch) Delete(key []byte) *Batch {
	keyCopy := append([]byte(nil), key...)
	b.entries = append(b.entries, DeleteEntry(keyCopy))
	return b
}

// Entries returns a copy of the staged entries.
func (b *Batch) Entries() []Entry {
	return append([]Entry(nil), b.entries...)
}

// Len returns the number of staged operations.
func (b *Batch) Len() int {
	return len(b.entries)
}

// Reset discards staged operations without committing them.
func (b *Batch) Reset() {
	b.entries = b.entries[:0]
}

// Commit atomically applies all staged operations.
func (b *Batch) Commit() error {
	if b.db == nil {
		return ErrClosed
	}
	return b.db.PutBatch(b.entries...)
}
