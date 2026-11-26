package batchkv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
)

const (
	segmentFileName = "000000001.seg"

	headerSize       = 28
	commitSize       = 8
	recordHeaderSize = 9

	opPut    byte = 1
	opDelete byte = 2
)

var (
	batchMagic  = [8]byte{'B', 'T', 'K', 'V', 'B', 'A', 'T', '1'}
	commitMagic = [4]byte{'C', 'M', 'I', 'T'}
)

var (
	ErrKeyTooLarge   = errors.New("batchkv: key is too large")
	ErrValueTooLarge = errors.New("batchkv: value is too large")
	ErrBatchTooLarge = errors.New("batchkv: batch is too large")
	ErrShape         = errors.New("batchkv: keys and values must have the same length")
	ErrCorruptRecord = errors.New("batchkv: corrupt record")
)

// Entry is one Put or Delete operation in an atomic batch.
type Entry struct {
	Key    []byte
	Value  []byte
	Delete bool
}

// PutEntry returns a put operation.
func PutEntry(key, value []byte) Entry {
	return Entry{Key: key, Value: value}
}

// DeleteEntry returns a delete operation.
func DeleteEntry(key []byte) Entry {
	return Entry{Key: key, Delete: true}
}

// LimitError identifies the operation and limit that rejected a batch.
type LimitError struct {
	Index int
	Kind  string
	Size  int
	Limit int
	err   error
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("batchkv: %s limit at entry %d: size %d, limit %d", e.Kind, e.Index, e.Size, e.Limit)
}

func (e *LimitError) Unwrap() error {
	return e.err
}

type record struct {
	op     byte
	key    string
	offset int
	length int
}

func limitError(index int, kind string, size, limit int, err error) error {
	return &LimitError{Index: index, Kind: kind, Size: size, Limit: limit, err: err}
}

func encodeBatch(seq uint64, entries []Entry, maxKey, maxValue, maxBatch int) ([]byte, []record, error) {
	if len(entries) > math.MaxUint32 {
		return nil, nil, fmt.Errorf("%w: too many entries", ErrBatchTooLarge)
	}

	records := make([]record, len(entries))
	recordsLen := 0

	for i, entry := range entries {
		keyLen := len(entry.Key)
		if keyLen > maxKey {
			return nil, nil, limitError(i, "key", keyLen, maxKey, ErrKeyTooLarge)
		}

		valueLen := 0
		op := opPut
		if entry.Delete {
			op = opDelete
		} else {
			valueLen = len(entry.Value)
			if valueLen > maxValue {
				return nil, nil, limitError(i, "value", valueLen, maxValue, ErrValueTooLarge)
			}
		}

		recordLen := recordHeaderSize + keyLen + valueLen
		if recordsLen > math.MaxInt-recordLen {
			return nil, nil, fmt.Errorf("%w: records overflow", ErrBatchTooLarge)
		}
		if recordsLen+recordLen > maxBatch-headerSize-commitSize {
			return nil, nil, limitError(i, "batch", recordsLen+recordLen+headerSize+commitSize, maxBatch, ErrBatchTooLarge)
		}

		records[i] = record{
			op:     op,
			key:    string(entry.Key),
			offset: headerSize + recordsLen,
			length: recordLen,
		}
		recordsLen += recordLen
	}

	frameLen := headerSize + recordsLen + commitSize
	if frameLen > maxBatch {
		return nil, nil, fmt.Errorf("%w: encoded size %d exceeds %d", ErrBatchTooLarge, frameLen, maxBatch)
	}

	frame := make([]byte, frameLen)
	copy(frame[0:8], batchMagic[:])
	binary.BigEndian.PutUint64(frame[8:16], seq)
	binary.BigEndian.PutUint32(frame[16:20], uint32(len(entries)))
	binary.BigEndian.PutUint64(frame[20:28], uint64(recordsLen))

	pos := headerSize
	for i, entry := range entries {
		value := entry.Value
		if entry.Delete {
			value = nil
		}
		keyLen := len(entry.Key)
		valueLen := len(value)
		recordLen := recordHeaderSize + keyLen + valueLen

		frame[pos] = records[i].op
		binary.BigEndian.PutUint32(frame[pos+1:pos+5], uint32(keyLen))
		binary.BigEndian.PutUint32(frame[pos+5:pos+9], uint32(valueLen))
		copy(frame[pos+9:pos+9+keyLen], entry.Key)
		copy(frame[pos+9+keyLen:pos+recordLen], value)
		pos += recordLen
	}

	return frame, records, nil
}

