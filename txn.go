// https://github.com/tidwall/btree
//
// Copyright 2026 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package btree

import (
	"cmp"
	"errors"
	"iter"
)

// ErrTxnDone is returned when a transaction is committed or rolled back more
// than once.
var ErrTxnDone = errors.New("btree: transaction already completed")

// ErrTransactionDone is an alias for ErrTxnDone.
var ErrTransactionDone = ErrTxnDone

// adopt replaces the root of t with the root of src. The old root is released
// exactly once. This transfers ownership of src's root to t.
func (t *tree[K, V]) adopt(src *tree[K, V]) {
	if t.root != nil {
		t.releaseNode(t.root)
	}
	*t = *src
	src.root = nil
}

////////////////////////////////////////////////////////////////////////////////
// Map transaction
////////////////////////////////////////////////////////////////////////////////

// MapTxn accumulates modifications to a Map and publishes them together on
// Commit. Reads inside the transaction observe the transaction's own writes.
//
// A transaction is not safe for concurrent use.
type MapTxn[K cmp.Ordered, V any] struct {
	base   *Map[K, V]
	work   *Map[K, V]
	copies int
	done   bool
}

// MapTransaction is an alias for MapTxn.
type MapTransaction[K cmp.Ordered, V any] = MapTxn[K, V]

// Begin starts a write transaction.
//
// The transaction uses a private copy-on-write view of the map. Nodes copied
// by the first write to a path are reused by later writes in the same
// transaction, so a batch only copies each shared path once.
func (b *Map[K, V]) Begin() *MapTxn[K, V] {
	b.init()
	return &MapTxn[K, V]{base: b, work: b.Clone()}
}

// BeginTransaction is an alias for Begin.
func (b *Map[K, V]) BeginTransaction() *MapTxn[K, V] {
	return b.Begin()
}

// BeginTxn is an alias for Begin.
func (b *Map[K, V]) BeginTxn() *MapTxn[K, V] {
	return b.Begin()
}

// Transaction is an alias for Begin.
func (b *Map[K, V]) Transaction() *MapTxn[K, V] {
	return b.Begin()
}

// Done reports whether the transaction has been committed or rolled back.
func (tx *MapTxn[K, V]) Done() bool {
	return tx == nil || tx.done
}

// CopyCount returns the number of nodes copied by this transaction.
func (tx *MapTxn[K, V]) CopyCount() int {
	if tx == nil {
		return 0
	}
	if tx.done {
		return tx.copies
	}
	return tx.work.tree.copies
}

// Len returns the number of items visible to the transaction.
func (tx *MapTxn[K, V]) Len() int {
	if tx == nil || tx.done || tx.work == nil {
		return 0
	}
	return tx.work.Len()
}

// Insert adds a new item to the transaction.
func (tx *MapTxn[K, V]) Insert(key K, value V) (V, bool) {
	var empty V
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Insert(key, value)
}

// Replace replaces an existing item in the transaction.
func (tx *MapTxn[K, V]) Replace(key K, value V) (V, bool) {
	var empty V
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Replace(key, value)
}

// Set inserts or replaces an item in the transaction.
func (tx *MapTxn[K, V]) Set(key K, value V) (V, bool) {
	var empty V
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Set(key, value)
}

// Delete removes an item in the transaction.
func (tx *MapTxn[K, V]) Delete(key K) (V, bool) {
	var empty V
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Delete(key)
}

// Contains reports whether key exists in the transaction.
func (tx *MapTxn[K, V]) Contains(key K) bool {
	return tx != nil && !tx.done && tx.work != nil && tx.work.Contains(key)
}

// Lookup finds an item in the transaction.
func (tx *MapTxn[K, V]) Lookup(key K) (V, bool) {
	var empty V
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Lookup(key)
}

// Get returns the value for key, or the zero value when it does not exist.
func (tx *MapTxn[K, V]) Get(key K) V {
	value, _ := tx.Lookup(key)
	return value
}

