package keys

import (
	"bufio"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gofrs/flock"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var defaultAppDir = util.AppDir("htnwallet", false)

// LastVersion is the most up to date file format version
const LastVersion = 1

func defaultKeysFile(netParams *dagconfig.Params) string {
	return filepath.Join(defaultAppDir, netParams.Name, "keys.json")
}

type encryptedPrivateKeyJSON struct {
	Cipher string `json:"cipher"`
	Salt   string `json:"salt"`
}

type keysFileJSON struct {
	Version               uint32                     `json:"version"`
	NumThreads            uint8                      `json:"numThreads,omitempty"` // This field is ignored for versions different from 0. See more details at the function `numThreads`.
	EncryptedPrivateKeys  []*encryptedPrivateKeyJSON `json:"encryptedMnemonics"`
	ExtendedPublicKeys    []string                   `json:"publicKeys"`
	MinimumSignatures     uint32                     `json:"minimumSignatures"`
	CosignerIndex         uint32                     `json:"cosignerIndex"`
	LastUsedExternalIndex uint32                     `json:"lastUsedExternalIndex"`
	LastUsedInternalIndex uint32                     `json:"lastUsedInternalIndex"`
	ECDSA                 bool                       `json:"ecdsa"`
	// Imported is left out of an htnwallet wallet's file, so such a file is written exactly as before
	// imported wallets existed. Older htnwallet versions reject an imported wallet's file.
	Imported         *importJSON          `json:"imported,omitempty"`
	MLDSA44          *mldsa44KeyPoolJSON  `json:"mldsa44,omitempty"`
	MLDSA44Cosigners mldsa44CosignersJSON `json:"mldsa44Cosigners,omitempty"`
}

// mldsa44CosignersJSON maps a cosigner's master extended public key to its ML-DSA-44 key pool.
type mldsa44CosignersJSON map[string]*mldsa44KeyPoolJSON

type mldsa44KeyPoolJSON struct {
	ExternalPublicKeyHashes []string `json:"externalPublicKeyHashes"`
	InternalPublicKeyHashes []string `json:"internalPublicKeyHashes"`
}

// MLDSA44KeyPool holds the hashes of a single-sig wallet's ML-DSA-44 public keys, indexed by
// address index, one list per key chain. See libhtnwallet.MLDSA44PublicKeyHashes for why the wallet
// stores these rather than deriving them.
type MLDSA44KeyPool struct {
	ExternalPublicKeyHashes [][]byte
	InternalPublicKeyHashes [][]byte
}

// PublicKeyHash returns the ML-DSA-44 public key hash at index of keychain, and false when the pool
// does not reach that far.
func (p *MLDSA44KeyPool) PublicKeyHash(keychain uint8, index uint32) ([]byte, bool) {
	if p == nil {
		return nil, false
	}
	hashes := p.ExternalPublicKeyHashes
	if keychain == libhtnwallet.InternalKeychain {
		hashes = p.InternalPublicKeyHashes
	}
	if uint64(index) >= uint64(len(hashes)) {
		return nil, false
	}
	return hashes[index], true
}

// Size returns the number of indexes the pool covers on both key chains.
func (p *MLDSA44KeyPool) Size() uint32 {
	if p == nil {
		return 0
	}
	return uint32(min(len(p.ExternalPublicKeyHashes), len(p.InternalPublicKeyHashes)))
}

// MLDSA44AddressIndexLimit is the exclusive upper bound on the wallet's address indexes.
// The wallet's public address derivation and scans stop before the BIP32 hardened range.
const MLDSA44AddressIndexLimit = 1 << 31

// DefaultMLDSA44KeyCount returns the exclusive end of a default pool. A wallet with recorded
// use gets up to DefaultMLDSA44KeyPoolSize future indexes. Without recorded use it gets the
// same pool size as a new wallet. limited reports whether the address index limit cuts into
// the future indexes.
func (d *File) DefaultMLDSA44KeyCount() (count uint32, limited bool) {
	highestUsedIndex := uint32(0)
	for _, lastUsedIndex := range []uint32{d.lastUsedExternalIndex, d.lastUsedInternalIndex} {
		if lastUsedIndex < MLDSA44AddressIndexLimit {
			highestUsedIndex = max(highestUsedIndex, lastUsedIndex)
		}
	}
	if highestUsedIndex == 0 {
		return libhtnwallet.DefaultMLDSA44KeyPoolSize, false
	}
	end := highestUsedIndex + 1 + libhtnwallet.DefaultMLDSA44KeyPoolSize
	return min(end, MLDSA44AddressIndexLimit), end > MLDSA44AddressIndexLimit
}

// NewMLDSA44KeyPool computes the ML-DSA-44 key pool for indexes [0, size) of both key chains.
// multisig selects the mnemonic's multisig cosigner keys.
func NewMLDSA44KeyPool(mnemonic string, size uint32, multisig bool) (*MLDSA44KeyPool, error) {
	return (&MLDSA44KeyPool{}).Extend(mnemonic, size, multisig)
}

// Extend returns a pool with at least size hashes on each chain, deriving only missing
// suffixes. It preserves hashes already held, including on a longer chain.
func (p *MLDSA44KeyPool) Extend(mnemonic string, size uint32, multisig bool) (*MLDSA44KeyPool, error) {
	if size > MLDSA44AddressIndexLimit || uint64(size) > uint64(math.MaxInt) {
		return nil, errors.Errorf("ML-DSA-44 key count %d exceeds the supported address range", size)
	}
	if p == nil {
		p = &MLDSA44KeyPool{}
	}
	extend := func(existing [][]byte, chain uint8) ([][]byte, error) {
		if len(existing) >= int(size) {
			return existing, nil
		}
		start := uint32(len(existing))
		suffix, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, chain, start, size-start, multisig)
		if err != nil || start == 0 {
			return suffix, err
		}
		return append(existing, suffix...), nil
	}
	external, err := extend(p.ExternalPublicKeyHashes, libhtnwallet.ExternalKeychain)
	if err != nil {
		return nil, err
	}
	internal, err := extend(p.InternalPublicKeyHashes, libhtnwallet.InternalKeychain)
	if err != nil {
		return nil, err
	}
	return &MLDSA44KeyPool{ExternalPublicKeyHashes: external, InternalPublicKeyHashes: internal}, nil
}

