package customtypes

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

type ClientCommandsArgs struct {
	// The incoming command from the client
	Command Command
	// Unique request ID to prevent duplicate execution
	RequestID string
}

type ClientCommandsResp struct {
	// Indicates if the command was successful or not
	Success bool
	// If the command was not successful, this will contain the error message
	Error string
	// The result of the command if it was successful
	Result any
	// The leader ID of the node that processed the command
	LeaderID string
	// The address of the leader node for the client to connect to
	LeaderAddress string
	// Indicates if the command needs to be redirected to the leader
	Redirect bool
}
