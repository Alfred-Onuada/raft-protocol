// Package raft contains the raft protocol implementation
package raft

import (
	"fmt"
	"math"
	"net"
	"net/rpc"
	"sync"
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
	// Protects all mutable state on the node
	mu sync.Mutex
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
	// Track processed request IDs to prevent duplicate execution
	ProcessedRequests map[string]*customtypes.ClientCommandsResp
}

// RequestVote function handles the RequestVote RPC call
func (n *Node) RequestVote(args *customtypes.RequestVoteArgs, resp *customtypes.RequestVoteResp) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.Uint32("currentTerm", n.CurrentTerm),
		zap.Any("logs", n.Logs),
		zap.Any("members", n.Members),
	)
	funcLogger.Debug("Received RequestVote RPC", zap.Any("args", args))

	// If candidate's term is higher, update our term and step down to follower
	if args.Term > n.CurrentTerm {
		n.CurrentTerm = args.Term
		n.VotedFor = ""
	}

	// If our term is higher than the candidate, reject the vote
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

	// Determine our last log index/term using 0 as the empty sentinel
	var myLastIndex uint64 = 0
	var myLastTerm uint32 = 0
	if len(n.Logs) > 0 {
		myLastIndex = n.Logs[len(n.Logs)-1].Index
		myLastTerm = n.Logs[len(n.Logs)-1].Term
	}

	// Raft up-to-date rule:
	// Grant if candidate's last term > ours, or terms equal and candidate's index >= ours
	upToDate := args.LastLogTerm > myLastTerm || (args.LastLogTerm == myLastTerm && args.LastLogIndex >= myLastIndex)
	if upToDate {
		funcLogger.Info("Granting vote to candidate", zap.String("candidateID", args.CandidateID), zap.Uint32("candidateTerm", args.Term))

		resp.Term = n.CurrentTerm
		resp.VoteGranted = true

		// update votedFor and reset election timer
		n.VotedFor = args.CandidateID
		n.resetElectionTimer()
		return nil
	}

	// any other case, the node should reject the vote
	funcLogger.Debug(
		"Rejecting vote due to outdated log",
		zap.Uint64("candidateLastLogIndex", args.LastLogIndex),
		zap.Uint32("candidateLastLogTerm", args.LastLogTerm),
		zap.Uint64("currentLastLogIndex", myLastIndex),
		zap.Uint32("currentLastLogTerm", myLastTerm),
	)
	resp.Term = n.CurrentTerm
	resp.VoteGranted = false

	return nil
}

