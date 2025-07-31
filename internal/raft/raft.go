// Package raft contains the raft protocol implementation
package raft

import (
	"fmt"
	"math"
	"net"
	"net/rpc"
	"time"

	"github.com/Alfred-Onuada/raft-protocol/internal/helpers"
	logger "github.com/Alfred-Onuada/raft-protocol/internal/logging"
	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
	"go.uber.org/zap"
)

type Member struct {
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
	// The timer holds the election timer
	ElectionTimer *time.Timer
	// Latest election term the server has seen
	CurrentTerm uint32
	// The ID of the server it voted for in the current term
	VotedFor string
	// An array of commands received by this server and the term they where received in
	Logs []customtypes.Log
	// The index of the highest log entry known to be committed
	CommitIndex uint64
	// The index of the highest log entry known to be applied to the state machine
	LastApplied uint64

	// The number of votes received in the current term
	VotesReceived int

	// The members of the Raft cluster
	Members []Member

	// The ID of the leader, if any
	LeaderID *string
	// Indicates if this node is currently the leader
	IsLeader bool
	// This channel is used to signal to any node that is thinks it's a leader to stop being a leader when a new leader is elected
	LeaderStopChan chan bool
	// The interval at which the leader sends heartbeats to followers
	HeartBeatInterval time.Duration

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
	Entries []customtypes.Log
	// The leader's commit index
	LeaderCommit uint64
}

type AppendEntriesResp struct {
	// Current term of the server for the leader to update itself
	Term uint32
	// True if the folower contained entries matching PrevLogIndex and PrevLogTerm
	Success bool
}

func (n *Node) RequestVote(args *RequestVoteArgs, resp *RequestVoteResp) error {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.Uint32("currentTerm", n.CurrentTerm),
		zap.Any("logs", n.Logs),
		zap.Any("members", n.Members),
	)
	funcLogger.Debug("Received RequestVote RPC", zap.Any("args", args))

	// If the node is in a higher term than the candidate, it should reject the vote
	if n.CurrentTerm > args.Term {
		funcLogger.Debug("Rejecting vote due to higher term", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("candidateTerm", args.Term))

		resp.Term = n.CurrentTerm
		resp.VoteGranted = false
		return nil
	}

	// if the node has voted for another candidate in the current term, it should reject the vote
	if n.VotedFor != "" && n.VotedFor != args.CandidateID {
		funcLogger.Debug("Rejecting vote due to already voting for another candidate", zap.String("votedFor", n.VotedFor), zap.String("candidateID", args.CandidateID))

		resp.Term = n.CurrentTerm
		resp.VoteGranted = false
		return nil
	}

	// If the candidate's log is up-to-date, it should grant the vote
	// up-to-date means the candidate's last log is in a higher term or is longer
	if len(n.Logs) == 0 ||
		(args.LastLogTerm > n.Logs[len(n.Logs)-1].Term ||
			args.LastLogIndex > uint64(len(n.Logs)-1)) {
		funcLogger.Debug("Granting vote to candidate", zap.String("candidateID", args.CandidateID), zap.Uint32("candidateTerm", args.Term))

		resp.Term = args.Term
		resp.VoteGranted = true

		// update the node's term and votedFor
		n.CurrentTerm = args.Term
		n.VotedFor = args.CandidateID
		return nil
	}

	// any other case, the node should reject the vote
	funcLogger.Debug(
		"Rejecting vote due to outdated log",
		zap.Uint64("lastLogIndex", args.LastLogIndex),
		zap.Uint32("lastLogTerm", args.LastLogTerm),
		zap.Uint64("currentLogIndex", uint64(len(n.Logs)-1)),
		zap.Uint32("currentLogTerm", n.Logs[len(n.Logs)-1].Term),
	)
	resp.Term = n.CurrentTerm
	resp.VoteGranted = false

	return nil
}

