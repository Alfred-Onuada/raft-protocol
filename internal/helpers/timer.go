// Package helpers provides general helper functions for the Raft protocol implementation.
package helpers

import (
	"math/rand"
	"time"
)

func GetNewElectionTimeout() time.Duration {
	// Generate a random timeout between 150ms and 300ms exclusive
	millisecondsToNanoseconds := 1_000_000
	timeoutMs := rand.Intn(150) + 150
	timeoutNs := timeoutMs * millisecondsToNanoseconds

	return time.Duration(timeoutNs)
}
