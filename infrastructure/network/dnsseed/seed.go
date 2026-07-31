// Copyright (c) 2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dnsseed

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	pb2 "github.com/HoosatNetwork/HTND/infrastructure/network/dnsseed/pb"
	"github.com/HoosatNetwork/HTND/util/random"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/HoosatNetwork/HTND/util/mstime"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

const (
	// These constants are used by the DNS seed code to pick a random last
	// seen time.
	secondsIn3Days int32 = 24 * 60 * 60 * 3
	secondsIn4Days int32 = 24 * 60 * 60 * 4

	// SubnetworkIDPrefixChar is the prefix of subnetworkID, when building a DNS seed request
	SubnetworkIDPrefixChar byte = 'n'
)

// OnSeed is the signature of the callback function which is invoked when DNS
// seeding is successful.
type OnSeed func(addrs []*appmessage.NetAddress)

func randomInt31n(limit int32) int32 {
	if limit <= 0 {
		panic(fmt.Sprintf("randomInt31n requires a positive limit, got %d", limit))
	}
	value, err := random.Uint64()
	if err != nil {
		panic(err)
	}
	limitUint32 := uint32(limit)
	randomOffset := value % uint64(limitUint32)
	randomOffsetInt64, err := strconv.ParseInt(strconv.FormatUint(randomOffset, 10), 10, 32)
	if err != nil {
		panic(err)
	}
	//nolint:gosec // bounded by ParseInt with bitSize 32 and positive limit
	return int32(randomOffsetInt64)
}

func mustPortUint16(port string) uint16 {
	intPort, err := strconv.Atoi(port)
	if err != nil {
		panic(err)
	}
	if intPort < 0 || intPort > 65535 {
		panic(fmt.Sprintf("port %d overflows uint16", intPort))
	}
	return uint16(intPort)
}

// LookupFunc is the signature of the DNS lookup function.
type LookupFunc func(string) ([]net.IP, error)

// SeedFromDNS uses DNS seeding to populate the address manager with peers.
func SeedFromDNS(dagParams *dagconfig.Params, customSeed string, includeAllSubnetworks bool,
	subnetworkID *externalapi.DomainSubnetworkID, lookupFn LookupFunc, seedFn OnSeed,
) {
	var dnsSeeds []string
	if customSeed != "" {
		dnsSeeds = []string{customSeed}
	} else {
		dnsSeeds = dagParams.DNSSeeds
	}

	for _, dnsseed := range dnsSeeds {
		host := dnsseed

		if !includeAllSubnetworks {
			if subnetworkID != nil {
				host = fmt.Sprintf("%c%s.%s", SubnetworkIDPrefixChar, subnetworkID, host)
			} else {
				host = fmt.Sprintf("%c.%s", SubnetworkIDPrefixChar, host)
			}
		}

		spawn("SeedFromDNS", func() {
			seedPeers, err := lookupFn(host)
			if err != nil {
				log.Infof("DNS discovery failed on seed %s: %s", host, err)
				return
			}
			numPeers := len(seedPeers)

			log.Infof("%d addresses found from DNS seed %s", numPeers, host)

			if numPeers == 0 {
				return
			}
			addresses := make([]*appmessage.NetAddress, len(seedPeers))
			port := mustPortUint16(dagParams.DefaultPort)
			for i, peer := range seedPeers {
				addresses[i] = appmessage.NewNetAddressTimestamp(
					// seed with addresses from a time randomly selected
					// between 3 and 7 days ago.
					mstime.Now().Add(-1*time.Second*time.Duration(secondsIn3Days+
						randomInt31n(secondsIn4Days))),
					peer, port)
			}

			seedFn(addresses)
		})
	}
}

// SeedFromGRPC send gRPC request to get list of peers for a given host
func SeedFromGRPC(dagParams *dagconfig.Params, customSeed string, includeAllSubnetworks bool,
	subnetworkID *externalapi.DomainSubnetworkID, seedFn OnSeed,
) {
	var grpcSeeds []string
	if customSeed != "" {
		grpcSeeds = []string{customSeed}
	} else {
		grpcSeeds = dagParams.GRPCSeeds
	}

	for _, host := range grpcSeeds {
		spawn("SeedFromGRPC", func() {
			conn, err := grpc.NewClient(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				log.Warnf("Failed to connect to gRPC server: %s", host)
				return
			}
			defer conn.Close()

			client := pb2.NewPeerServiceClient(conn)

			var subnetID []byte
			if subnetworkID != nil {
				subnetID = subnetworkID[:]
			} else {
				subnetID = nil
			}

			req := &pb2.GetPeersListRequest{
				SubnetworkID:          subnetID,
				IncludeAllSubnetworks: includeAllSubnetworks,
			}
			res, err := client.GetPeersList(context.Background(), req)
			if err != nil {
				log.Infof("gRPC request to get peers failed (host=%s): %s", host, err)
				return
			}

			seedPeers := fromProtobufAddresses(res.Addresses)

			numPeers := len(seedPeers)

			log.Infof("%d addresses found from DNS seed %s", numPeers, host)

			if numPeers == 0 {
				return
			}
			addresses := make([]*appmessage.NetAddress, len(seedPeers))
			port := mustPortUint16(dagParams.DefaultPort)
			for i, peer := range seedPeers {
				addresses[i] = appmessage.NewNetAddressTimestamp(
					// seed with addresses from a time randomly selected
					// between 3 and 7 days ago.
					mstime.Now().Add(-1*time.Second*time.Duration(secondsIn3Days+
						randomInt31n(secondsIn4Days))),
					peer, port)
			}

			seedFn(addresses)
		})
	}
}

func fromProtobufAddresses(proto []*pb2.NetAddress) []net.IP {
	var addresses []net.IP

	for _, pbAddr := range proto {
		addresses = append(addresses, net.IP(pbAddr.IP))
	}

	return addresses
}