// RebuildCosigner derives both chains of a local cosigner pool from the mnemonic, covering
// size indexes or the stored range of a chain if that is longer.
// matches reports whether the stored hashes equal the derived ones.
func (p *MLDSA44KeyPool) RebuildCosigner(mnemonic string, size uint32) (rebuilt *MLDSA44KeyPool, matches bool, err error) {
	if p == nil {
		rebuilt, err = NewMLDSA44KeyPool(mnemonic, size, true)
		return rebuilt, err == nil, err
	}
	derive := func(chain uint8, existing [][]byte) ([][]byte, error) {
		count := max(uint64(size), uint64(len(existing)))
		if count > MLDSA44AddressIndexLimit || count > uint64(math.MaxInt) {
			return nil, errors.Errorf("ML-DSA-44 key count %d exceeds the supported address range", count)
		}
		return libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, chain, 0, uint32(count), true)
	}
	receive, err := derive(libhtnwallet.ExternalKeychain, p.ExternalPublicKeyHashes)
	if err != nil {
		return nil, false, err
	}
	change, err := derive(libhtnwallet.InternalKeychain, p.InternalPublicKeyHashes)
	if err != nil {
		return nil, false, err
	}
	rebuilt = &MLDSA44KeyPool{ExternalPublicKeyHashes: receive, InternalPublicKeyHashes: change}
	return rebuilt, p.agreesWith(rebuilt), nil
}

