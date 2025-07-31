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

// This function handles the RequestVote RPC call
func (n *Node) RequestVote(args *customtypes.RequestVoteArgs, resp *customtypes.RequestVoteResp) error {
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

// This function handles the AppendEntries RPC call
func (n *Node) AppendEntries(args *customtypes.AppendEntriesArgs, resp *customtypes.AppendEntriesResp) error {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)

	funcLogger.Debug("Received AppendEntries RPC", zap.Any("args", args))

	// If the node is in a higher term than the leader, it should reject the append entries
	if n.CurrentTerm > args.Term {
		funcLogger.Debug("Rejecting AppendEntries due to higher term", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("leaderTerm", args.Term))

		resp.Term = n.CurrentTerm
		resp.Success = false
		return nil
	}

	// If the node is in a lower term than the leader, it should update its term and become a follower
	if n.CurrentTerm < args.Term {
		funcLogger.Debug("Node is in a lower term, transistioning to follower state", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("leaderTerm", args.Term))

		n.transistionToFollower(args.Term, &args.LeaderID)
	}

	// Reject the append entries if the entry at PrevLogIndex does not match PrevLogTerm or does not exist
	if len(n.Logs) > 0 && (args.PrevLogIndex > uint64(len(n.Logs)-1) || n.Logs[args.PrevLogIndex].Term != args.PrevLogTerm) {
		funcLogger.Debug("Rejecting AppendEntries due to mismatch at PrevLogIndex", zap.Uint64("prevLogIndex", args.PrevLogIndex), zap.Uint32("prevLogTerm", args.PrevLogTerm), zap.Any("logs", n.Logs))

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

		n.applyLogToStateMachine(entry, i)
	}

	funcLogger.Debug("AppendEntries successful", zap.Uint64("commitIndex", n.CommitIndex), zap.Uint64("lastApplied", n.LastApplied))
	// update the current term and success response
	n.CurrentTerm = args.Term

	resp.Term = n.CurrentTerm
	resp.Success = true

	return nil
}

// This function handles ClientCommand RPC call, it is called by the client to send commands to the Raft cluster
func (n *Node) ClientCommand(args *customtypes.ClientCommandsArgs, resp *customtypes.ClientCommandsResp) error {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.Any("command", args.Command),
	)

	funcLogger.Debug("Received ClientCommand RPC", zap.Any("args", args), zap.Bool("isLeader", n.IsLeader))

	// If this node is not the leader, return the leader's ID and address
	if !n.IsLeader {
		funcLogger.Debug("Command was received on a follower, returning leader information")

		if n.LeaderID != nil {
			// Find the leader's address from Members
			for _, member := range n.Members {
				if member.ID == *n.LeaderID {
					funcLogger.Debug("Found leader address", zap.String("leaderID", *n.LeaderID), zap.String("leaderAddress", member.Address))

					resp.LeaderID = *n.LeaderID
					resp.LeaderAddress = member.Address
					resp.Redirect = true // tells it to proceed on leader

					return nil
				}
			}
		}

		funcLogger.Debug("No leader found, cannot redirect command")
		// Fail if there is no leader in the cluster
		resp.Success = false
		resp.Error = "No leader known at this moment"
		return nil
	}

	// Append the command to the log
	logEntry := customtypes.Log{
		Term:      n.CurrentTerm,
		Content:   args.Command,
		Timestamp: time.Now(),
		Index:     uint64(len(n.Logs)), // The index is the current length
	}

	funcLogger.Debug("Appending client command to log", zap.Any("logEntry", logEntry))
	n.Logs = append(n.Logs, logEntry)

	// Replicate the log entry to followers (the heartbeat already contains logic to send the appropriate logs)
	n.replicateLogEntriesToMembers([]customtypes.Log{logEntry})

	// Wait for the command to be committed (replicated to a majority of nodes)
	time.Sleep(5000 * time.Millisecond) // In production this would be replaced with something more robust, for now it's a simple timeout
	if n.CommitIndex < uint64(len(n.Logs)-1) {
		funcLogger.Warn("Timeout exceeded while waiting for command to be committed", zap.Uint64("commitIndex", n.CommitIndex), zap.Uint64("logLength", uint64(len(n.Logs)-1)))
		resp.Success = false
		resp.Error = "Timeout exceeded while waiting for command to be committed"
		return nil
	}

	// Apply the command to the state machine if not already applied
	if n.LastApplied < uint64(len(n.Logs)-1) {
		n.applyLogToStateMachine(logEntry, uint64(len(n.Logs)-1))
	}

	// If command is not a mutation command, applying to state machine does nothing, so here the command is processed and the response is set
	n.executeNonMutationCommands(args.Command, resp)

	funcLogger.Debug("Client command processed successfully", zap.Any("response", resp))
	return nil
}

