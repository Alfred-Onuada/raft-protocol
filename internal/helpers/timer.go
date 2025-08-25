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
	timeoutMs := rand.Intn(400) + 100 // 100-500ms range
	timeoutNs := timeoutMs * millisecondsToNanoseconds

	return time.Duration(timeoutNs)
}
