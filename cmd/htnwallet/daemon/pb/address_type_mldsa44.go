package pb

// AddressTypeMLDSA44 is ADDRESS_TYPE_MLDSA44 from htnwalletd.proto.
//
// It is declared by hand because the generated files predate it: proto3 enums are open, so the value
// travels over gRPC unchanged without regenerating htnwalletd.pb.go, and a daemon built before it
// sees an unknown address type rather than a decode error. After the next regeneration,
// AddressType_ADDRESS_TYPE_MLDSA44 has the same value and this can be replaced by it.
const AddressTypeMLDSA44 AddressType = 4
