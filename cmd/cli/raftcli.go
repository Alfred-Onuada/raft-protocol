package main

import (
	"flag"
	"fmt"
	"net/rpc"
	"os"
	"strconv"
	"strings"
	"time"

	logger "github.com/Alfred-Onuada/raft-protocol/internal/logging"
	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Command struct {
	Type  customtypes.CommandType
	Key   string
	Value *int
}

func main() {
	// Initialize the logger
	logger.Init(zapcore.DebugLevel, nil)

	// Define CLI flags
	nodeAddr := flag.String("node", "localhost:7500", "Address of the Raft node to connect to (host:port)")
	flag.Parse()

	// Parse command-line arguments
	if len(flag.Args()) < 1 {
		logger.Log.Info("Usage: raftcli --node=<host:port> <command> [args...]")
		logger.Log.Info("Commands: set <key> <value>, get <key>, increase <key>, decrease <key>, delete <key>")
		os.Exit(1)
	}

	cmdArgs := flag.Args()
	command, err := parseCommand(cmdArgs)
	if err != nil {
		logger.Log.Sugar().Fatalf("Invalid command: %v", err)
	}

	logger.Log.Debug("Parsed command", zap.Any("command", command), zap.String("nodeAddr", *nodeAddr))

	// Connect to the Raft node and execute the command
	result, err := executeCommand(*nodeAddr, command)
	if err != nil {
		logger.Log.Sugar().Fatalf("Error executing command: %v", err)
	}

	logger.Log.Info(result)
}

func parseCommand(args []string) (Command, error) {
	logger.Log.Debug("Parsing command", zap.Strings("args", args))
	cmdType := strings.ToLower(args[0])
	switch cmdType {
	case "set":
		if len(args) != 3 {
			return Command{}, fmt.Errorf("set command requires key and value")
		}
		v, err := strconv.ParseInt(args[2], 10, 0)
		if err != nil {
			return Command{}, fmt.Errorf("invalid value: %v", err)
		}
		value := int(v)
		return Command{Type: customtypes.SetCommand, Key: args[1], Value: &value}, nil
	case "get":
		if len(args) != 2 {
			return Command{}, fmt.Errorf("get command requires key")
		}
		return Command{Type: customtypes.GetCommand, Key: args[1], Value: nil}, nil
	case "increase":
		if len(args) != 2 {
			return Command{}, fmt.Errorf("increase command requires key")
		}
		return Command{Type: customtypes.IncreaseCommand, Key: args[1], Value: nil}, nil
	case "decrease":
		if len(args) != 2 {
			return Command{}, fmt.Errorf("decrease command requires key")
		}
		return Command{Type: customtypes.DecreaseCommand, Key: args[1], Value: nil}, nil
	case "delete":
		if len(args) != 2 {
			return Command{}, fmt.Errorf("delete command requires key")
		}
		return Command{Type: customtypes.DelCommand, Key: args[1], Value: nil}, nil
	default:
		return Command{}, fmt.Errorf("unknown command: %s", cmdType)
	}
}

func executeCommand(nodeAddr string, command Command) (string, error) {
	client, err := rpc.Dial("tcp", nodeAddr)
	if err != nil {
		return "", fmt.Errorf("failed to connect to node %s: %v", nodeAddr, err)
	}

	args := &customtypes.ClientCommandsArgs{Command: customtypes.Command{
		Type:  command.Type,
		Key:   command.Key,
		Value: command.Value,
	}}
	var resp customtypes.ClientCommandsResp
	err = client.Call("Node.ClientCommand", args, &resp)
	client.Close()
	if err != nil {
		return "", fmt.Errorf("RPC error: %v", err)
	}

	if !resp.Success {
		if resp.Error == "Command received on a follower, please contact the leader" && resp.LeaderAddress != "" {
			fmt.Printf("Redirecting to leader at %s\n", resp.LeaderAddress)

			// Redirect to the leader
			nodeAddr = resp.LeaderAddress
			time.Sleep(5 * time.Second)
			return executeCommand(nodeAddr, command)
		}
		return "", fmt.Errorf("command failed: %s", resp.Error)
	}

	// Format the result
	if command.Type == customtypes.GetCommand && resp.Result != nil {
		return fmt.Sprintf("%v", resp.Result), nil
	}
	return "Success", nil
}