// All iterates all items visible to the transaction.
func (tx *MapTxn[K, V]) All() iter.Seq2[K, V] {
	if tx == nil || tx.done || tx.work == nil {
		return func(func(K, V) bool) {}
	}
	return tx.work.All()
}

// Backward iterates all items visible to the transaction in reverse order.
func (tx *MapTxn[K, V]) Backward() iter.Seq2[K, V] {
	if tx == nil || tx.done || tx.work == nil {
		return func(func(K, V) bool) {}
	}
	return tx.work.Backward()
}

// Commit publishes every modification made by the transaction.
func (tx *MapTxn[K, V]) Commit() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.tree.copies
	tx.base.tree.adopt(&tx.work.tree)
	tx.work = nil
	tx.done = true
	return nil
}

// Rollback discards every modification made by the transaction.
func (tx *MapTxn[K, V]) Rollback() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.tree.copies
	tx.work.Release()
	tx.work = nil
	tx.done = true
	return nil
}

// Close rolls back the transaction when it is still active.
func (tx *MapTxn[K, V]) Close() error {
	if tx == nil || tx.done {
		return nil
	}
	return tx.Rollback()
}

////////////////////////////////////////////////////////////////////////////////
// Set transaction
////////////////////////////////////////////////////////////////////////////////

// SetTxn accumulates modifications to a Set and publishes them together on
// Commit. A transaction is not safe for concurrent use.
type SetTxn[K cmp.Ordered] struct {
	base   *Set[K]
	work   *Set[K]
	copies int
	done   bool
}

// SetTransaction is an alias for SetTxn.
type SetTransaction[K cmp.Ordered] = SetTxn[K]

// Begin starts a write transaction.
func (b *Set[K]) Begin() *SetTxn[K] {
	return &SetTxn[K]{base: b, work: b.Clone()}
}

// BeginTransaction is an alias for Begin.
func (b *Set[K]) BeginTransaction() *SetTxn[K] {
	return b.Begin()
}

// BeginTxn is an alias for Begin.
func (b *Set[K]) BeginTxn() *SetTxn[K] {
	return b.Begin()
}

// Transaction is an alias for Begin.
func (b *Set[K]) Transaction() *SetTxn[K] {
	return b.Begin()
}

// Done reports whether the transaction has been committed or rolled back.
func (tx *SetTxn[K]) Done() bool {
	return tx == nil || tx.done
}

// CopyCount returns the number of nodes copied by this transaction.
func (tx *SetTxn[K]) CopyCount() int {
	if tx == nil {
		return 0
	}
	if tx.done {
		return tx.copies
	}
	return tx.work.base.tree.copies
}

// Len returns the number of keys visible to the transaction.
func (tx *SetTxn[K]) Len() int {
	if tx == nil || tx.done || tx.work == nil {
		return 0
	}
	return tx.work.Len()
}

// Insert adds key to the transaction.
func (tx *SetTxn[K]) Insert(key K) bool {
	return tx != nil && !tx.done && tx.work != nil && tx.work.Insert(key)
}

// Delete removes key in the transaction.
func (tx *SetTxn[K]) Delete(key K) bool {
	return tx != nil && !tx.done && tx.work != nil && tx.work.Delete(key)
}

// Contains reports whether key exists in the transaction.
func (tx *SetTxn[K]) Contains(key K) bool {
	return tx != nil && !tx.done && tx.work != nil && tx.work.Contains(key)
}

// All iterates all keys visible to the transaction.
func (tx *SetTxn[K]) All() iter.Seq[K] {
	if tx == nil || tx.done || tx.work == nil {
		return func(func(K) bool) {}
	}
	return tx.work.All()
}

// Commit publishes every modification made by the transaction.
func (tx *SetTxn[K]) Commit() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.base.tree.copies
	tx.base.base.tree.adopt(&tx.work.base.tree)
	tx.work = nil
	tx.done = true
	return nil
}

// Rollback discards every modification made by the transaction.
func (tx *SetTxn[K]) Rollback() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.base.tree.copies
	tx.work.Release()
	tx.work = nil
	tx.done = true
	return nil
}

