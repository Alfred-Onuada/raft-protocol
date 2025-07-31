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
