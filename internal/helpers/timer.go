// Package helpers provides general helper functions for the Raft protocol implementation.
package helpers

import (
	"math/rand"
	"time"
)

func GetNewElectionTimeout() time.Duration {
	// Generate a random timeout between 150ms and 300ms as recommended by the Raft paper
	// This ensures quick leader election while avoiding split votes
	millisecondsToNanoseconds := 1_000_000
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	timeoutMs := r.Intn(150) + 150
	timeoutNs := timeoutMs * millisecondsToNanoseconds

	return time.Duration(timeoutNs)
}