func (n *Node) AppendEntries(args *AppendEntriesArgs, resp *AppendEntriesResp) error {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)

	funcLogger.Debug("Received AppendEntries RPC", zap.Any("args", args), zap.Any("currentState", n))

	// If the node is in a higher term than the leader, it should reject the append entries
	if n.CurrentTerm > args.Term {
		funcLogger.Debug("Rejecting AppendEntries due to higher term", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("leaderTerm", args.Term))

		resp.Term = n.CurrentTerm
		resp.Success = false
	}

	// Reject the append entries if the entry at PrevLogIndex does not match PrevLogTerm or does not exist
	if int(args.PrevLogIndex) > len(n.Logs)-1 || n.Logs[args.PrevLogIndex].Term != args.PrevLogTerm {
		funcLogger.Debug("Rejecting AppendEntries due to mismatch at PrevLogIndex", zap.Uint64("prevLogIndex", args.PrevLogIndex), zap.Uint32("prevLogTerm", args.PrevLogTerm))

		resp.Term = n.CurrentTerm
		resp.Success = false
		return nil
	}

	// Perform a clean up of log entries if needed
	for i, entry := range args.Entries {
		// get the index for this new entry
		idx := args.PrevLogIndex + uint64(i) + 1

		// If there is an entry at that index, in a different term, we need to delete it and all that follow
		if idx < uint64(len(n.Logs)) && n.Logs[idx].Term != entry.Term {
			funcLogger.Debug("Cleaning up log entries due to term mismatch", zap.Uint64("index", idx), zap.Uint32("entryTerm", entry.Term), zap.Uint32("currentTerm", n.Logs[idx].Term))

			// Remove all entries from the log starting from this index
			n.Logs = n.Logs[:idx]
			break
		}
	}

	// At this point the RPC will be successful, at least as a heartbeat so reset the election timer
	n.ElectionTimer.Reset(helpers.GetNewElectionTimeout())
	n.LeaderID = &args.LeaderID // Indicate that this node is now following the leader
	// Stop the node's heartbeat if it was a leader
	// At this point it is guaranteed that this node is not the current leader, since it received an AppendEntries RPC and was about to use it.
	if n.IsLeader {
		funcLogger.Debug("Stopping leader heartbeat as a new leader has been elected")
		n.LeaderStopChan <- true
		n.IsLeader = false // Set the node to not be a leader anymore
	}

	// Append the new entries to the log
	for _, entry := range args.Entries {
		funcLogger.Debug("Appending entry to log", zap.Any("entry", entry))
		n.Logs = append(n.Logs, entry)
	}
	// Update the node's last committed index
	if args.LeaderCommit > n.CommitIndex {
		funcLogger.Debug("Updating commit index", zap.Uint64("leaderCommit", args.LeaderCommit), zap.Uint64("currentCommitIndex", n.CommitIndex))
		n.CommitIndex = uint64(math.Min(float64(args.LeaderCommit), float64(len(n.Logs)-1)))
	}

	// apply the logs to the state machine
	for i := n.LastApplied + 1; i <= n.CommitIndex; i++ {
		entry := n.Logs[i]
		funcLogger.Debug("Applying log entry to state machine", zap.Any("entry", entry))

		n.ApplyLogToStateMachine(entry, i)
	}

	funcLogger.Debug("AppendEntries successful", zap.Uint64("commitIndex", n.CommitIndex), zap.Uint64("lastApplied", n.LastApplied))
	// update the current term and success response
	n.CurrentTerm = args.Term

	resp.Term = n.CurrentTerm
	resp.Success = true

	return nil
}

func (n *Node) ApplyLogToStateMachine(entry customtypes.Log, index uint64) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	funcLogger.Debug("Applying log entry to state machine", zap.Any("entry", entry), zap.Uint64("index", index))

	// Apply the command to the state machine
	switch entry.Content.Type {
	case customtypes.SetCommand:
		if entry.Content.Value != nil {
			n.State[entry.Content.Key] = *entry.Content.Value
		} else {
			funcLogger.Warn("Set command received with no value", zap.String("key", entry.Content.Key))
		}
	case customtypes.IncreaseCommand:
		n.State[entry.Content.Key] += 1
	case customtypes.DecreaseCommand:
		n.State[entry.Content.Key] -= 1
	case customtypes.DelCommand:
		delete(n.State, entry.Content.Key)
	default:
		funcLogger.Warn("Unknown command type received", zap.String("commandType", string(entry.Content.Type)))
	}

	// Update the last applied index
	n.LastApplied = index

	funcLogger.Debug("State machine updated", zap.Any("state", n.State))
}