// MLDSA44MultiSigPublicKeyHashes returns every cosigner's ML-DSA-44 public key hash at index of
// keychain, keyed by the cosigner's extended public key, or an error naming the first cosigner whose
// keys have not been imported or do not reach that far.
func (d *File) MLDSA44MultiSigPublicKeyHashes(keychain uint8, index uint32) (map[string][]byte, error) {
	hashes := make(map[string][]byte, len(d.ExtendedPublicKeys))
	for _, extendedPublicKey := range d.ExtendedPublicKeys {
		pool, ok := d.MLDSA44Cosigners[extendedPublicKey]
		if !ok {
			return nil, errors.Errorf("the ML-DSA-44 keys of cosigner %s have not been imported", extendedPublicKey)
		}
		hash, ok := pool.PublicKeyHash(keychain, index)
		if !ok {
			return nil, errors.Errorf("the ML-DSA-44 keys of cosigner %s cover indexes below %d only, and index %d was requested",
				extendedPublicKey, pool.Size(), index)
		}
		hashes[extendedPublicKey] = hash
	}
	return hashes, nil
}

func mldsa44CosignersToJSON(cosigners map[string]*MLDSA44KeyPool) mldsa44CosignersJSON {
	if len(cosigners) == 0 {
		return nil
	}
	cosignersJSON := make(mldsa44CosignersJSON, len(cosigners))
	for extendedPublicKey, pool := range cosigners {
		cosignersJSON[extendedPublicKey] = pool.toJSON()
	}
	return cosignersJSON
}

func mldsa44CosignersFromJSON(cosignersJSON mldsa44CosignersJSON) (map[string]*MLDSA44KeyPool, error) {
	if len(cosignersJSON) == 0 {
		return nil, nil
	}
	cosigners := make(map[string]*MLDSA44KeyPool, len(cosignersJSON))
	for extendedPublicKey, poolJSON := range cosignersJSON {
		pool, err := mldsa44KeyPoolFromJSON(poolJSON)
		if err != nil {
			return nil, errors.Wrap(err, "invalid ML-DSA-44 cosigner key pool")
		}
		if pool == nil {
			return nil, errors.New("empty ML-DSA-44 cosigner key pool")
		}
		cosigners[extendedPublicKey] = pool
	}
	return cosigners, nil
}

func (p *MLDSA44KeyPool) toJSON() *mldsa44KeyPoolJSON {
	if p == nil {
		return nil
	}
	encode := func(hashes [][]byte) []string {
		encoded := make([]string, len(hashes))
		for i, hash := range hashes {
			encoded[i] = hex.EncodeToString(hash)
		}
		return encoded
	}
	return &mldsa44KeyPoolJSON{
		ExternalPublicKeyHashes: encode(p.ExternalPublicKeyHashes),
		InternalPublicKeyHashes: encode(p.InternalPublicKeyHashes),
	}
}

func mldsa44KeyPoolFromJSON(poolJSON *mldsa44KeyPoolJSON) (*MLDSA44KeyPool, error) {
	if poolJSON == nil {
		return nil, nil
	}
	decode := func(encoded []string) ([][]byte, error) {
		hashes := make([][]byte, len(encoded))
		for i, hashHex := range encoded {
			hash, err := hex.DecodeString(hashHex)
			if err != nil {
				return nil, err
			}
			if len(hash) != 32 {
				return nil, errors.Errorf("ML-DSA-44 public key hash #%d is %d bytes, expected 32", i, len(hash))
			}
			hashes[i] = hash
		}
		return hashes, nil
	}
	external, err := decode(poolJSON.ExternalPublicKeyHashes)
	if err != nil {
		return nil, err
	}
	internal, err := decode(poolJSON.InternalPublicKeyHashes)
	if err != nil {
		return nil, err
	}
	return &MLDSA44KeyPool{ExternalPublicKeyHashes: external, InternalPublicKeyHashes: internal}, nil
}

// EncryptedMnemonic represents an encrypted mnemonic
type EncryptedMnemonic struct {
	cipher []byte
	salt   []byte
}

// File holds all the data related to the wallet keys
type File struct {
	Version               uint32
	NumThreads            uint8 // This field is ignored for versions different than 0
	EncryptedMnemonics    []*EncryptedMnemonic
	ExtendedPublicKeys    []string
	MinimumSignatures     uint32
	CosignerIndex         uint32
	lastUsedExternalIndex uint32
	lastUsedInternalIndex uint32
	ECDSA                 bool
	Imported              *Import // Set for an imported wallet, which then has no mnemonics or extended public keys
	// MLDSA44 is nil for multisig wallets and for wallets created before ML-DSA-44 support; see
	// the generate-mldsa44-keys command.
	MLDSA44 *MLDSA44KeyPool
	// MLDSA44Cosigners holds a multisig wallet's ML-DSA-44 key pools, one per cosigner, keyed by the
	// cosigner's entry in ExtendedPublicKeys. The wallet's own are generated from its mnemonics; the
	// others are imported from the cosigners (export-mldsa44-keys / import-mldsa44-keys), because
	// ML-DSA-44 keys cannot be derived from an extended public key.
	MLDSA44Cosigners map[string]*MLDSA44KeyPool
	path             string
}

