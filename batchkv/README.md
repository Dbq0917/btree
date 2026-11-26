# batchkv

The main `btree` package is in-memory only and has no `Put`, segment file, or
recovery path. This subpackage provides the smallest append-only segment store
needed to specify and verify atomic batch commits on top of `btree.Map`.

## API

```go
db, err := batchkv.Open("./data")
defer db.Close()

err = db.Put([]byte("k"), []byte("v"))
err = db.PutBatch(
    batchkv.PutEntry([]byte("a"), []byte("1")),
    batchkv.PutEntry([]byte("b"), []byte("2")),
)

b := db.NewBatch()
b.Put([]byte("c"), []byte("3")).Delete([]byte("old"))
err = b.Commit()
```

## Commit protocol

- Size and shape validation and frame encoding happen before taking the commit
  lock; an oversized value writes no bytes.
- A short critical section assigns the sequence number and reserves a
  contiguous segment range.
- The full batch frame is written with `WriteAt` outside the commit lock.
  Concurrent batches use disjoint offsets, so a slow or large write does not
  hold the global lock.
- An 8-byte `CMIT` marker with a CRC is written only after all record bytes
  are stable. The in-memory B-tree position index is updated only after that
  marker write succeeds.
- If a write fails midway, the reserved range is never indexed. Later writes
  start after the reservation, leaving the partial bytes as an unreachable
  hole.
- On open, recovery accepts only CRC-valid frames with a commit marker and
  scans past torn writes or holes to find later committed frames.
- Sequence-ordered index application prevents an out-of-order disk write from
  making an older batch overwrite a newer key.

Run the focused checks with:

```sh
go test -race ./batchkv -count=1
go test ./... -count=1
```
