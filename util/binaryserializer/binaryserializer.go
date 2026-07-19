package binaryserializer

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/pkg/errors"
)

// Use sync.Pool instead of a channel to drastically cut down heap allocations.
var freeListPool = sync.Pool{
	New: func() any {
		b := make([]byte, 8)
		return &b
	},
}

// Borrow returns a byte slice from the free list with a length of 8.
func Borrow() []byte {
	ptr := freeListPool.Get().(*[]byte)
	return (*ptr)[:8]
}

// Return puts the provided byte slice back on the free list.
func Return(buf []byte) {
	// Re-slice to capacity to preserve the full 8 bytes for the next borrow
	buf = buf[:8]
	freeListPool.Put(&buf)
}

// Uint8 reads a single byte from the provided reader using a buffer from the
// free list and returns it as a uint8.
func Uint8(r io.Reader) (uint8, error) {
	buf := Borrow()[:1]
	if _, err := io.ReadFull(r, buf); err != nil {
		Return(buf)
		return 0, errors.WithStack(err)
	}
	rv := buf[0]
	Return(buf)
	return rv, nil
}

// Uint16 reads two bytes from the provided reader using a buffer from the
// free list, converts it to a number using the provided byte order, and returns
// the resulting uint16.
func Uint16(r io.Reader) (uint16, error) {
	buf := Borrow()[:2]
	if _, err := io.ReadFull(r, buf); err != nil {
		Return(buf)
		return 0, errors.WithStack(err)
	}
	rv := binary.LittleEndian.Uint16(buf)
	Return(buf)
	return rv, nil
}

// Uint32 reads four bytes from the provided reader using a buffer from the
// free list, converts it to a number using the provided byte order, and returns
// the resulting uint32.
func Uint32(r io.Reader) (uint32, error) {
	buf := Borrow()[:4]
	if _, err := io.ReadFull(r, buf); err != nil {
		Return(buf)
		return 0, errors.WithStack(err)
	}
	rv := binary.LittleEndian.Uint32(buf)
	Return(buf)
	return rv, nil
}

// Uint64 reads eight bytes from the provided reader using a buffer from the
// free list, converts it to a number using the provided byte order, and returns
// the resulting uint64.
func Uint64(r io.Reader) (uint64, error) {
	buf := Borrow()[:8]
	if _, err := io.ReadFull(r, buf); err != nil {
		Return(buf)
		return 0, errors.WithStack(err)
	}
	rv := binary.LittleEndian.Uint64(buf)
	Return(buf)
	return rv, nil
}

// PutUint8 copies the provided uint8 into a local stack array and
// writes the resulting byte to the given writer.
func PutUint8(w io.Writer, val uint8) error {
	var buf [1]byte
	buf[0] = val
	if _, err := w.Write(buf[:]); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// PutUint16 serializes the provided uint16 using the given byte order into a
// local stack array and writes the resulting two bytes to the given writer.
func PutUint16(w io.Writer, val uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], val)
	if _, err := w.Write(buf[:]); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// PutUint32 serializes the provided uint32 using the given byte order into a
// local stack array and writes the resulting four bytes to the given writer.
func PutUint32(w io.Writer, val uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], val)
	if _, err := w.Write(buf[:]); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

// PutUint64 serializes the provided uint64 using the given byte order into a
// local stack array and writes the resulting eight bytes to the given writer.
func PutUint64(w io.Writer, val uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], val)
	if _, err := w.Write(buf[:]); err != nil {
		return errors.WithStack(err)
	}
	return nil
}
