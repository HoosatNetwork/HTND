package pruningstore

import (
	"encoding/binary"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// pruningPointUTXOSetVerificationKeyName holds the marker recording whether the served
// pruning-point UTXO set matched its own header commitment the last time anyone looked. It is a
// marker only - it never gates what is served, and it is not consensus state. Written directly
// (no staging area), like the other diagnostic markers in this store.
var pruningPointUTXOSetVerificationKeyName = []byte("pruning-utxo-verified")

// Wire layout, all fixed-width so a partial write can be detected by length alone:
//
//	[0]         format version
//	[1]         status
//	[2]         presence bits: 0x01 per-block multiset, 0x02 diff-chain multiset
//	[3:35]      pruning point
//	[35:67]     header commitment
//	[67:99]     bucket multiset
//	[99:131]    per-block multiset   (zeroes when absent)
//	[131:163]   diff-chain multiset  (zeroes when absent)
//	[163:171]   entry count
//	[171:179]   checked-at DAA score
const (
	pruningPointUTXOSetVerificationVersion = 1

	pruningPointUTXOSetVerificationHasPerBlock  = 0x01
	pruningPointUTXOSetVerificationHasDiffChain = 0x02

	ppuvOffsetVersion    = 0
	ppuvOffsetStatus     = 1
	ppuvOffsetPresence   = 2
	ppuvOffsetPruningPt  = 3
	ppuvOffsetHeader     = ppuvOffsetPruningPt + externalapi.DomainHashSize
	ppuvOffsetBucket     = ppuvOffsetHeader + externalapi.DomainHashSize
	ppuvOffsetPerBlock   = ppuvOffsetBucket + externalapi.DomainHashSize
	ppuvOffsetDiffChain  = ppuvOffsetPerBlock + externalapi.DomainHashSize
	ppuvOffsetEntryCount = ppuvOffsetDiffChain + externalapi.DomainHashSize
	ppuvOffsetDAAScore   = ppuvOffsetEntryCount + 8
	ppuvSerializedLen    = ppuvOffsetDAAScore + 8
)

// SetPruningPointUTXOSetVerification persists the result of comparing the served pruning-point
// UTXO bucket against the pruning point's header commitment.
func (ps *pruningStore) SetPruningPointUTXOSetVerification(dbContext model.DBWriter,
	verification *model.PruningPointUTXOSetVerification,
) error {
	serialized, err := serializePruningPointUTXOSetVerification(verification)
	if err != nil {
		return err
	}
	return dbContext.Put(ps.pruningPointUTXOSetVerificationKey, serialized)
}

// PruningPointUTXOSetVerification returns the last recorded comparison, or a
// database.ErrNotFound-wrapping error if none has ever been written.
//
// The caller must check that the returned PruningPoint is the current one: a marker for a previous
// pruning point is stale, and reporting it as a current verdict is the exact mistake this marker
// exists to prevent.
func (ps *pruningStore) PruningPointUTXOSetVerification(dbContext model.DBReader) (
	*model.PruningPointUTXOSetVerification, error,
) {
	serialized, err := dbContext.Get(ps.pruningPointUTXOSetVerificationKey)
	if err != nil {
		return nil, err
	}
	return deserializePruningPointUTXOSetVerification(serialized)
}

func serializePruningPointUTXOSetVerification(verification *model.PruningPointUTXOSetVerification) ([]byte, error) {
	if verification == nil {
		return nil, errors.Errorf("cannot serialize a nil pruning point UTXO set verification")
	}
	if verification.PruningPoint == nil || verification.HeaderCommitment == nil || verification.BucketMultiset == nil {
		return nil, errors.Errorf("pruning point UTXO set verification is missing a required hash " +
			"(pruning point, header commitment and bucket multiset are all mandatory)")
	}

	serialized := make([]byte, ppuvSerializedLen)
	serialized[ppuvOffsetVersion] = pruningPointUTXOSetVerificationVersion
	serialized[ppuvOffsetStatus] = byte(verification.Status)

	copy(serialized[ppuvOffsetPruningPt:], verification.PruningPoint.ByteSlice())
	copy(serialized[ppuvOffsetHeader:], verification.HeaderCommitment.ByteSlice())
	copy(serialized[ppuvOffsetBucket:], verification.BucketMultiset.ByteSlice())

	presence := byte(0)
	if verification.PerBlockMultiset != nil {
		presence |= pruningPointUTXOSetVerificationHasPerBlock
		copy(serialized[ppuvOffsetPerBlock:], verification.PerBlockMultiset.ByteSlice())
	}
	if verification.DiffChainMultiset != nil {
		presence |= pruningPointUTXOSetVerificationHasDiffChain
		copy(serialized[ppuvOffsetDiffChain:], verification.DiffChainMultiset.ByteSlice())
	}
	serialized[ppuvOffsetPresence] = presence

	binary.LittleEndian.PutUint64(serialized[ppuvOffsetEntryCount:], verification.EntryCount)
	binary.LittleEndian.PutUint64(serialized[ppuvOffsetDAAScore:], verification.CheckedAtDAAScore)

	return serialized, nil
}

func deserializePruningPointUTXOSetVerification(serialized []byte) (
	*model.PruningPointUTXOSetVerification, error,
) {
	if len(serialized) != ppuvSerializedLen {
		return nil, errors.Errorf("serialized pruning point UTXO set verification has length %d, expected %d",
			len(serialized), ppuvSerializedLen)
	}
	if serialized[ppuvOffsetVersion] != pruningPointUTXOSetVerificationVersion {
		return nil, errors.Errorf("unsupported pruning point UTXO set verification format version %d",
			serialized[ppuvOffsetVersion])
	}

	hashAt := func(offset int) (*externalapi.DomainHash, error) {
		return externalapi.NewDomainHashFromByteSlice(serialized[offset : offset+externalapi.DomainHashSize])
	}

	pruningPoint, err := hashAt(ppuvOffsetPruningPt)
	if err != nil {
		return nil, err
	}
	headerCommitment, err := hashAt(ppuvOffsetHeader)
	if err != nil {
		return nil, err
	}
	bucketMultiset, err := hashAt(ppuvOffsetBucket)
	if err != nil {
		return nil, err
	}

	verification := &model.PruningPointUTXOSetVerification{
		PruningPoint:      pruningPoint,
		HeaderCommitment:  headerCommitment,
		BucketMultiset:    bucketMultiset,
		Status:            model.PruningPointUTXOSetStatus(serialized[ppuvOffsetStatus]),
		EntryCount:        binary.LittleEndian.Uint64(serialized[ppuvOffsetEntryCount:]),
		CheckedAtDAAScore: binary.LittleEndian.Uint64(serialized[ppuvOffsetDAAScore:]),
	}

	presence := serialized[ppuvOffsetPresence]
	if presence&pruningPointUTXOSetVerificationHasPerBlock != 0 {
		verification.PerBlockMultiset, err = hashAt(ppuvOffsetPerBlock)
		if err != nil {
			return nil, err
		}
	}
	if presence&pruningPointUTXOSetVerificationHasDiffChain != 0 {
		verification.DiffChainMultiset, err = hashAt(ppuvOffsetDiffChain)
		if err != nil {
			return nil, err
		}
	}

	return verification, nil
}
