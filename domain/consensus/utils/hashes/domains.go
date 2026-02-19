package hashes

import (
	"crypto/sha256"
	sha30 "crypto/sha3"
	"crypto/sha512"
	"hash"

	"golang.org/x/crypto/sha3"
	"lukechampine.com/blake3"
)

const (
	transcationHashDomain         = "TransactionHash"
	transcationIDDomain           = "TransactionID"
	transcationSigningDomain      = "TransactionSigningHash"
	transcationSigningECDSADomain = "TransactionSigningHashECDSA"
	blockDomain                   = "BlockHash"
	heavyHashDomain               = "HeavyHash"
	merkleBranchDomain            = "MerkleBranchHash"
)

// transactionSigningECDSADomainHash is a hashed version of transcationSigningECDSADomain that is used
// to make it a constant size. This is needed because this domain is used by sha256 hash writer, and
// sha256 doesn't support variable size domain separation.
var transactionSigningECDSADomainHash = sha256.Sum256([]byte(transcationSigningECDSADomain))

// NewTransactionHashWriter Returns a new HashWriter used for transaction hashes
func NewTransactionHashWriter() HashWriter {
	var fixedSizeKey [32]byte
	copy(fixedSizeKey[:], transcationHashDomain)
	blake := blake3.New(32, fixedSizeKey[:])
	return HashWriter{blake}
}

// func NewTransactionHashWriter() HashWriter {
// 	blake, err := blake2b.New256([]byte(transcationHashDomain))
// 	if err != nil {
// 		panic(errors.Wrapf(err, "this should never happen. %s is less than 64 bytes", transcationHashDomain))
// 	}
// 	return HashWriter{blake}
// }

// NewTransactionIDWriter Returns a new HashWriter used for transaction IDs
func NewTransactionIDWriter() HashWriter {
	var fixedSizeKey [32]byte
	copy(fixedSizeKey[:], transcationIDDomain)
	blake := blake3.New(32, fixedSizeKey[:])
	return HashWriter{blake}
}

// func NewTransactionIDWriter() HashWriter {
// 	blake, err := blake2b.New256([]byte(transcationIDDomain))
// 	if err != nil {
// 		panic(errors.Wrapf(err, "this should never happen. %s is less than 64 bytes", transcationIDDomain))
// 	}
// 	return HashWriter{blake}
// }

// NewTransactionSigningHashWriter Returns a new HashWriter used for signing on a transaction
func NewTransactionSigningHashWriter() HashWriter {
	var fixedSizeKey [32]byte
	copy(fixedSizeKey[:], transcationSigningDomain)
	blake := blake3.New(32, fixedSizeKey[:])
	return HashWriter{blake}
}

// func NewTransactionSigningHashWriter() HashWriter {
// 	blake, err := blake2b.New256([]byte(transcationSigningDomain))
// 	if err != nil {
// 		panic(errors.Wrapf(err, "this should never happen. %s is less than 64 bytes", transcationSigningDomain))
// 	}
// 	return HashWriter{blake}
// }

// NewTransactionSigningHashECDSAWriter Returns a new HashWriter used for signing on a transaction with ECDSA
func NewTransactionSigningHashECDSAWriter() HashWriter {
	hashWriter := HashWriter{sha256.New()}
	hashWriter.InfallibleWrite(transactionSigningECDSADomainHash[:])
	return hashWriter
}

// NewBlockHashWriter Returns a new HashWriter used for hashing blocks
func NewBlockHashWriter() HashWriter {
	var fixedSizeKey [32]byte
	copy(fixedSizeKey[:], blockDomain)
	blake := blake3.New(32, fixedSizeKey[:])
	return HashWriter{blake}
}

// func NewBlockHashWriter() HashWriter {
// 	blake, err := blake2b.New256([]byte(blockDomain))
// 	if err != nil {
// 		panic(errors.Wrapf(err, "this should never happen. %s is less than 64 bytes", blockDomain))
// 	}
// 	return HashWriter{blake}
// }

func SHA3512PowHashWriter() HashWriter {
	sha3512 := hash.Hash(sha30.New512())
	return HashWriter{sha3512}
}

func SHA512PowHashWriter() HashWriter {
	sha512 := sha512.New()
	return HashWriter{sha512}
}

// NewPoWHashWriter Returns a new HashWriter used for the PoW function
func PoWHashWriter() HashWriter {
	blake := blake3.New(32, nil)
	return HashWriter{blake}
}

// func NewPoWHashWriter() ShakeHashWriter {
// 	shake256 := sha3.NewCShake256(nil, []byte(proofOfWorkDomain))
// 	return ShakeHashWriter{shake256}
// }

// NewHeavyHashWriter Returns a new HashWriter used for the HeavyHash function
func BlakeHeavyHashWriter() HashWriter {
	blake := blake3.New(32, nil)
	return HashWriter{blake}
}

// BlakeHashWriter Returns a new HashWriter used for the HeavyHash function
func Blake3HashWriter() HashWriter {
	blake := blake3.New(32, nil)
	return HashWriter{blake}
}

// NewHeavyHashWriter Returns a new HashWriter used for the HeavyHash function
func KeccakHeavyHashWriter() ShakeHashWriter {
	shake256 := sha3.NewCShake256(nil, []byte(heavyHashDomain))
	return ShakeHashWriter{shake256}
}

// NewMerkleBranchHashWriter Returns a new HashWriter used for a merkle tree branch
func NewMerkleBranchHashWriter() HashWriter {
	var fixedSizeKey [32]byte
	copy(fixedSizeKey[:], merkleBranchDomain)
	blake := blake3.New(32, fixedSizeKey[:])
	return HashWriter{blake}
}

// func NewMerkleBranchHashWriter() HashWriter {
// 	blake, err := blake2b.New256([]byte(merkleBranchDomain))
// 	if err != nil {
// 		panic(errors.Wrapf(err, "this should never happen. %s is less than 64 bytes", merkleBranchDomain))
// 	}
// 	return HashWriter{blake}
// }
