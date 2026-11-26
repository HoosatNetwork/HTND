package keys

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestMalformedMLDSA44PoolErrorRedactsCosigner(t *testing.T) {
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, pool := range []string{`null`, `{"externalPublicKeyHashes":["00"]}`} {
		path := filepath.Join(t.TempDir(), "keys.json")
		data := fmt.Sprintf(`{"publicKeys":[%q],"mldsa44Cosigners":{%q:%s}}`, xpub, xpub, pool)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadKeysFile(params, path)
		if err == nil || !strings.Contains(err.Error(), "ML-DSA-44") || strings.Contains(fmt.Sprintf("%+v", err), xpub) {
			t.Fatalf("expected a malformed-pool error without the full cosigner key, got %v", err)
		}
	}
}