// KnownLocalCosignerKeys returns the mnemonic-backed extended public keys when
// they can be identified without decrypting. CosignerIndex records the first
// local key's position in sorted order, even if a daemon reordered the file.
func (d *File) KnownLocalCosignerKeys() ([]string, bool) {
	switch n := len(d.EncryptedMnemonics); {
	case n == 0:
		return nil, true
	case n == len(d.ExtendedPublicKeys):
		return d.ExtendedPublicKeys, true
	case n == 1 && uint64(d.CosignerIndex) < uint64(len(d.ExtendedPublicKeys)):
		sortedKeys := slices.Sorted(slices.Values(d.ExtendedPublicKeys))
		return sortedKeys[d.CosignerIndex : d.CosignerIndex+1], true
	default:
		return nil, false
	}
}

func (d *File) toJSON() *keysFileJSON {
	encryptedPrivateKeysJSON := make([]*encryptedPrivateKeyJSON, len(d.EncryptedMnemonics))
	for i, encryptedPrivateKey := range d.EncryptedMnemonics {
		encryptedPrivateKeysJSON[i] = &encryptedPrivateKeyJSON{
			Cipher: hex.EncodeToString(encryptedPrivateKey.cipher),
			Salt:   hex.EncodeToString(encryptedPrivateKey.salt),
		}
	}

	return &keysFileJSON{
		Version:               d.Version,
		NumThreads:            d.NumThreads,
		EncryptedPrivateKeys:  encryptedPrivateKeysJSON,
		ExtendedPublicKeys:    d.ExtendedPublicKeys,
		MinimumSignatures:     d.MinimumSignatures,
		ECDSA:                 d.ECDSA,
		CosignerIndex:         d.CosignerIndex,
		LastUsedExternalIndex: d.lastUsedExternalIndex,
		LastUsedInternalIndex: d.lastUsedInternalIndex,
		Imported:              importToJSON(d.Imported),
		MLDSA44:               d.MLDSA44.toJSON(),
		MLDSA44Cosigners:      mldsa44CosignersToJSON(d.MLDSA44Cosigners),
	}
}

// NewFileFromMnemonic generates a new File from the given mnemonic string
func NewFileFromMnemonic(params *dagconfig.Params, mnemonic string, password string) (*File, error) {
	encryptedMnemonics, extendedPublicKeys, mldsa44KeyPools, err := encryptedMnemonicExtendedPublicKeyPairs(params, []string{mnemonic}, password, false)
	if err != nil {
		return nil, err
	}
	mldsa44KeyPool := mldsa44KeyPools[extendedPublicKeys[0]]
	return &File{
		Version:            LastVersion,
		NumThreads:         defaultNumThreads,
		EncryptedMnemonics: encryptedMnemonics,
		ExtendedPublicKeys: extendedPublicKeys,
		MinimumSignatures:  1,
		ECDSA:              false,
		MLDSA44:            mldsa44KeyPool,
	}, nil
}