func (n *Node) RequestMemberVote(member Member) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.String("memberID", member.ID),
		zap.String("memberAddress", member.Address),
	)

	// Dial the member to request a vote
	logger.Log.Debug("Requesting vote from member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
	client, err := rpc.Dial("tcp", member.Address)
	if err != nil {
		funcLogger.Error("Failed to dial member for vote request", zap.Error(err))
		return
	}

	// This prevents an array out of bounds error if there are no logs
	lastLogIndex := uint64(0)
	lastLogTerm := uint32(0)
	if len(n.Logs) > 0 {
		lastLogIndex = uint64(len(n.Logs) - 1)
		lastLogTerm = n.Logs[lastLogIndex].Term
	}

	args := &RequestVoteArgs{
		Term:         n.CurrentTerm,
		CandidateID:  n.ID,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	var resp RequestVoteResp
	err = client.Call("Node.RequestVote", args, &resp)
	if err != nil {
		funcLogger.Error("Failed to call RequestVote on member", zap.Error(err))
		return
	}
	funcLogger.Debug("Received vote response from member", zap.Any("response", resp))

	// If the response is successful, increment the votes received
	if resp.VoteGranted {
		funcLogger.Debug("Vote granted by member", zap.String("memberID", member.ID))
		n.VotesReceived++

		// If the node has received a majority of votes, it becomes the leader
		if n.VotesReceived > len(n.Members)/2 {
			funcLogger.Info("Node has become the leader", zap.String("nodeID", n.ID), zap.Uint32("currentTerm", n.CurrentTerm))
			n.LeaderID = &n.ID
			// Reset the election timer as the node is now the leader
			n.ElectionTimer.Reset(helpers.GetNewElectionTimeout())
			// Begin broadcasting heartbeats to other members
			funcLogger.Debug("Broadcasting heartbeat to other members")
			n.BroadcastHeartbeat()
		}
	} else {
		funcLogger.Debug("Vote not granted by member", zap.String("memberID", member.ID), zap.Uint32("term", resp.Term))
	}
}

func (n *Node) SendHeartbeatToMember(member Member, args *AppendEntriesArgs) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.String("memberID", member.ID),
		zap.String("memberAddress", member.Address),
	)

	client, err := rpc.Dial("tcp", member.Address)
	if err != nil {
		funcLogger.Error("Failed to dial member for heartbeat", zap.Error(err))
		return
	}

	var resp AppendEntriesResp
	err = client.Call("Node.AppendEntries", args, &resp)
	if err != nil {
		funcLogger.Error("Failed to call AppendEntries on member", zap.Error(err))
		return
	}

	funcLogger.Debug("Received AppendEntries response from member", zap.Any("response", resp))
}

func (n *Node) BroadcastHeartbeat() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)

	funcLogger.Debug("Broadcasting heartbeat to other members", zap.String("leaderID", n.ID))

	// Create a new AppendEntriesArgs with an empty entries array
	args := &AppendEntriesArgs{
		Term:         n.CurrentTerm,
		LeaderID:     n.ID,
		PrevLogIndex: 0,                   // No previous log index for heartbeat
		PrevLogTerm:  0,                   // No previous log term for heartbeat
		Entries:      []customtypes.Log{}, // Empty entries for heartbeat
		LeaderCommit: n.CommitIndex,       // Current commit index
	}

	// Indefinitely send heartbeats to all members until a new leader is elected
	for {
		select {
		case <-n.LeaderStopChan:
			funcLogger.Debug("Stopping heartbeat broadcast as a new leader has been elected")
			n.LeaderStopChan = make(chan bool) // Reset the channel so it's back to a clean state
			return
		default:
			for _, member := range n.Members {
				if member.ID == n.ID {
					continue // Skip itself
				}

				funcLogger.Debug("Sending heartbeat to member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
				go n.SendHeartbeatToMember(member, args)
			}

			// This represents the heartbeat interval
			time.Sleep(n.HeartBeatInterval) // Sleep for a while before sending the next heartbeat
		}
	}
}

