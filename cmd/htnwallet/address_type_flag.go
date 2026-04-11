package main

import (
	"fmt"
	"strings"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

func parseAddressTypeFlag(flagValue string) (pb.AddressType, error) {
	value := strings.TrimSpace(strings.ToLower(flagValue))
	if value == "" {
		return pb.AddressType_ADDRESS_TYPE_UNSPECIFIED, nil
	}

	switch value {
	case "p2pkh":
		return pb.AddressType_ADDRESS_TYPE_P2PKH, nil
	case "p2pk":
		return pb.AddressType_ADDRESS_TYPE_P2PK, nil
	default:
		return pb.AddressType_ADDRESS_TYPE_UNSPECIFIED, fmt.Errorf("unknown --address-type %q (expected p2pkh or p2pk)", flagValue)
	}
}
