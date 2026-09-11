package grpcserver

import (
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// compressionFramingErrorMessage is what grpc-go reports when a message arrives flagged as
// compressed on a stream whose declared grpc-encoding is identity or absent (checkRecvPayload in
// grpc-go's rpc_util.go). The message is well formed; it is the flag and the stream header that
// contradict each other, and grpc-go ends the stream on the spot - there is no option to relax it.
const compressionFramingErrorMessage = "grpc: compressed flag set with identity or empty encoding"

// compressionFallbackDuration is how long an outbound peer that could not hold a gzip stream is
// dialed without compression before gzip is tried on it again. Long enough that a broken peer is
// not re-tried on every reconnect, short enough that a peer which upgrades, or loses the proxy in
// front of it, gets compression back without anyone restarting this node.
const compressionFallbackDuration = 24 * time.Hour

// isCompressionFramingError reports whether err is grpc-go's compression framing failure.
func isCompressionFramingError(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Internal && strings.Contains(st.Message(), compressionFramingErrorMessage)
}

// compressionFallback lets outbound P2P connections support both compressed and uncompressed
// peers.
//
// Outbound streams request gzip. That works with any peer whose gRPC stack declares and honours the
// encoding - grpc-go replies in the encoding it was dialed with, and declares it - but some peers on
// the network do not, and a stream to one of them dies on the first message whose compressed flag
// contradicts the stream's declared encoding. Without this the node reconnects, requests gzip
// again, and loses the peer again, indefinitely.
//
// So a peer that fails that way is remembered, and the next connection to it is opened without
// requesting compression. Every other peer keeps gzip. Inbound connections need none of this: the
// dialer chooses the encoding and the server side already follows it.
type compressionFallback struct {
	lock  sync.Mutex
	until map[string]time.Time
	now   func() time.Time
}

func newCompressionFallback() *compressionFallback {
	return &compressionFallback{
		until: make(map[string]time.Time),
		now:   time.Now,
	}
}

// shouldCompress reports whether an outbound stream to address should request gzip.
func (f *compressionFallback) shouldCompress(address string) bool {
	f.lock.Lock()
	defer f.lock.Unlock()

	until, ok := f.until[address]
	if !ok {
		return true
	}
	if !f.now().Before(until) {
		delete(f.until, address)
		return true
	}
	return false
}

// disableFor makes connections to address skip compression for compressionFallbackDuration.
func (f *compressionFallback) disableFor(address string) {
	f.lock.Lock()
	defer f.lock.Unlock()

	now := f.now()
	// Entries are otherwise only removed when their peer is dialed again, and a peer the node never
	// redials would stay forever. Pruning here keeps the map bounded by the peers that failed within
	// the last compressionFallbackDuration.
	for peerAddress, until := range f.until {
		if !now.Before(until) {
			delete(f.until, peerAddress)
		}
	}
	f.until[address] = now.Add(compressionFallbackDuration)
}