func (d *File) fromJSON(fileJSON *keysFileJSON) error {
	d.Version = fileJSON.Version
	d.NumThreads = fileJSON.NumThreads
	d.MinimumSignatures = fileJSON.MinimumSignatures
	d.ECDSA = fileJSON.ECDSA
	d.ExtendedPublicKeys = fileJSON.ExtendedPublicKeys
	d.CosignerIndex = fileJSON.CosignerIndex
	d.lastUsedExternalIndex = fileJSON.LastUsedExternalIndex
	d.lastUsedInternalIndex = fileJSON.LastUsedInternalIndex

	imported, err := importFromJSON(fileJSON.Imported)
	if err != nil {
		return err
	}
	if imported != nil && (len(fileJSON.EncryptedPrivateKeys) > 0 || len(fileJSON.ExtendedPublicKeys) > 0) {
		return errors.New("the keys file holds both an imported wallet and htnwallet keys; " +
			"a keys file holds one wallet")
	}
	d.Imported = imported
	mldsa44KeyPool, err := mldsa44KeyPoolFromJSON(fileJSON.MLDSA44)
	if err != nil {
		return err
	}
	d.MLDSA44 = mldsa44KeyPool

	mldsa44Cosigners, err := mldsa44CosignersFromJSON(fileJSON.MLDSA44Cosigners)
	if err != nil {
		return err
	}
	d.MLDSA44Cosigners = mldsa44Cosigners

	d.EncryptedMnemonics = make([]*EncryptedMnemonic, len(fileJSON.EncryptedPrivateKeys))
	for i, encryptedPrivateKeyJSON := range fileJSON.EncryptedPrivateKeys {
		cipher, err := hex.DecodeString(encryptedPrivateKeyJSON.Cipher)
		if err != nil {
			return err
		}

		salt, err := hex.DecodeString(encryptedPrivateKeyJSON.Salt)
		if err != nil {
			return err
		}

		d.EncryptedMnemonics[i] = &EncryptedMnemonic{
			cipher: cipher,
			salt:   salt,
		}
	}

	return nil
}

// SetPath sets the path where the file is saved to.
func (d *File) SetPath(params *dagconfig.Params, path string, forceOverride bool) error {
	if path == "" {
		path = defaultKeysFile(params)
	}

	if !forceOverride {
		exists, err := pathExists(path)
		if err != nil {
			return err
		}

		if exists {
			reader := bufio.NewReader(os.Stdin)
			fmt.Printf("The file %s already exists. Are you sure you want to override it (type 'y' to approve)? ", d.path)
			line, err := utils.ReadLine(reader)
			if err != nil {
				return err
			}

			if string(line) != "y" {
				return errors.Errorf("aborted setting the file path to %s", path)
			}
		}
	}
	d.path = path
	return nil
}

// Path returns the file path.
func (d *File) Path() string {
	return d.path
}

// SetLastUsedExternalIndex sets the last used index in the external key
// chain, and saves the file with the updated data.
func (d *File) SetLastUsedExternalIndex(index uint32) error {
	return d.setLastUsedIndex(&d.lastUsedExternalIndex, index)
}

// LastUsedExternalIndex returns the last used index in the external key
// chain and saves the file with the updated data.
func (d *File) LastUsedExternalIndex() uint32 {
	return d.lastUsedExternalIndex
}

// SetLastUsedInternalIndex sets the last used index in the internal key chain, and saves the file.
func (d *File) SetLastUsedInternalIndex(index uint32) error {
	return d.setLastUsedIndex(&d.lastUsedInternalIndex, index)
}

func (d *File) setLastUsedIndex(current *uint32, index uint32) error {
	if *current == index {
		return nil
	}

	previous := *current
	*current = index
	renamed, err := d.save()
	// After the rename, readers see the new index even if syncing the directory fails.
	if !renamed {
		*current = previous
	}
	return err
}

// ReserveInternalIndex saves an index before issuing its address. If the save fails, the
// address is still unissued and the daemon can retry the same index, even if the replacement
// file is already visible after a failed directory sync.
func (d *File) ReserveInternalIndex(index uint32) error {
	previous := d.lastUsedInternalIndex
	if err := d.SetLastUsedInternalIndex(index); err != nil {
		d.lastUsedInternalIndex = previous
		return err
	}
	return nil
}

// LastUsedInternalIndex returns the last used index in the internal key chain
func (d *File) LastUsedInternalIndex() uint32 {
	return d.lastUsedInternalIndex
}

