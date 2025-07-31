// Package custom_types contains the types for our raft consensus, including the RPCs and the Log type
package customtypes

import (
	"time"
)

type CommandType string

// State machine
const (
	SetCommand      CommandType = "set"
	IncreaseCommand CommandType = "increase"
	DecreaseCommand CommandType = "decrease"
	DelCommand      CommandType = "del"
	GetCommand      CommandType = "get"
)

type Command struct {
	// The type of command
	Type CommandType
	// The key to affect
	Key string
	// The value to use if applicable
	Value *int
}

type Log struct {
	// The time this log was received by the server
	Timestamp time.Time
	// The index of this Log
	Index uint64
	// The election term the log was received in
	Term uint32
	// The command that was received
	Content Command
}
