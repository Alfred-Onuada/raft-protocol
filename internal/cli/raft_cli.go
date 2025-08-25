package raftcli

import (
	"fmt"
	"net"
	"net/rpc"
	"time"

	logger "github.com/Alfred-Onuada/raft-protocol/internal/logging"
	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
	"go.uber.org/zap"
)

// ExecuteCommand connects to the specified Raft node and executes the given command.
// It returns the result of the command or an error if the command failed.
func ExecuteCommand(nodeAddr string, command customtypes.Command, requestID string) (*customtypes.ClientCommandsResp, error) {
	logger.Log.Debug("Connecting to Raft node",
		zap.String("nodeAddress", nodeAddr),
	)
	// Dial with timeout to avoid hanging
	conn, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to node %s: %v", nodeAddr, err)
	}
	client := rpc.NewClient(conn)

	// prepare the RPC arguments
	args := &customtypes.ClientCommandsArgs{
		Command:   command,
		RequestID: requestID,
	}
	var resp customtypes.ClientCommandsResp

	logger.Log.Info("Sending command to Raft node",
		zap.String("nodeAddress", nodeAddr),
		zap.String("command", string(command.Type)),
		zap.String("key", command.Key),
	)
	err = client.Call("Node.ClientCommand", args, &resp)
	defer client.Close() // Ensure the client is closed after the request
	if err != nil {
		return nil, fmt.Errorf("RPC error: %v", err)
	}

	// The leader address will never be nil if redirect is true but this is to ensure we don't panic
	if resp.Redirect && resp.LeaderAddress != "" {
		fmt.Printf("Redirecting to leader at %s\n", resp.LeaderAddress)

		// Execute on leader but preserve redirect info in response
		redirectedResp, err := ExecuteCommand(resp.LeaderAddress, command, requestID)
		if err != nil {
			return nil, err
		}
		// Add redirect information to the final response
		redirectedResp.Redirect = true
		redirectedResp.LeaderAddress = resp.LeaderAddress
		redirectedResp.LeaderID = resp.LeaderID
		return redirectedResp, nil
	}

	// Return the response
	return &resp, nil
}