func commitMarker(frame []byte) []byte {
	marker := make([]byte, commitSize)
	copy(marker[:4], commitMagic[:])
	crc := crc32.ChecksumIEEE(frame[:len(frame)-commitSize])
	binary.BigEndian.PutUint32(marker[4:8], crc)
	return marker
}

// decodeCommittedFrame validates one complete, explicitly committed batch.
// It returns the frame length and records with offsets relative to frame start.
func decodeCommittedFrame(src []byte) (int, []record, bool) {
	if len(src) < headerSize+commitSize {
		return 0, nil, false
	}
	if src[0] != batchMagic[0] || string(src[0:8]) != string(batchMagic[:]) {
		return 0, nil, false
	}

	count := binary.BigEndian.Uint32(src[16:20])
	recordsLen64 := binary.BigEndian.Uint64(src[20:28])
	if recordsLen64 > uint64(len(src)-headerSize-commitSize) {
		return 0, nil, false
	}
	recordsLen := int(recordsLen64)
	frameLen := headerSize + recordsLen + commitSize
	if frameLen > len(src) {
		return 0, nil, false
	}

	commitStart := frameLen - commitSize
	if string(src[commitStart:commitStart+4]) != string(commitMagic[:]) {
		return 0, nil, false
	}
	wantCRC := binary.BigEndian.Uint32(src[commitStart+4 : commitStart+8])
	gotCRC := crc32.ChecksumIEEE(src[:commitStart])
	if wantCRC != gotCRC {
		return 0, nil, false
	}

	recordsEnd := headerSize + recordsLen
	maxRecords := recordsLen / recordHeaderSize
	if count > uint32(maxRecords) {
		return 0, nil, false
	}
	records := make([]record, 0, count)
	pos := headerSize

	for i := uint32(0); i < count; i++ {
		if pos+recordHeaderSize > recordsEnd {
			return 0, nil, false
		}
		op := src[pos]
		if op != opPut && op != opDelete {
			return 0, nil, false
		}
		keyLen64 := binary.BigEndian.Uint32(src[pos+1 : pos+5])
		valueLen64 := binary.BigEndian.Uint32(src[pos+5 : pos+9])
		keyLen := int(keyLen64)
		valueLen := int(valueLen64)
		recordLen := recordHeaderSize + keyLen + valueLen
		if recordLen < recordHeaderSize || pos+recordLen > recordsEnd {
			return 0, nil, false
		}

		keyStart := pos + recordHeaderSize
		records = append(records, record{
			op:     op,
			key:    string(src[keyStart : keyStart+keyLen]),
			offset: pos,
			length: recordLen,
		})
		pos += recordLen
	}

	if pos != recordsEnd || uint32(len(records)) != count {
		return 0, nil, false
	}
	return frameLen, records, true
}

func decodeRecord(src []byte) (op byte, key, value []byte, err error) {
	if len(src) < recordHeaderSize {
		return 0, nil, nil, fmt.Errorf("%w: short record header", ErrCorruptRecord)
	}
	op = src[0]
	if op != opPut && op != opDelete {
		return 0, nil, nil, fmt.Errorf("%w: unknown operation %d", ErrCorruptRecord, op)
	}
	keyLen := int(binary.BigEndian.Uint32(src[1:5]))
	valueLen := int(binary.BigEndian.Uint32(src[5:9]))
	recordLen := recordHeaderSize + keyLen + valueLen
	if recordLen != len(src) {
		return 0, nil, nil, fmt.Errorf("%w: length mismatch", ErrCorruptRecord)
	}
	keyStart := recordHeaderSize
	valueStart := keyStart + keyLen
	key = src[keyStart:valueStart]
	value = src[valueStart:recordLen]
	return op, key, value, nil
}
