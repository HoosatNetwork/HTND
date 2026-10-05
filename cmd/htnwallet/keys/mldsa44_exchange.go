package keys

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// mldsa44CosignerKeysFileJSON is the file cosigners of an ML-DSA-44 multisig exchange. It holds only
// public key hashes - nothing in it can spend - but a cosigner who imports a wrong hash for someone
// else creates addresses that cosigner cannot sign for, so it should arrive over a channel the
// cosigners trust, like the extended public keys themselves.
type mldsa44CosignerKeysFileJSON struct {
	Network   string               `json:"network"`
	Cosigners mldsa44CosignersJSON `json:"cosigners"`
}

// ExportMLDSA44Cosigners writes every ML-DSA-44 cosigner key pool this wallet holds - its own and any
// already imported - to path, for the other cosigners to import.
func (d *File) ExportMLDSA44Cosigners(params *dagconfig.Params, path string) (int, error) {
	if len(d.ExtendedPublicKeys) < 2 {
		return 0, errors.New("ML-DSA-44 key export is for multisig wallets; a single-sig wallet has nothing to share")
	}
	if len(d.MLDSA44Cosigners) == 0 {
		return 0, errors.Errorf("this wallet holds no ML-DSA-44 keys; run \"htnwallet generate-mldsa44-keys\" first")
	}
	encoded, err := json.MarshalIndent(&mldsa44CosignerKeysFileJSON{
		Network:   params.Name,
		Cosigners: mldsa44CosignersToJSON(d.MLDSA44Cosigners),
	}, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(d.MLDSA44Cosigners), os.WriteFile(path, encoded, 0o600)
}

// ImportMLDSA44Cosigners reads an ML-DSA-44 cosigner key file written by ExportMLDSA44Cosigners and
// adds the pools of this wallet's cosigners to it. A pool for a key that is not one of this wallet's
// extended public keys is refused rather than skipped: it means the file belongs to another wallet.
// It returns how many cosigner pools were added or replaced.
func (d *File) ImportMLDSA44Cosigners(params *dagconfig.Params, path string) (int, error) {
	if len(d.ExtendedPublicKeys) < 2 {
		return 0, errors.New("ML-DSA-44 key import is for multisig wallets")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var fileJSON mldsa44CosignerKeysFileJSON
	err = json.Unmarshal(encoded, &fileJSON)
	if err != nil {
		return 0, errors.Wrapf(err, "%s is not an ML-DSA-44 cosigner key file", path)
	}
	if fileJSON.Network != params.Name {
		return 0, errors.Errorf("%s holds keys for %s, not %s", path, fileJSON.Network, params.Name)
	}
	cosigners, err := mldsa44CosignersFromJSON(fileJSON.Cosigners)
	if err != nil {
		return 0, err
	}

	isCosigner := make(map[string]bool, len(d.ExtendedPublicKeys))
	for _, extendedPublicKey := range d.ExtendedPublicKeys {
		isCosigner[extendedPublicKey] = true
	}
	for extendedPublicKey := range cosigners {
		if !isCosigner[extendedPublicKey] {
			return 0, errors.Errorf("%s holds ML-DSA-44 keys for %s, which is not a cosigner of this wallet", path, extendedPublicKey)
		}
	}

	// A pool can only ever grow: the same cosigner's keys at the same indexes never change. So an
	// imported pool must agree with what is already here on every index both cover, and the longer
	// one is kept. That also keeps a stale copy of this wallet's own keys, in someone else's file,
	// from shrinking them.
	for extendedPublicKey, pool := range cosigners {
		existing, ok := d.MLDSA44Cosigners[extendedPublicKey]
		if !ok {
			continue
		}
		if !existing.agreesWith(pool) {
			return 0, errors.Errorf("%s holds ML-DSA-44 keys for %s that differ from the ones this wallet already has",
				path, extendedPublicKey)
		}
	}

	if d.MLDSA44Cosigners == nil {
		d.MLDSA44Cosigners = make(map[string]*MLDSA44KeyPool, len(cosigners))
	}
	added := 0
	for extendedPublicKey, pool := range cosigners {
		if existing, ok := d.MLDSA44Cosigners[extendedPublicKey]; ok && existing.Size() >= pool.Size() {
			continue
		}
		d.MLDSA44Cosigners[extendedPublicKey] = pool
		added++
	}
	return added, nil
}

// agreesWith reports whether p and other hold the same hash at every index of both key chains that
// both cover.
func (p *MLDSA44KeyPool) agreesWith(other *MLDSA44KeyPool) bool {
	agree := func(a, b [][]byte) bool {
		for i := range min(len(a), len(b)) {
			if !bytes.Equal(a[i], b[i]) {
				return false
			}
		}
		return true
	}
	return agree(p.ExternalPublicKeyHashes, other.ExternalPublicKeyHashes) &&
		agree(p.InternalPublicKeyHashes, other.InternalPublicKeyHashes)
}
