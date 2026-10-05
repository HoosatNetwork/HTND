package random

import (
	"crypto/rand"
	"encoding/binary"
)

var readRandom = rand.Read

// Uint64 returns a cryptographically random uint64 value.
func Uint64() (uint64, error) {
	var buf [8]byte
	_, err := readRandom(buf[:])
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}