// AppendEntries handles the AppendEntries RPC call
func (n *Node) AppendEntries(args *customtypes.AppendEntriesArgs, resp *customtypes.AppendEntriesResp) error {
	n.mu.Lock()
	defer n.mu.Unlock()
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
		funcLogger.Info("Node is in a lower term, transitioning to follower state", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("leaderTerm", args.Term))

		n.transistionToFollower(args.Term, &args.LeaderID)
	}

	// Reject if there is no matching entry at PrevLogIndex with PrevLogTerm
	if args.PrevLogIndex == 0 && args.PrevLogTerm == 0 {
		// This is valid - leader is sending entries starting from the beginning
	} else {
		// Find the log entry at PrevLogIndex
		var foundEntry *customtypes.Log = nil
		for i := range n.Logs {
			if n.Logs[i].Index == args.PrevLogIndex {
				foundEntry = &n.Logs[i]
				break
			}
		}
		if foundEntry == nil {
			funcLogger.Debug("Rejecting AppendEntries due to missing entry at PrevLogIndex", zap.Uint64("prevLogIndex", args.PrevLogIndex))
			resp.Term = n.CurrentTerm
			resp.Success = false
			return nil
		}
		if foundEntry.Term != args.PrevLogTerm {
			funcLogger.Debug("Rejecting AppendEntries due to term mismatch at PrevLogIndex",
				zap.Uint64("prevLogIndex", args.PrevLogIndex),
				zap.Uint32("prevLogTerm", args.PrevLogTerm),
				zap.Uint32("actualTerm", foundEntry.Term))
			resp.Term = n.CurrentTerm
			resp.Success = false
			return nil
		}
	}

	// Perform a clean up of log entries if needed
	for _, entry := range args.Entries {
		// Find if we already have an entry at this index
		conflictIndex := -1
		for i, existingEntry := range n.Logs {
			if existingEntry.Index == entry.Index {
				if existingEntry.Term != entry.Term {
					conflictIndex = i
					funcLogger.Debug("Found conflicting entry, removing from this point",
						zap.Uint64("index", entry.Index),
						zap.Uint32("existingTerm", existingEntry.Term),
						zap.Uint32("newTerm", entry.Term))
				}
				break
			}
		}

		// If we found a conflict, remove all entries from that point onward
		if conflictIndex >= 0 {
			n.Logs = n.Logs[:conflictIndex]
			break
		}
	}

	// At this point the RPC will be successful, at least as a heartbeat so reset the election timer
	n.resetElectionTimer()
	n.LeaderID = &args.LeaderID // Indicate that this node is now following the leader
	// Stop the node's heartbeat if it was a leader
	// At this point it is guaranteed that this node is not the current leader, since it received an AppendEntries RPC and was about to use it.
	if n.IsLeader {
		funcLogger.Info("Stopping leader heartbeat as a new leader has been elected")
		select {
		case n.LeaderStopChan <- true:
		default:
		}
		n.IsLeader = false // Set the node to not be a leader anymore
	}

	// Append the new entries to the log (only if we don't already have them)
	for _, entry := range args.Entries {
		alreadyExists := false
		for _, existingEntry := range n.Logs {
			if existingEntry.Index == entry.Index {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			funcLogger.Debug("Appending entry to log", zap.Any("entry", entry))
			n.Logs = append(n.Logs, entry)
		}
	}
	// Update the node's last committed index
	if args.LeaderCommit > n.CommitIndex {
		funcLogger.Debug("Updating commit index", zap.Uint64("leaderCommit", args.LeaderCommit), zap.Uint64("currentCommitIndex", n.CommitIndex))
		// Find the index of the last new entry or our last log entry
		lastNewEntryIndex := n.CommitIndex
		if len(args.Entries) > 0 {
			lastNewEntryIndex = args.Entries[len(args.Entries)-1].Index
		} else if len(n.Logs) > 0 {
			lastNewEntryIndex = n.Logs[len(n.Logs)-1].Index
		}
		n.CommitIndex = uint64(math.Min(float64(args.LeaderCommit), float64(lastNewEntryIndex)))
	}

	// apply the logs to the state machine
	for i := n.LastApplied + 1; i <= n.CommitIndex; i++ {
		// Find the log entry with index i
		var entryToApply *customtypes.Log = nil
		for j := range n.Logs {
			if n.Logs[j].Index == i {
				entryToApply = &n.Logs[j]
				break
			}
		}
		if entryToApply != nil {
			funcLogger.Debug("Applying log entry to state machine", zap.Any("entry", *entryToApply))
			n.applyLogToStateMachine(*entryToApply, i)
		}
	}

	funcLogger.Debug("AppendEntries successful", zap.Uint64("commitIndex", n.CommitIndex), zap.Uint64("lastApplied", n.LastApplied))
	// update the current term and success response (term already updated above if needed)

	resp.Term = n.CurrentTerm
	resp.Success = true

	return nil
}

