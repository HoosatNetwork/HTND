package coinbasemanager

import (
	"bytes"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
)

// TestCoinbasePayloadRoundTripAcrossForkVersions guards against the exact bug hit on a
// running node: a merge-set block's coinbase must always be parsed using that specific
// block's own version, not the version of whichever block is currently being
// built/validated. Getting this wrong misaligns the payload offsets across the entropy
// hard fork boundary and produces garbage values (e.g. a bogus scriptPubKey version).
func TestCoinbasePayloadRoundTripAcrossForkVersions(t *testing.T) {
	cm := &coinbaseManager{coinbasePayloadScriptPublicKeyMaxLength: 150}

	coinbaseData := &externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x52, 0x53}, Version: 0},
		ExtraData:       []byte("extra"),
	}

	originalVersion := constants.GetBlockVersion()
	defer constants.SetBlockVersion(originalVersion)

	for _, version := range []uint16{coinbaseEntropyActivationVersion - 1, coinbaseEntropyActivationVersion} {
		var entropy [lengthOfEntropy]byte
		if version >= coinbaseEntropyActivationVersion {
			entropy = [lengthOfEntropy]byte{1, 2, 3, 4, 5, 6, 7, 8}
		}

		// serializeCoinbasePayload (like the rest of production code) always targets
		// the ambient "currently active" block version - set it to simulate building
		// this specific block.
		constants.SetBlockVersion(version)
		payload, err := cm.serializeCoinbasePayload(12345, coinbaseData, 999, entropy)
		if err != nil {
			t.Fatalf("version %d: serialize failed: %v", version, err)
		}

		wantPayloadLen := prefixLengthForVersion(version) + lengthOfVersionScriptPubKey + lengthOfScriptPubKeyLength +
			len(coinbaseData.ScriptPublicKey.Script) + len(coinbaseData.ExtraData)
		if len(payload) != wantPayloadLen {
			t.Fatalf("version %d: payload length = %d, want %d", version, len(payload), wantPayloadLen)
		}

		// Parse it back as if it were a merge-set block's coinbase being examined while
		// some *other*, differently-versioned block is the one currently being
		// processed - i.e. exactly the scenario that broke: the ambient version here
		// deliberately does NOT match the payload's own version, and extraction must
		// still use the version passed in explicitly, not the ambient one.
		// Deliberately land on the *other side* of the fork threshold, so a version
		// that (bugfully) fell back to the ambient global would compute the wrong
		// prefix length instead of just happening to agree by coincidence.
		mismatchedVersion := uint16(coinbaseEntropyActivationVersion)
		if version >= coinbaseEntropyActivationVersion {
			mismatchedVersion = coinbaseEntropyActivationVersion - 1
		}
		constants.SetBlockVersion(mismatchedVersion)
		blueScore, gotData, subsidy, err := cm.ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(
			&externalapi.DomainTransaction{Payload: payload}, version)
		if err != nil {
			t.Fatalf("version %d: extract failed: %v", version, err)
		}
		if blueScore != 12345 {
			t.Errorf("version %d: blueScore = %d, want 12345", version, blueScore)
		}
		if subsidy != 999 {
			t.Errorf("version %d: subsidy = %d, want 999", version, subsidy)
		}
		if gotData.ScriptPublicKey.Version != coinbaseData.ScriptPublicKey.Version {
			t.Errorf("version %d: scriptPubKey version = %d, want %d",
				version, gotData.ScriptPublicKey.Version, coinbaseData.ScriptPublicKey.Version)
		}
		if !bytes.Equal(gotData.ScriptPublicKey.Script, coinbaseData.ScriptPublicKey.Script) {
			t.Errorf("version %d: script = %x, want %x", version, gotData.ScriptPublicKey.Script, coinbaseData.ScriptPublicKey.Script)
		}
		if !bytes.Equal(gotData.ExtraData, coinbaseData.ExtraData) {
			t.Errorf("version %d: extraData = %q, want %q", version, gotData.ExtraData, coinbaseData.ExtraData)
		}
	}
}
