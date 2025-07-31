package main

import (
	"encoding/json"
	"fmt"

	raftcli "github.com/Alfred-Onuada/raft-protocol/internal/cli"
	"github.com/Alfred-Onuada/raft-protocol/internal/flags"
	logger "github.com/Alfred-Onuada/raft-protocol/internal/logging"
	customtypes "github.com/Alfred-Onuada/raft-protocol/internal/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// Initialize the logger
	logger.Init(zapcore.DebugLevel, nil)

	// Parse command-line arguments
	config := flags.InitCLIFlags()

	logger.Log.Debug("Executing Raft CLI command",
		zap.Any("config", config),
	)

	// Connect to the Raft node and execute the command
	result, err := raftcli.ExecuteCommand(config.NodeAddress, customtypes.Command{
		Type:  config.CommandType,
		Key:   config.Key,
		Value: config.Value,
	})
	if err != nil {
		logger.Log.Error("Error executing command:", zap.Error(err))
		return
	}

	// pretty print the result
	logger.Log.Debug("Command executed successfully",
		zap.Any("result", result),
	)

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Log.Error("Error marshalling result to JSON", zap.Error(err))
		return
	}

	// print the result to the console
	fmt.Println(string(output))
}