// ClientCommand handles ClientCommand RPC call, it is called by the client to send commands to the Raft cluster
func (n *Node) ClientCommand(args *customtypes.ClientCommandsArgs, resp *customtypes.ClientCommandsResp) error {
	n.mu.Lock()
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
		zap.Any("command", args.Command),
	)

	funcLogger.Info("Received ClientCommand RPC", zap.String("command", string(args.Command.Type)), zap.String("key", args.Command.Key), zap.Bool("isLeader", n.IsLeader))

	// Check if we've already processed this request ID
	if args.RequestID != "" {
		if cachedResp, exists := n.ProcessedRequests[args.RequestID]; exists {
			funcLogger.Debug("Returning cached response for duplicate request", zap.String("requestID", args.RequestID))
			*resp = *cachedResp
			n.mu.Unlock()
			return nil
		}
	}

	// For read consistency, all commands (including reads) must go through the leader
	if !n.IsLeader {
		funcLogger.Debug("Command was received on a follower, returning leader information")

		if n.LeaderID != nil {
			// Find the leader's address from Members
			for _, member := range n.Members {
				if member.ID == *n.LeaderID {
					funcLogger.Info("Redirecting client to leader", zap.String("leaderID", *n.LeaderID), zap.String("leaderAddress", member.Address))

					resp.LeaderID = *n.LeaderID
					resp.LeaderAddress = member.Address
					resp.Redirect = true // tells it to proceed on leader

					n.mu.Unlock()
					return nil
				}
			}
		}

		funcLogger.Warn("No leader found, cannot redirect command")
		// Fail if there is no leader in the cluster
		resp.Success = false
		resp.Error = "No leader known at this moment"
		n.mu.Unlock()
		return nil
	}

	// Check if this is a read-only command that doesn't need replication
	if args.Command.Type == customtypes.GetCommand || args.Command.Type == customtypes.TopologyCommand {
		funcLogger.Debug("Handling read-only command immediately", zap.String("command", string(args.Command.Type)))
		// Handle read-only commands immediately without log replication
		n.executeNonMutationCommands(args.Command, resp)
		funcLogger.Debug("Read-only command processed", zap.Bool("success", resp.Success))
		n.mu.Unlock()
	} else {
		// Handle mutation commands through log replication
		logEntry := customtypes.Log{
			Term:      n.CurrentTerm,
			Content:   args.Command,
			Timestamp: time.Now(),
			Index:     uint64(len(n.Logs)), // The index is the current length
		}

		funcLogger.Info("Appending client command to log", zap.String("command", string(logEntry.Content.Type)), zap.String("key", logEntry.Content.Key), zap.Uint64("index", logEntry.Index))
		n.Logs = append(n.Logs, logEntry)
		n.mu.Unlock()

		// Replicate the log entry to followers (the heartbeat already contains logic to send the appropriate logs)
		n.replicateLogEntriesToMembers([]customtypes.Log{logEntry})

		// Wait until the entry is committed or timeout
		deadline := time.Now().Add(5 * time.Second)
		for {
			n.mu.Lock()
			committed := n.CommitIndex >= logEntry.Index
			n.mu.Unlock()
			if committed {
				break
			}
			if time.Now().After(deadline) {
				funcLogger.Error("Timeout exceeded while waiting for command to be committed", zap.String("command", string(args.Command.Type)), zap.String("key", args.Command.Key))
				resp.Success = false
				resp.Error = "Timeout exceeded while waiting for command to be committed"
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}

		// Apply the command to the state machine if not already applied
		n.mu.Lock()
		if n.LastApplied < logEntry.Index {
			n.applyLogToStateMachine(logEntry, logEntry.Index)
		}
		n.mu.Unlock()

		// Set success for mutation commands
		resp.Success = true
	}

	// Cache the response for this request ID to prevent duplicate execution
	if args.RequestID != "" {
		n.mu.Lock()
		// Create a copy of the response to cache
		cachedResp := *resp
		n.ProcessedRequests[args.RequestID] = &cachedResp
		n.mu.Unlock()
	}

	funcLogger.Info("Client command processed successfully", zap.String("command", string(args.Command.Type)), zap.String("key", args.Command.Key), zap.Bool("success", resp.Success))
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
		n.mu.Lock()
		// Compute per-follower prev index/term based on NextIndex
		var prevLogIndex uint64 = 0
		var prevLogTerm uint32 = 0
		if n.Members[memberIdx].NextIndex > 0 {
			// Find the log entry at NextIndex - 1
			for _, logEntry := range n.Logs {
				if logEntry.Index == n.Members[memberIdx].NextIndex-1 {
					prevLogIndex = logEntry.Index
					prevLogTerm = logEntry.Term
					break
				}
			}
		}
		// Find all log entries starting from NextIndex
		entries := make([]customtypes.Log, 0)
		for _, logEntry := range n.Logs {
			if logEntry.Index >= n.Members[memberIdx].NextIndex {
				entries = append(entries, logEntry)
			}
		}
		term := n.CurrentTerm
		leaderCommit := n.CommitIndex
		n.mu.Unlock()

		go n.sendHeartbeatToMember(member, memberIdx, &customtypes.AppendEntriesArgs{
			Term:         term,
			LeaderID:     n.ID,
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevLogTerm,
			Entries:      entries,
			LeaderCommit: leaderCommit,
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
	case customtypes.TopologyCommand:
		funcLogger := logger.Log.With(zap.String("nodeID", n.ID))
		funcLogger.Debug("Processing topology command")
		// Build topology information
		topology := &customtypes.TopologyInfo{
			CurrentTerm: n.CurrentTerm,
			LeaderID:    "",
			Members:     make([]customtypes.MemberInfo, len(n.Members)),
		}

		if n.LeaderID != nil {
			topology.LeaderID = *n.LeaderID
		}

		// Build member information
		for _, member := range n.Members {
			memberInfo := customtypes.MemberInfo{
				ID:       member.ID,
				Address:  member.Address,
				Term:     n.CurrentTerm,
				IsOnline: true, // Assume online for simplicity
				Role:     "follower",
			}

			if n.LeaderID != nil && member.ID == *n.LeaderID {
				memberInfo.Role = "leader"
			}

			topology.Members = append(topology.Members, memberInfo)
		}

		// Put topology directly in Result field for simpler RPC serialization
		resp.Result = topology
		funcLogger.Debug("Topology command completed", zap.Int("memberCount", len(topology.Members)), zap.String("leaderID", topology.LeaderID))
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
		funcLogger.Warn("Failed to dial member for vote request", zap.String("memberID", member.ID), zap.Error(err))
		return
	}
	defer client.Close() // Ensure the client is closed after the request

	// This prevents an array out of bounds error if there are no logs
	lastLogIndex := uint64(0)
	lastLogTerm := uint32(0)
	if len(n.Logs) > 0 {
		lastLogIndex = n.Logs[len(n.Logs)-1].Index
		lastLogTerm = n.Logs[len(n.Logs)-1].Term
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
		funcLogger.Warn("Failed to call RequestVote on member", zap.String("memberID", member.ID), zap.Error(err))
		return
	}
	funcLogger.Debug("Received vote response from member", zap.Any("response", resp))

	n.mu.Lock()
	defer n.mu.Unlock()

	// If the peer has a higher term, step down to follower
	if resp.Term > n.CurrentTerm {
		funcLogger.Debug("Vote response indicates higher term, stepping down", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("respTerm", resp.Term))
		n.transistionToFollower(resp.Term, nil)
		return
	}

	// If the response is successful, increment the votes received
	if resp.VoteGranted {
		funcLogger.Info("Vote granted by member", zap.String("memberID", member.ID))
		n.VotesReceived++

		// If the node has received a majority of votes, it becomes the leader
		majorityThreshold := len(n.Members)/2 + 1
		if n.VotesReceived >= majorityThreshold && !n.IsLeader {
			funcLogger.Info("Node has become the leader", zap.String("nodeID", n.ID), zap.Uint32("currentTerm", n.CurrentTerm))
			n.LeaderID = &n.ID
			n.IsLeader = true

			// Initialize NextIndex for all followers to lastLogIndex + 1
			lastLogIndex := uint64(0)
			if len(n.Logs) > 0 {
				lastLogIndex = n.Logs[len(n.Logs)-1].Index
			}
			for idx := range n.Members {
				if n.Members[idx].ID == n.ID {
					continue
				}
				n.Members[idx].NextIndex = lastLogIndex + 1
				// MatchIndex stays as is (0) until successful append
			}

			// Reset the election timer as the node is now the leader
			n.resetElectionTimer()
			// Begin broadcasting heartbeats to other members
			funcLogger.Info("Node became leader, starting heartbeat broadcasts")
			go n.broadcastHeartbeat()
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
		funcLogger.Warn("Failed to dial member for heartbeat", zap.String("memberID", member.ID), zap.Error(err))
		return
	}
	defer client.Close() // Ensure the client is closed after the request

	for {
		var resp customtypes.AppendEntriesResp
		callErr := client.Call("Node.AppendEntries", args, &resp)
		if callErr != nil {
			funcLogger.Warn("Failed to call AppendEntries on member", zap.String("memberID", member.ID), zap.Error(callErr))
			return
		}

		funcLogger.Debug("Received AppendEntries response from member", zap.Any("response", resp))

		n.mu.Lock()
		// If the member is in a higher term, update the current term, step down and reset the election timer
		if resp.Term > n.CurrentTerm {
			funcLogger.Debug("Member is in a higher term, stepping down", zap.Uint32("currentTerm", n.CurrentTerm), zap.Uint32("memberTerm", resp.Term))
			n.transistionToFollower(resp.Term, nil) // Step down to follower state
			n.mu.Unlock()
			return
		}

		if resp.Success {
			// To avoid an out of bounds error, we check if the entries array is not empty
			if len(args.Entries) > 0 {
				lastIdx := args.Entries[len(args.Entries)-1].Index
				n.Members[memberIdx].NextIndex = lastIdx + 1 // Update the next index to the last entry's index + 1
				n.Members[memberIdx].MatchIndex = lastIdx    // Update the match index to the last entry's index
			} else {
				// Heartbeat without entries: ensure NextIndex is at least current log length
				if n.Members[memberIdx].NextIndex < uint64(len(n.Logs)) {
					n.Members[memberIdx].NextIndex = uint64(len(n.Logs))
				}
			}

			n.updateCommitIndex()
			n.mu.Unlock()

			funcLogger.Debug("Heartbeat sent successfully, updated member details",
				zap.String("memberID", member.ID),
				// Note: read unlocked values may be slightly stale in logs
				zap.Uint64("nextIndex", n.Members[memberIdx].NextIndex),
				zap.Uint64("matchIndex", n.Members[memberIdx].MatchIndex),
			)
			return
		}

		// Backoff: decrement NextIndex and retry
		if n.Members[memberIdx].NextIndex > 0 {
			n.Members[memberIdx].NextIndex--
		}

		// Recompute args based on new NextIndex
		var prevLogIndex uint64 = 0
		var prevLogTerm uint32 = 0
		if n.Members[memberIdx].NextIndex > 0 {
			// Find the log entry at NextIndex - 1
			for _, logEntry := range n.Logs {
				if logEntry.Index == n.Members[memberIdx].NextIndex-1 {
					prevLogIndex = logEntry.Index
					prevLogTerm = logEntry.Term
					break
				}
			}
		}
		entries := make([]customtypes.Log, 0)
		// Find all log entries starting from NextIndex
		for _, logEntry := range n.Logs {
			if logEntry.Index >= n.Members[memberIdx].NextIndex {
				entries = append(entries, logEntry)
			}
		}
		args = &customtypes.AppendEntriesArgs{
			Term:         n.CurrentTerm,
			LeaderID:     n.ID,
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevLogTerm,
			Entries:      entries,
			LeaderCommit: n.CommitIndex,
		}
		n.mu.Unlock()
		// Loop and retry
	}
}

// updateCommitIndex updates the commit index based on the logs and the members' match indices
func (n *Node) updateCommitIndex() {
	funcLogger := logger.Log.With(zap.String("nodeID", n.ID))

	funcLogger.Debug("Updating commit index based on members' match indices", zap.Uint64("currentCommitIndex", n.CommitIndex), zap.Int("logsLength", len(n.Logs)))

	// Count how many followers have replicated each log index
	// Go through each log entry and check if majority has replicated it
	for _, logEntry := range n.Logs {
		if logEntry.Index <= n.CommitIndex {
			continue // Skip already committed entries
		}
		count := 1 // Count the leader itself
		for _, member := range n.Members {
			if member.ID == n.ID {
				continue
			}
			if member.MatchIndex >= logEntry.Index {
				count++
			}
		}

		funcLogger.Debug("Checking for majority replication", zap.Uint64("index", logEntry.Index), zap.Int("count", count))

		// If a majority has replicated this index and it's from the current term
		majorityThreshold := len(n.Members)/2 + 1
		if count >= majorityThreshold && logEntry.Term == n.CurrentTerm {
			n.CommitIndex = logEntry.Index
			funcLogger.Debug("Updated CommitIndex", zap.Uint64("commitIndex", n.CommitIndex))
		} else {
			break // Stop at first non-majority entry
		}
	}

	// Apply newly committed entries
	for i := n.LastApplied + 1; i <= n.CommitIndex; i++ {
		// Find the log entry with index i
		for _, logEntry := range n.Logs {
			if logEntry.Index == i {
				n.applyLogToStateMachine(logEntry, i)
				break
			}
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
			n.LeaderStopChan = make(chan bool, 1) // Reset the channel so it's back to a clean state
			return
		default:
			for memberIdx, member := range n.Members {
				if member.ID == n.ID {
					continue // Skip itself
				}

				n.mu.Lock()
				// For heartbeats, use simple approach - no log matching required
				args := &customtypes.AppendEntriesArgs{
					Term:         n.CurrentTerm,
					LeaderID:     n.ID,
					PrevLogIndex: 0, // Simple heartbeat with no log consistency check
					PrevLogTerm:  0,
					Entries:      []customtypes.Log{}, // Empty heartbeat
					LeaderCommit: n.CommitIndex,       // Current commit index
				}
				n.mu.Unlock()

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
	for {
		<-n.ElectionTimer.C
		// When the election timer expires, become a candidate
		funcLogger.Info("Election timer expired, transitioning to candidate state")
		n.mu.Lock()
		n.transistionToCandidate()
		n.mu.Unlock()
	}
}

// transistionToCandidate transistions the node to a candidate state, increments the term, and requests votes from other members
func (n *Node) transistionToCandidate() {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	funcLogger.Info("Node is becoming a candidate", zap.String("nodeID", n.ID), zap.Uint32("newTerm", n.CurrentTerm+1))

	// Increment the current term
	n.CurrentTerm++
	n.VotedFor = n.ID   // Vote for itself
	n.VotesReceived = 1 // Start with one vote (itself)

	// Reset the election timer
	n.resetElectionTimer()

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
func (n *Node) transistionToFollower(term uint32, leaderID *string) {
	funcLogger := logger.Log.With(
		zap.String("nodeID", n.ID),
	)
	funcLogger.Info("Node is transitioning to follower state", zap.String("nodeID", n.ID), zap.Uint32("newTerm", term))

	n.CurrentTerm = term
	n.VotedFor = ""       // Reset the voted for field
	n.LeaderID = leaderID // Set the leader ID to the given leader ID
	if n.IsLeader {
		funcLogger.Info("Stopping leader heartbeat as a new leader has been elected")
		n.IsLeader = false       // Set the node to not be a leader anymore
		n.LeaderStopChan <- true // Stop the leader's heartbeat if it was a leader
	}
	n.resetElectionTimer() // Reset the election timer
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
		HeartBeatInterval: 50 * time.Millisecond, // Much less than election timeout (150-300ms)
		// Intialize all other fields to their default values
		CurrentTerm:       0,
		VotedFor:          "",
		Logs:              []customtypes.Log{},
		CommitIndex:       0,
		LastApplied:       0,
		VotesReceived:     0,
		LeaderID:          nil, // No leader at the start
		IsLeader:          false,
		LeaderStopChan:    make(chan bool, 1),                               // Buffered to avoid blocking
		State:             make(map[string]int),                             // Initialize the state machine
		ProcessedRequests: make(map[string]*customtypes.ClientCommandsResp), // Initialize request ID tracking
	}
}

// Init initializes the Raft node, registers it for RPC, and starts listening for incoming requests
func Init(config *customtypes.Config) {
	// Initialize a new node
	logger.Log.Info("Initializing a new Raft node")
	node := newNode(config)
	logger.Log.Info("New Raft node initialized", zap.String("nodeID", node.ID))

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
	funcLogger.Info("Listening for RPC requests", zap.String("address", address))

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

// resetElectionTimer safely stops, and resets the election timer
func (n *Node) resetElectionTimer() {
	n.ElectionTimer.Stop()
	n.ElectionTimer.Reset(helpers.GetNewElectionTimeout())
}
