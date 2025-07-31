// Package helpers provides general helper functions for the Raft protocol implementation.
package helpers

import (
	"math/rand"
	"time"
)

func GetNewElectionTimeout() time.Duration {
	// Generate a random timeout between 5000ms and 10000ms exclusive
	// In production, this should be between 150ms and 300ms but for testing purposes, we use a larger range so we can see the logs more clearly
	millisecondsToNanoseconds := 1_000_000
	timeoutMs := rand.Intn(5000) + 10000
	timeoutNs := timeoutMs * millisecondsToNanoseconds

	return time.Duration(timeoutNs)
}
