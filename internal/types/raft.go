// Package custom_types contains the types for our raft consensus, including the RPCs and the Log type
package customtypes

import "time"

type CommandType string

// State machine
const (
	SetCommand      CommandType = "set"
	IncreaseCommand CommandType = "increase"
	DecreaseCommand CommandType = "decrease"
	DelCommand      CommandType = "del"
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

type Follower struct {
	// The follower's unique identifier
	ID string
	// The network address and port of the follower on the network
	Address string
	// The index of the next log entry to send to the server
	NextIndex uint64
	// The index of the last log entry known to be replicated on the server
	MatchIndex uint64
}

type Node struct {
	// A unique identifier for each server
	ID string
	// Latest election term the server has seen
	CurrentTerm uint32
	// The ID of the server it voted for in the current term
	VotedFor string
	// An array of commands received by this server and the term they where received in
	Logs []Log
	// The index of the highest log entry known to be committed
	CommitIndex uint64
	// The index of the highest log entry known to be applied to the state machine
	LastApplied uint64

	// Leader only states
	Followers []Follower

	// State machine
	State map[string]int
}

type RequestVoteArgs struct {
	// the candidate's current term
	Term uint32
	// the ID of the candidate requesting the vote
	CandidateID string
	// The index of the candidate's last log entry
	LastLogIndex uint64
	// the term of the candidate's last log entry
	LastLogTerm uint32
}

type RequestVoteResp struct {
	// the current term on the voter
	Term uint32
	// Indicates if the vote was given or not
	VoteGranted bool
}

type AppendEntriesArgs struct {
	// The leader's current term
	Term uint32
	// The leader ID ensuring all clients know who the leader is
	LeaderID string
	// The index of the last log entry on the leader preceeding the new ones getting sent
	PrevLogIndex uint64
	// The term of the PrevLogIndex entry
	PrevLogTerm uint32
	// An array of logs to be replicated, if empty the message serves as a heart beat
	Entries []Log
	// The leader's commit index
	LeaderCommit uint64
}

type AppendEntriesResp struct {
	// Current term of the server for the leader to update itself
	Term uint32
	// True if the folower contained entries matching PrevLogIndex and PrevLogTerm
	Success bool
}
