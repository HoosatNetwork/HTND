package addressmanager

import (
	cryptorand "crypto/rand"
	"math/big"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
)

// AddressRandomize implements addressRandomizer interface
type AddressRandomize struct {
	maxFailedCount uint64
}

// NewAddressRandomize returns a new RandomizeAddress.
func NewAddressRandomize(maxFailedCount uint64) *AddressRandomize {
	return &AddressRandomize{
		maxFailedCount: maxFailedCount,
	}
}

// weightedRand is a help function which returns a random index in the
// range [0, len(weights)-1] with probability weighted by `weights`
func weightedRand(weights []float32) int {
	sum := float32(0)
	for _, weight := range weights {
		sum += weight
	}
	randPoint := cryptoRandFloat32()
	scanPoint := float32(0)
	for i, weight := range weights {
		normalizedWeight := weight / sum
		scanPoint += normalizedWeight
		if randPoint <= scanPoint {
			return i
		}
	}
	return len(weights) - 1
}

// cryptoRandFloat32 returns a cryptographically secure random float32 in [0,1)
func cryptoRandFloat32() float32 {
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(1<<24))
	if err != nil {
		panic(err)
	}
	return float32(n.Int64()) / float32(1<<24)
}

// RandomAddresses returns count addresses at random from input list
// with improved weighting that considers both failure count and recency
func (amc *AddressRandomize) RandomAddresses(addresses []*address, count int) []*appmessage.NetAddress {
	if len(addresses) < count {
		count = len(addresses)
	}

	now := time.Now()
	weights := make([]float32, 0, len(addresses))

	for _, addr := range addresses {
		// Base weight starts high
		weight := float32(1000.0)

		// Reduce weight based on failure count, but not to zero
		failurePenalty := float32(addr.connectionFailedCount) * 100.0
		weight -= failurePenalty

		// Boost weight for addresses that have succeeded recently
		if !addr.lastSuccess.IsZero() {
			hoursSinceSuccess := float32(now.Sub(addr.lastSuccess).Hours())
			if hoursSinceSuccess < 1.0 {
				weight *= 2.0 // Double weight for recent successes
			} else if hoursSinceSuccess < 24.0 {
				weight *= 1.5 // 1.5x weight for successes within 24h
			}
		}

		// Reduce weight for addresses that have been attempted very recently
		if !addr.lastAttempt.IsZero() {
			minutesSinceAttempt := float32(now.Sub(addr.lastAttempt).Minutes())
			if minutesSinceAttempt < 5.0 {
				weight *= 0.1 // Very low weight for recent attempts
			} else if minutesSinceAttempt < 30.0 {
				weight *= 0.5 // Lower weight for attempts within 30 minutes
			}
		}

		// Ensure minimum weight is never zero to give every address a chance
		if weight < 1.0 {
			weight = 1.0
		}

		weights = append(weights, weight)
	}

	result := make([]*appmessage.NetAddress, 0, count)
	for count > 0 {
		i := weightedRand(weights)
		result = append(result, addresses[i].netAddress)
		// Zero entry i to avoid re-selection
		weights[i] = 0
		// Update count
		count--
	}
	return result
}