// Close rolls back the transaction when it is still active.
func (tx *SetTxn[K]) Close() error {
	if tx == nil || tx.done {
		return nil
	}
	return tx.Rollback()
}

////////////////////////////////////////////////////////////////////////////////
// Table transaction
////////////////////////////////////////////////////////////////////////////////

// TableTxn accumulates modifications to a Table and publishes them together on
// Commit. A transaction is not safe for concurrent use.
type TableTxn[T any] struct {
	base   *Table[T]
	work   *Table[T]
	copies int
	done   bool
}

// TableTransaction is an alias for TableTxn.
type TableTransaction[T any] = TableTxn[T]

// Begin starts a write transaction.
func (b *Table[T]) Begin() *TableTxn[T] {
	b.init()
	return &TableTxn[T]{base: b, work: b.Clone()}
}

// BeginTransaction is an alias for Begin.
func (b *Table[T]) BeginTransaction() *TableTxn[T] {
	return b.Begin()
}

// BeginTxn is an alias for Begin.
func (b *Table[T]) BeginTxn() *TableTxn[T] {
	return b.Begin()
}

// Transaction is an alias for Begin.
func (b *Table[T]) Transaction() *TableTxn[T] {
	return b.Begin()
}

// Done reports whether the transaction has been committed or rolled back.
func (tx *TableTxn[T]) Done() bool {
	return tx == nil || tx.done
}

// CopyCount returns the number of nodes copied by this transaction.
func (tx *TableTxn[T]) CopyCount() int {
	if tx == nil {
		return 0
	}
	if tx.done {
		return tx.copies
	}
	return tx.work.tree.copies
}

// Len returns the number of items visible to the transaction.
func (tx *TableTxn[T]) Len() int {
	if tx == nil || tx.done || tx.work == nil {
		return 0
	}
	return tx.work.Len()
}

// Insert adds item to the transaction.
func (tx *TableTxn[T]) Insert(item T) (T, bool) {
	var empty T
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Insert(item)
}

// Replace replaces an existing item in the transaction.
func (tx *TableTxn[T]) Replace(item T) (T, bool) {
	var empty T
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Replace(item)
}

// Set inserts or replaces an item in the transaction.
func (tx *TableTxn[T]) Set(item T) (T, bool) {
	var empty T
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Set(item)
}

// Delete removes item in the transaction.
func (tx *TableTxn[T]) Delete(item T) (T, bool) {
	var empty T
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Delete(item)
}

// Contains reports whether item exists in the transaction.
func (tx *TableTxn[T]) Contains(item T) bool {
	return tx != nil && !tx.done && tx.work != nil && tx.work.Contains(item)
}

// Lookup finds an item in the transaction.
func (tx *TableTxn[T]) Lookup(item T) (T, bool) {
	var empty T
	if tx == nil || tx.done || tx.work == nil {
		return empty, false
	}
	return tx.work.Lookup(item)
}

// Get returns the item matching key, or the zero value when it does not exist.
func (tx *TableTxn[T]) Get(key T) T {
	item, _ := tx.Lookup(key)
	return item
}

// All iterates all items visible to the transaction.
func (tx *TableTxn[T]) All() iter.Seq[T] {
	if tx == nil || tx.done || tx.work == nil {
		return func(func(T) bool) {}
	}
	return tx.work.All()
}

// Commit publishes every modification made by the transaction.
func (tx *TableTxn[T]) Commit() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.tree.copies
	tx.base.tree.adopt(&tx.work.tree)
	tx.work = nil
	tx.done = true
	return nil
}

// Rollback discards every modification made by the transaction.
func (tx *TableTxn[T]) Rollback() error {
	if tx == nil || tx.done {
		return ErrTxnDone
	}
	tx.copies = tx.work.tree.copies
	tx.work.Release()
	tx.work = nil
	tx.done = true
	return nil
}

// Close rolls back the transaction when it is still active.
func (tx *TableTxn[T]) Close() error {
	if tx == nil || tx.done {
		return nil
	}
	return tx.Rollback()
}
