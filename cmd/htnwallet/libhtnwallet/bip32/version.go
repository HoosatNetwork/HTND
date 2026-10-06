package bip32

import "github.com/pkg/errors"

// BitcoinMainnetPrivate is the version that is used for
// bitcoin mainnet bip32 private extended keys.
// Ecnodes to xprv in base58.
var BitcoinMainnetPrivate = [4]byte{
	0x04,
	0x88,
	0xad,
	0xe4,
}

// BitcoinMainnetPublic is the version that is used for
// bitcoin mainnet bip32 public extended keys.
// Ecnodes to xpub in base58.
var BitcoinMainnetPublic = [4]byte{
	0x04,
	0x88,
	0xb2,
	0x1e,
}

// HoosatMainnetPrivate is the version that is used for
// hoosat mainnet bip32 private extended keys.
// Ecnodes to xprv in base58.
var HoosatMainnetPrivate = [4]byte{
	0x03,
	0x8f,
	0x2e,
	0xf4,
}

// HoosatMainnetPublic is the version that is used for
// hoosat mainnet bip32 public extended keys.
// Ecnodes to kpub in base58.
var HoosatMainnetPublic = [4]byte{
	0x03,
	0x8f,
	0x33,
	0x2e,
}

// HoosatTestnetPrivate is the version that is used for
// hoosat testnet bip32 public extended keys.
// Ecnodes to ktrv in base58.
var HoosatTestnetPrivate = [4]byte{
	0x03,
	0x90,
	0x9e,
	0x07,
}

// HoosatTestnetPublic is the version that is used for
// hoosat testnet bip32 public extended keys.
// Ecnodes to ktub in base58.
var HoosatTestnetPublic = [4]byte{
	0x03,
	0x90,
	0xa2,
	0x41,
}

// HoosatDevnetPrivate is the version that is used for
// hoosat devnet bip32 public extended keys.
// Ecnodes to kdrv in base58.
var HoosatDevnetPrivate = [4]byte{
	0x03,
	0x8b,
	0x3d,
	0x80,
}

// HoosatDevnetPublic is the version that is used for
// hoosat devnet bip32 public extended keys.
// Ecnodes to xdub in base58.
var HoosatDevnetPublic = [4]byte{
	0x03,
	0x8b,
	0x41,
	0xba,
}

// HoosatSimnetPrivate is the version that is used for
// hoosat simnet bip32 public extended keys.
// Ecnodes to ksrv in base58.
var HoosatSimnetPrivate = [4]byte{
	0x03,
	0x90,
	0x42,
	0x42,
}

// HoosatSimnetPublic is the version that is used for
// hoosat simnet bip32 public extended keys.
// Ecnodes to xsub in base58.
var HoosatSimnetPublic = [4]byte{
	0x03,
	0x90,
	0x46,
	0x7d,
}

func toPublicVersion(version [4]byte) ([4]byte, error) {
	switch version {
	case BitcoinMainnetPrivate:
		return BitcoinMainnetPublic, nil
	case HoosatMainnetPrivate:
		return HoosatMainnetPublic, nil
	case HoosatTestnetPrivate:
		return HoosatTestnetPublic, nil
	case HoosatDevnetPrivate:
		return HoosatDevnetPublic, nil
	case HoosatSimnetPrivate:
		return HoosatSimnetPublic, nil
	}

	return [4]byte{}, errors.Errorf("unknown version %x", version)
}

func isPrivateVersion(version [4]byte) bool {
	switch version {
	case BitcoinMainnetPrivate:
		return true
	case HoosatMainnetPrivate:
		return true
	case HoosatTestnetPrivate:
		return true
	case HoosatDevnetPrivate:
		return true
	case HoosatSimnetPrivate:
		return true
	}

	return false
}
