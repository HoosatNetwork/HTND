package grpcserver

// Import gzip codec to register the compressor/decompressor with grpc-go.
// Some peers may negotiate grpc-encoding: gzip; without this, Recv will fail with:
// "grpc: Decompressor is not installed for grpc-encoding \"gzip\"".
import _ "google.golang.org/grpc/encoding/gzip"