func (n *Node) replicateLogEntriesToMembers(logEntries []customtypes.Log) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)

	funcLogger.Debug("Replicating log entry to members", zap.Any("logEntries", logEntries))

	for memberIdx, member := range n.Members {
		if member.ID == n.ID {
			continue // Skip itself
		}

		funcLogger.Debug("Replicating log entry to member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
		prevLogIndex := uint64(0)
		prevLogTerm := uint32(0)
		if len(n.Logs) > 0 {
			prevLogIndex = uint64(len(n.Logs) - 1)
			prevLogTerm = n.Logs[prevLogIndex].Term
		}

		go n.sendHeartbeatToMember(member, memberIdx, &customtypes.AppendEntriesArgs{
			Term:         n.CurrentTerm,
			LeaderID:     n.ID,
			PrevLogIndex: prevLogIndex,  // The previous log index is the last log index before this one
			PrevLogTerm:  prevLogTerm,   // The term of the previous log entry
			Entries:      logEntries,    // Send the new log entries
			LeaderCommit: n.CommitIndex, // The current commit index
		})
	}
}

func (n *Node) executeNonMutationCommands(command customtypes.Command, resp *customtypes.ClientCommandsResp) {
	resp.Success = true

	switch command.Type {
	case customtypes.GetCommand:
		if value, exists := n.State[command.Key]; exists {
			resp.Result = value
		} else {
			resp.Success = false
			resp.Error = "Key not found"
		}
	default:
		resp.Result = nil // No result for Set, Increase, Decrease, Delete
	}
}

// applyLogToStateMachine applies the log entry to the state machine
func (n *Node) applyLogToStateMachine(entry customtypes.Log, index uint64) {
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

// requestMemberVote requests a vote from a member of the Raft cluster, this triggers the RPC call to the member's RequestVote method
func (n *Node) requestMemberVote(member Member) {
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
	defer client.Close() // Ensure the client is closed after the request

	// This prevents an array out of bounds error if there are no logs
	lastLogIndex := uint64(0)
	lastLogTerm := uint32(0)
	if len(n.Logs) > 0 {
		lastLogIndex = uint64(len(n.Logs) - 1)
		lastLogTerm = n.Logs[lastLogIndex].Term
	}

	args := &customtypes.RequestVoteArgs{
		Term:         n.CurrentTerm,
		CandidateID:  n.ID,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	var resp customtypes.RequestVoteResp
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
			n.IsLeader = true
			// Reset the election timer as the node is now the leader
			n.ElectionTimer.Reset(helpers.GetNewElectionTimeout())
			// Begin broadcasting heartbeats to other members
			funcLogger.Debug("Broadcasting heartbeat to other members")
			n.broadcastHeartbeat()
		}
	} else {
		funcLogger.Debug("Vote not granted by member", zap.String("memberID", member.ID), zap.Uint32("term", resp.Term))
	}
}

// sendHeartbeatToMember sends a heartbeat to a member of the Raft cluster, this triggers the RPC call to the member's AppendEntries method
func (n *Node) sendHeartbeatToMember(member Member, memberIdx int, args *customtypes.AppendEntriesArgs) {
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
	defer client.Close() // Ensure the client is closed after the request

	var resp customtypes.AppendEntriesResp
	err = client.Call("Node.AppendEntries", args, &resp)
	if err != nil {
		funcLogger.Error("Failed to call AppendEntries on member", zap.Error(err))
		return
	}

	funcLogger.Debug("Received AppendEntries response from member", zap.Any("response", resp))

	// If the member is in a higher term, update the current term, step down and reset the election timer
	if resp.Term > n.CurrentTerm {
		funcLogger.Debug("Member is in a higher term, stepping down", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("memberTerm", resp.Term))

		n.transistionToFollower(resp.Term, nil) // Step down to follower state
		return
	}

	// Update the members details with
	if resp.Success {
		// To avoid an out of bounds error, we check if the entries array is not empty
		if len(args.Entries) > 0 {
			n.Members[memberIdx].NextIndex = args.Entries[len(args.Entries)-1].Index + 1 // Update the next index to the last entry's index + 1
			n.Members[memberIdx].MatchIndex = args.Entries[len(args.Entries)-1].Index    // Update the match index to the last entry's index
		}

		n.updateCommitIndex()

		funcLogger.Debug("Heartbeat sent successfully, updated member details",
			zap.String("memberID", member.ID),
			zap.Uint64("nextIndex", n.Members[memberIdx].NextIndex),
			zap.Uint64("matchIndex", n.Members[memberIdx].MatchIndex),
		)
	}
}