func (n *Node) CheckElectionTimeoutExpiry() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	<-n.ElectionTimer.C
	// When the election timer expires, become a candidate
	funcLogger.Debug("Election timer expired, transistioning to candidate state")
	n.TransistionToCandidate()
}

func (n *Node) TransistionToCandidate() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	funcLogger.Debug("Node is becoming a candidate", zap.String("nodeID", n.ID))

	// Increment the current term
	n.CurrentTerm++
	n.VotedFor = n.ID   // Vote for itself
	n.VotesReceived = 1 // Start with one vote (itself)

	// Reset the election timer
	n.ElectionTimer.Reset(helpers.GetNewElectionTimeout())
	go n.CheckElectionTimeoutExpiry()

	// Log the new term and state
	funcLogger.Info("Node has become a candidate", zap.Uint32("currentTerm", n.CurrentTerm), zap.String("votedFor", n.VotedFor))

	// Request votes from other nodes
	for _, member := range n.Members {
		if member.ID == n.ID {
			continue // Skip itself
		}

		funcLogger.Debug("Requesting vote from member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
		go n.RequestMemberVote(member)
	}
}

func NewNode(config *customtypes.Config) *Node {
	// Generate the members
	members := make([]Member, len(config.Group.Members))
	for i, memberAddress := range config.Group.Members {
		members[i] = Member{
			ID:      memberAddress, // This will be the correct ID for the member, since the ID is the address
			Address: memberAddress,
			// Initialize the next index and match index to 0, since this is a new node
			NextIndex:  0,
			MatchIndex: 0,
		}
	}

	return &Node{
		// Given that one port can only be used by one node, we can use the host and port as the ID and it is guaranteed to be unique
		ID:      fmt.Sprintf("%s:%s", config.Network.Host, config.Network.IP),
		Members: members,
		// This must be less than the MTBF
		ElectionTimer: time.NewTimer(helpers.GetNewElectionTimeout()),
		// This must be less than the election timeout to ensure that the leader sends heartbeats before followers can time out
		HeartBeatInterval: 20 * time.Millisecond, // This is fixed for all nodes,
		// Intialize all other fields to their default values
		CurrentTerm:    0,
		VotedFor:       "",
		Logs:           []customtypes.Log{},
		CommitIndex:    0,
		LastApplied:    0,
		VotesReceived:  0,
		LeaderID:       nil, // No leader at the start
		IsLeader:       false,
		LeaderStopChan: make(chan bool),      // This channel is used to stop the leader
		State:          make(map[string]int), // Initialize the state machine
	}
}

func Init(config *customtypes.Config) {
	// Initialize a new node
	logger.Log.Debug("Initializing a new Raft node")
	node := NewNode(config)
	logger.Log.Debug("New Raft node initialized", zap.String("nodeID", node.ID))

	// build the node address
	address := config.Network.Host + ":" + config.Network.IP

	// append the nodeID and address to logs
	funcLogger := logger.Log.With(
		zap.String("nodeID", node.ID),
		zap.String("address", address),
	)

	// Register the node as an RPC service
	funcLogger.Debug("Registering the Raft node for RPC")
	rpc.RegisterName("Node", node)

	// listen for incoming RPC requests
	ln, err := net.Listen("tcp", address)
	if err != nil {
		funcLogger.Error("Failed to listen for RPC requests", zap.Error(err))
		return
	}
	funcLogger.Debug("Listening for RPC requests", zap.String("address", address))

	go node.CheckElectionTimeoutExpiry()

	// Accept connections and serve requests
	for {
		conn, err := ln.Accept()
		if err != nil {
			funcLogger.Error("Failed to accept connection", zap.Error(err))
			continue
		}

		funcLogger.Debug("Accepted new connection", zap.String("remoteAddr", conn.RemoteAddr().String()))

		funcLogger.Debug("Serving RPC connection")
		go rpc.ServeConn(conn)
	}
}
