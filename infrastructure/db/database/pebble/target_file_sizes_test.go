package pebble

import "testing"

// TestTargetFileSizesAreCapped pins that no level's target file size exceeds the cap, so no sstable gets a
// bloom filter block too large to stay in the block cache, while the levels below the cap keep the sizes
// they had.
func TestTargetFileSizesAreCapped(t *testing.T) {
	const base = 64 << 20
	sizes := Options(64).TargetFileSizes
	want := [7]int64{base, 4 * base, defaultMaxTargetFileSize, defaultMaxTargetFileSize,
		defaultMaxTargetFileSize, defaultMaxTargetFileSize, defaultMaxTargetFileSize}
	if sizes != want {
		t.Errorf("target file sizes %v, want %v", sizes, want)
	}

	t.Setenv("HTND_MAX_FILE_SIZE_MB", "1024")
	if got := Options(64).TargetFileSizes[6]; got != 1024<<20 {
		t.Errorf("with HTND_MAX_FILE_SIZE_MB=1024 the last level targets %d bytes, want %d", got, 1024<<20)
	}

	// A cap below the base file size would make the base level's tables smaller than a flush; it is
	// raised to the base instead.
	t.Setenv("HTND_MAX_FILE_SIZE_MB", "16")
	if got := Options(64).TargetFileSizes; got[0] != base || got[6] != base {
		t.Errorf("with a cap below the base, target file sizes %v, want all %d", got, base)
	}
}