// updateCommitIndex updates the commit index based on the logs and the members' match indices
func (n *Node) updateCommitIndex() {
	funcLogger := logger.Log.With(zap.String("nodeID", n.ID))

	funcLogger.Debug("Updating commit index based on members' match indices", zap.Uint64("currentCommitIndex", n.CommitIndex), zap.Int("logsLength", len(n.Logs)))

	// Count how many followers have replicated each log index
	for i := n.CommitIndex + 1; i < uint64(len(n.Logs)); i++ {
		count := 1 // Count the leader itself
		for _, member := range n.Members {
			if member.ID == n.ID {
				continue
			}
			if member.MatchIndex >= i {
				count++
			}
		}

		funcLogger.Debug("Checking for majority replication", zap.Uint64("index", i), zap.Int("count", count))

		// If a majority has replicated this index and it’s from the current term
		if count > len(n.Members)/2 && n.Logs[i].Term == n.CurrentTerm {
			n.CommitIndex = i
			funcLogger.Debug("Updated CommitIndex", zap.Uint64("commitIndex", n.CommitIndex))
		} else {
			break
		}
	}
}

// This function broadcasts heartbeats to all members of the Raft cluster
func (n *Node) broadcastHeartbeat() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)

	funcLogger.Debug("Broadcasting heartbeat to other members", zap.String("leaderID", n.ID))

	// Indefinitely send heartbeats to all members until a new leader is elected
	for {
		select {
		case <-n.LeaderStopChan:
			funcLogger.Debug("Stopping heartbeat broadcast as a new leader has been elected")
			n.LeaderStopChan = make(chan bool) // Reset the channel so it's back to a clean state
			return
		default:
			for memberIdx, member := range n.Members {
				if member.ID == n.ID {
					continue // Skip itself
				}

				// This prevents an array out of bounds error if there are no logs
				prevLogIndex := uint64(0)
				prevLogTerm := uint32(0)
				if len(n.Logs) > 0 {
					prevLogIndex = uint64(len(n.Logs) - 1)
					prevLogTerm = n.Logs[prevLogIndex].Term
				}

				// Create a new customtypes.AppendEntriesArgs with an empty entries array
				args := &customtypes.AppendEntriesArgs{
					Term:         n.CurrentTerm,
					LeaderID:     n.ID,
					PrevLogIndex: prevLogIndex,
					PrevLogTerm:  prevLogTerm,
					Entries:      n.Logs[member.NextIndex:], // send all the missing logs to the member (not super performant but works)
					LeaderCommit: n.CommitIndex,             // Current commit index
				}

				funcLogger.Debug("Sending heartbeat to member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
				go n.sendHeartbeatToMember(member, memberIdx, args)
			}

			// This represents the heartbeat interval
			time.Sleep(n.HeartBeatInterval) // Sleep for a while before sending the next heartbeat
		}
	}
}

// checkElectionTimeoutExpiry checks if the election timer has expired, if it has, it transistions the node to a candidate state
func (n *Node) checkElectionTimeoutExpiry() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	<-n.ElectionTimer.C
	// When the election timer expires, become a candidate
	funcLogger.Debug("Election timer expired, transistioning to candidate state")
	n.transistionToCandidate()
}

// transistionToCandidate transistions the node to a candidate state, increments the term, and requests votes from other members
func (n *Node) transistionToCandidate() {
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
	go n.checkElectionTimeoutExpiry()

	// Log the new term and state
	funcLogger.Info("Node has become a candidate", zap.Uint32("currentTerm", n.CurrentTerm), zap.String("votedFor", n.VotedFor))

	// Request votes from other nodes
	for _, member := range n.Members {
		if member.ID == n.ID {
			continue // Skip itself
		}

		funcLogger.Debug("Requesting vote from member", zap.String("memberID", member.ID), zap.String("memberAddress", member.Address))
		go n.requestMemberVote(member)
	}
}

// transistionToFollower transistions the node to a follower state, updates the term, resets the votedFor field, and sets the leader ID
func (n *Node) transistionToFollower(term uint32, leaderId *string) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	funcLogger.Debug("Node is transistioning to follower state", zap.String("nodeID", n.ID))

	n.CurrentTerm = term
	n.VotedFor = ""       // Reset the voted for field
	n.LeaderID = leaderId // Set the leader ID to the given leader ID
	if n.IsLeader {
		funcLogger.Debug("Stopping leader heartbeat as a new leader has been elected")
		n.IsLeader = false       // Set the node to not be a leader anymore
		n.LeaderStopChan <- true // Stop the leader's heartbeat if it was a leader
	}
	n.ElectionTimer.Reset(helpers.GetNewElectionTimeout()) // Reset the election timer
	funcLogger.Info("Node has transistioned to follower state", zap.Uint32("currentTerm", n.CurrentTerm), zap.String("votedFor", n.VotedFor), zap.Any("leaderID", n.LeaderID))
}

// newNode creates a new Raft node with the given configuration
func newNode(config *customtypes.Config) *Node {
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
		HeartBeatInterval: 3000 * time.Millisecond, // This is fixed for all nodes,
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

// Init initializes the Raft node, registers it for RPC, and starts listening for incoming requests
func Init(config *customtypes.Config) {
	// Initialize a new node
	logger.Log.Debug("Initializing a new Raft node")
	node := newNode(config)
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

	go node.checkElectionTimeoutExpiry()

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
