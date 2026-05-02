package bip32

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type path struct {
	isPublic bool
	indexes  []uint32
}

func parsePath(pathString string) (*path, error) {
	parts := strings.Split(pathString, "/")
	isPublic := false
	switch parts[0] {
	case "m":
		isPublic = false
	case "M":
		isPublic = true
	default:
		return nil, errors.Errorf("%s is an invalid extended key type", parts[0])
	}

	indexParts := parts[1:]
	indexes := make([]uint32, len(indexParts))
	for i, part := range indexParts {
		var err error
		indexes[i], err = parseIndex(part)
		if err != nil {
			return nil, err
		}
	}

	return &path{
		isPublic: isPublic,
		indexes:  indexes,
	}, nil
}

func parseIndex(indexString string) (uint32, error) {
	const isHardenedSuffix = "'"
	isHardened := strings.HasSuffix(indexString, isHardenedSuffix)
	trimmedIndexString := strings.TrimSuffix(indexString, isHardenedSuffix)
	index, err := strconv.Atoi(trimmedIndexString)
	if err != nil {
		return 0, err
	}

	if index >= hardenedIndexStart {
		return 0, errors.Errorf("max index value is %d but got %d", hardenedIndexStart, index)
	}

	if isHardened {
		if index < 0 {
			return 0, errors.Errorf("index cannot be negative: %d", index)
		}
		if uint64(index)+uint64(hardenedIndexStart) > uint64(^uint32(0)) {
			return 0, errors.Errorf("index + hardenedIndexStart overflows uint32: %d + %d", index, hardenedIndexStart)
		}
		return uint32(index) + hardenedIndexStart, nil
	}

	if index < 0 {
		return 0, errors.Errorf("index cannot be negative: %d", index)
	}
	if index > int(^uint32(0)) {
		return 0, errors.Errorf("index overflows uint32: %d", index)
	}
	return uint32(index), nil
}
