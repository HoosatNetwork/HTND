package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestMultisigRestoreExplainsMLDSA44RecoveryRange(t *testing.T) {
	output := runMultisigRestore(t, 2)
	if output == "" {
		return // The child ran create.
	}
	for _, want := range []string{
		"initially cover address indexes below 500",
		"ML-DSA-44-only history",
		"generate-mldsa44-keys --count <n>",
		"longer pools from backup",
		"export-mldsa44-keys",
		"import-mldsa44-keys",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("restore advice missing %q", want)
		}
	}
}

func TestUnsupportedMultisigRestoreOmitsMLDSA44RecoveryAdvice(t *testing.T) {
	output := runMultisigRestore(t, 3)
	if output == "" {
		return // The child ran create.
	}
	if !strings.Contains(output, "Wrote the keys into") {
		t.Fatal("restore did not write keys")
	}
	t.Run("restore guidance", func(t *testing.T) {
		if strings.Contains(output, generateMLDSA44KeysSubCmd) {
			t.Error("unsupported multisig restore advertised key generation")
		}
	})
	t.Run("exchange guidance", func(t *testing.T) {
		if strings.Contains(output, exportMLDSA44KeysSubCmd) || strings.Contains(output, importMLDSA44KeysSubCmd) {
			t.Error("unsupported multisig restore advertised key exchange")
		}
	})
}

func runMultisigRestore(t *testing.T, keyCount uint32) string {
	t.Helper()
	const childPath = "HTNWALLET_RESTORE_RANGE_TEST_PATH"
	if path := os.Getenv(childPath); path != "" {
		conf := &createConfig{Import: true, NumPrivateKeys: 1, NumPublicKeys: keyCount, MinimumSignatures: keyCount,
			KeysFile: path, Password: "test password", Yes: true}
		conf.ActiveNetParams = &dagconfig.MainnetParams
		if err := create(conf); err != nil {
			t.Fatal(err)
		}
		return ""
	}

	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	peers := make([]string, keyCount-1)
	for i := range peers {
		peerMnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatal(err)
		}
		peers[i], err = libhtnwallet.MasterPublicKeyFromMnemonic(&dagconfig.MainnetParams, peerMnemonic, true)
		if err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), childPath+"="+filepath.Join(t.TempDir(), "keys.json"))
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	// Restore uses separate readers for the mnemonic and the peer key. Wait for each prompt
	// so the first reader cannot buffer the input intended for the second.
	var lines strings.Builder
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(&lines, line)
		switch {
		case strings.HasPrefix(line, "Enter mnemonic #"):
			_, err = fmt.Fprintln(input, mnemonic)
		case strings.HasPrefix(line, "Enter public key #"):
			_, err = fmt.Fprintln(input, peers[0])
			peers = peers[1:]
		}
		if err != nil {
			cancel()
			_ = child.Wait()
			t.Fatal(err)
		}
	}
	_ = input.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("restore: %v\n%s", err, lines.String())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines.String()
}