// DecryptMnemonics returns the decrypted mnemonics without writing the keys file.
// A caller that holds the lock may save the detected legacy thread count with its updates.
func (d *File) DecryptMnemonics(password string) ([]string, error) {
	passwordBytes := []byte(password)

	var numThreads uint8
	if len(d.EncryptedMnemonics) > 0 {
		var err error
		numThreads, err = d.numThreads(passwordBytes)
		if err != nil {
			return nil, err
		}
	}

	privateKeys := make([]string, len(d.EncryptedMnemonics))
	for i, encryptedPrivateKey := range d.EncryptedMnemonics {
		var err error
		privateKeys[i], err = decryptMnemonic(numThreads, encryptedPrivateKey, passwordBytes)
		if err != nil {
			return nil, err
		}
	}

	return privateKeys, nil
}

// ReadKeysFile returns the data related to the keys file
func ReadKeysFile(netParams *dagconfig.Params, path string) (*File, error) {
	if path == "" {
		path = defaultKeysFile(netParams)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	decodedFile := &keysFileJSON{}
	err = decoder.Decode(&decodedFile)
	if err != nil {
		return nil, err
	}

	keysFile := &File{
		path: path,
	}
	err = keysFile.fromJSON(decodedFile)
	if err != nil {
		return nil, err
	}

	return keysFile, nil
}

// ReadLockedKeysFile acquires the wallet's lock before opening the keys file.
// The caller holds the returned lock until it calls unlock or the process exits.
func ReadLockedKeysFile(netParams *dagconfig.Params, path string) (file *File, unlock func() error, err error) {
	if path == "" {
		path = defaultKeysFile(netParams)
	}
	unlock, err = tryLockKeysFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err = ReadKeysFile(netParams, path)
	if err != nil {
		_ = unlock()
		return nil, nil, err
	}
	return file, unlock, nil
}

func createFileDirectoryIfDoesntExist(path string) error {
	dir := filepath.Dir(path)
	exists, err := pathExists(dir)
	if err != nil {
		return err
	}

	if exists {
		return nil
	}

	return os.MkdirAll(dir, 0o700)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)

	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

// Save writes the file contents to the disk atomically: write to a temp file in the same directory,
// fsync it, rename it over the real path, then fsync the directory - rather than truncating and
// overwriting the existing file's bytes in place.
//
// The previous implementation opened with O_WRONLY|O_CREATE (no O_TRUNC) and encoded directly over
// the existing bytes with no fsync at all. A shorter new encoding left the old file's tail behind
// (harmless today only because ReadKeysFile's json.Decoder.Decode stops at the first value), and -
// more seriously - a crash or power loss mid-write, which can happen on every NewAddress or
// change-address save the daemon does, could leave a truncated or mixed file holding the encrypted
// mnemonics, unrecoverable on the next load. Writing to a new file and renaming it into place means
// a crash before the rename leaves the original file untouched, and POSIX guarantees the rename
// itself is atomic - readers never see a partial file. The on-disk JSON format itself is unchanged.
func (d *File) Save() error {
	_, err := d.save()
	return err
}

// save reports whether the replacement file was renamed into place.
func (d *File) save() (bool, error) {
	if d.path == "" {
		return false, errors.New("cannot save a file with uninitialized path")
	}

	err := createFileDirectoryIfDoesntExist(d.path)
	if err != nil {
		return false, err
	}

	dir := filepath.Dir(d.path)
	tempFile, err := os.CreateTemp(dir, filepath.Base(d.path)+".tmp-*")
	if err != nil {
		return false, err
	}
	tempPath := tempFile.Name()
	// A no-op once the rename below has succeeded (the path no longer exists); only cleans up a
	// leftover temp file when Save returns early.
	defer os.Remove(tempPath)

	encoder := json.NewEncoder(tempFile)
	err = encoder.Encode(d.toJSON())
	if err != nil {
		tempFile.Close()
		return false, err
	}

	err = tempFile.Sync()
	if err != nil {
		tempFile.Close()
		return false, err
	}
	err = tempFile.Close()
	if err != nil {
		return false, err
	}

	directory, err := openSyncDir(dir)
	if err != nil {
		return false, err
	}
	defer directory.Close()

	err = renameFile(tempPath, d.path)
	if err != nil {
		return false, err
	}

	return true, directory.Sync()
}

type syncDirectory interface {
	Sync() error
	Close() error
}

// openSyncDir allows tests to inject a failure after the rename.
var openSyncDir = openDirectoryForSync

// renameFile is os.Rename, as a variable so tests can fail Save at its last step on every platform.
var renameFile = os.Rename

const defaultNumThreads = 8

func (d *File) numThreads(password []byte) (uint8, error) {
	// There's a bug in v0 wallets where the number of threads
	// was determined by the number of logical CPUs at the machine,
	// which made the authentication non-deterministic across platforms.
	// In order to solve it we introduce v1 where the number of threads
	// is constant, and brute force the number of threads in v0.

	if d.Version != 0 {
		return defaultNumThreads, nil
	}

	numThreads, err := d.detectNumThreads(password, d.EncryptedMnemonics[0])
	if err != nil {
		return 0, err
	}

	d.NumThreads = numThreads
	return numThreads, nil
}

func (d *File) detectNumThreads(password []byte, encryptedMnemonic *EncryptedMnemonic) (uint8, error) {
	firstGuessNumThreads := d.NumThreads
	if d.NumThreads == 0 {
		numThreads, err := strconv.ParseUint(strconv.Itoa(runtime.NumCPU()), 10, 8)
		if err != nil {
			return 0, err
		}
		firstGuessNumThreads = uint8(numThreads)
	}
	_, err := decryptMnemonic(firstGuessNumThreads, encryptedMnemonic, password)
	if err != nil {
		if !strings.Contains(err.Error(), "message authentication failed") {
			return 0, err
		}
	} else {
		return firstGuessNumThreads, nil
	}

	for numThreadsGuess := uint8(1); ; numThreadsGuess++ {
		if numThreadsGuess == firstGuessNumThreads {
			continue
		}

		_, err := decryptMnemonic(numThreadsGuess, encryptedMnemonic, password)
		if err != nil {
			const maxTries = 255
			if numThreadsGuess == maxTries || !strings.Contains(err.Error(), "message authentication failed") {
				return 0, err
			}
		} else {
			return numThreadsGuess, nil
		}
	}
}

func getAEAD(threads uint8, password, salt []byte) (cipher.AEAD, error) {
	key := argon2.IDKey(password, salt, 1, 64*1024, threads, 32)
	return chacha20poly1305.NewX(key)
}

func decryptMnemonic(numThreads uint8, encryptedPrivateKey *EncryptedMnemonic, password []byte) (string, error) {
	aead, err := getAEAD(numThreads, password, encryptedPrivateKey.salt)
	if err != nil {
		return "", err
	}

	if len(encryptedPrivateKey.cipher) < aead.NonceSize() {
		return "", errors.New("ciphertext too short")
	}

	// Split nonce and ciphertext.
	nonce, ciphertext := encryptedPrivateKey.cipher[:aead.NonceSize()], encryptedPrivateKey.cipher[aead.NonceSize():]

	// Decrypt the message and check it wasn't tampered with.
	decrypted, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(decrypted), nil
}

// flockMap is a map that holds all lock file handlers. This map guarantees that
// the associated locked file handler will never get cleaned by the GC, because
// once they are cleaned the associated file will be unlocked.
var (
	flockMap = make(map[string]*flock.Flock)
	flockMu  sync.Mutex
)

// TryLock tries to acquire an exclusive lock for the file.
func (d *File) TryLock() error {
	if err := createFileDirectoryIfDoesntExist(d.path); err != nil {
		return err
	}
	_, err := tryLockKeysFile(d.path)
	return err
}

func tryLockKeysFile(path string) (func() error, error) {
	flockMu.Lock()
	defer flockMu.Unlock()
	if _, ok := flockMap[path]; ok {
		return nil, errors.Errorf("file %s is already locked", path)
	}
	lockFile := flock.New(path + ".lock")
	success, err := lockFile.TryLock()
	if err != nil {
		return nil, err
	}
	if !success {
		return nil, errors.Errorf("%s is locked and cannot be used; stop any active wallet command using it", path)
	}
	flockMap[path] = lockFile
	return func() error {
		flockMu.Lock()
		defer flockMu.Unlock()
		if flockMap[path] != lockFile {
			return nil
		}
		if err := lockFile.Unlock(); err != nil {
			return err
		}
		delete(flockMap, path)
		return nil
	}, nil
}
